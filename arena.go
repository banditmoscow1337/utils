package utils

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	MaxArenaSize        = 1<<32 - 1
	RegionSize          = 2 * 1024 * 1024
	MaxRegions          = (MaxArenaSize + RegionSize - 1) / RegionSize
	TLABChunkSize       = 64 * 1024
	RelocationWatermark = 0.40
	NumTLABs            = 128
	SizeClassCount      = 9
)

// Arena keeps 64-bit offsets throughout the allocator. Its mapped layout is
// different from Arena's; a file mapped with one format must not be opened with
// the other. Allocations still fit within a single RegionSize-sized region.
//
// The metadata grows with the mapping instead of reserving space for every
// possible 64-bit offset. As with Arena, Close requires all users to be done.
type Arena struct {
	basePtr   unsafe.Pointer
	size      uint64
	dataStart uint64
	meta      *arenaMeta
	regions   []Region
	tlabPool  sync.Pool
	tlabHead  atomic.Pointer[localTLAB]
}

type arenaMeta struct {
	bumpOffset  uint64
	magic       uint64
	mappingSize uint64
	_           [CacheLineSize - 24]byte
}

const arenaMagic = uint64(0x6172656e61363401)

// Region64 uses the low bit of each free-list head as a short-lived lock.
// Block payload offsets are multiples of eight, so the bit is always free.
// This serializes access to NextFree without a 128-bit CAS or per-free heap
// allocation, and permits the entire uint64 range of offsets (no tag wrap).
type Region struct {
	activeCount atomic.Int32
	liveBytes   atomic.Int32
	evacuate    atomic.Uint32
	users       atomic.Int32
	freeHeads   [SizeClassCount]uint64
	_           [40]byte // 16 + 72 + 40 = 128 bytes.
}

// BlockHeader64 precedes each allocated payload. Size includes the header.
type BlockHeader struct {
	Size     uint64
	NextFree uint64
}

type localTLAB struct {
	offset    uint64
	rem       uint32
	claims    int32
	regionIdx uint64
	inUse     atomic.Bool
	_         [4]byte
	next      *localTLAB // Immutable after registration.
	_         [CacheLineSize - 40]byte
}

const blockHeaderSize64 = uint64(unsafe.Sizeof(BlockHeader{}))

// NewArena maps size bytes. The mapping must have room for its variable-sized
// region table; fd == -1 creates a private anonymous mapping.
func NewArena[T any](size uint64, fd int) (*Arena, error) {
	if size > uint64(int(^uint(0)>>1)) {
		return nil, errors.New("invalid arena size: mapping exceeds int")
	}
	regionCount := (size + RegionSize - 1) / RegionSize
	metaSize := uint64(unsafe.Sizeof(arenaMeta{})) + regionCount*uint64(unsafe.Sizeof(Region{}))
	dataStart := (metaSize + 7) &^ uint64(7)
	if size < dataStart+blockHeaderSize64+8 {
		return nil, errors.New("invalid arena size: mapping cannot hold metadata")
	}

	flags := unix.MAP_SHARED
	if fd == -1 {
		flags = unix.MAP_ANON | unix.MAP_PRIVATE
	}
	b, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ|unix.PROT_WRITE, flags)
	if err != nil {
		return nil, err
	}
	a := &Arena{
		basePtr:   unsafe.Pointer(&b[0]),
		size:      size,
		dataStart: dataStart,
	}
	a.meta = (*arenaMeta)(a.basePtr)
	a.regions = unsafe.Slice((*Region)(unsafe.Add(a.basePtr, unsafe.Sizeof(arenaMeta{}))), int(regionCount))
	if atomic.LoadUint64(&a.meta.bumpOffset) == 0 {
		atomic.CompareAndSwapUint64(&a.meta.magic, 0, arenaMagic)
		atomic.CompareAndSwapUint64(&a.meta.mappingSize, 0, size)
		if atomic.LoadUint64(&a.meta.magic) == arenaMagic &&
			atomic.LoadUint64(&a.meta.mappingSize) == size {
			atomic.CompareAndSwapUint64(&a.meta.bumpOffset, 0, dataStart)
		}
	}
	bump := atomic.LoadUint64(&a.meta.bumpOffset)
	if atomic.LoadUint64(&a.meta.magic) != arenaMagic ||
		atomic.LoadUint64(&a.meta.mappingSize) != size ||
		bump < dataStart || bump > size {
		_ = unix.Munmap(b)
		return nil, errors.New("invalid arena metadata or mapping size")
	}
	return a, nil
}

func (a *Arena) NeedsEvacuation(offset uint64) bool {
	if offset == 0 {
		return false
	}
	return a.regions[offset/RegionSize].evacuate.Load() == 1
}

func (a *Arena) AllocDense(size uint64) uint64 {
	if size == 0 {
		return 0
	}
	total := blockSize64(size)
	if class, rounded := GetSizeClass(uint32(total)); class != -1 {
		total = uint64(rounded)
	}
	return a.AllocBatch(total-blockHeaderSize64, 1)
}


func GetSizeClass(size uint32) (int, uint32) {
	switch {
	case size <= 64:
		return 0, 64
	case size <= 128:
		return 1, 128
	case size <= 272:
		return 2, 272
	case size <= 512:
		return 3, 512
	case size <= 1088:
		return 4, 1088
	case size <= 1600:
		return 5, 1600
	case size <= 2176:
		return 6, 2176
	case size <= 4224:
		return 7, 4224
	case size <= 8192:
		return 8, 8192
	default:
		return -1, size
	}
}

// AllocChunk reserves raw bytes including caller-written block headers.
// count is the number of block claims transferred to the caller.
func (a *Arena) AllocChunk(size uint64, count int32) uint64 {
	if size == 0 || size > RegionSize {
		panic("utils: invalid arena chunk size")
	}
	aligned := (size + 7) &^ uint64(7)
	if aligned > RegionSize {
		panic("utils: invalid arena chunk size")
	}
	return a.reserve(aligned, count)
}

func (a *Arena) PopBatch(classIdx int, max int) (headOff, tailOff uint64, count int) {
	if classIdx < 0 || classIdx >= SizeClassCount || max <= 0 {
		return 0, 0, 0
	}
	startRegion := atomic.LoadUint64(&a.meta.bumpOffset) / RegionSize
	if startRegion >= uint64(len(a.regions)) {
		startRegion = uint64(len(a.regions) - 1)
	}
	for r := int(startRegion); r >= 0; r-- {
		region := &a.regions[r]
		headAddr := &region.freeHeads[classIdx]
		if region.evacuate.Load() == 1 || atomic.LoadUint64(headAddr) == 0 {
			continue
		}
		// Keep the region leased until the removed blocks regain their claims.
		region.enter()
		if region.evacuate.Load() == 1 {
			a.leaveRegion(uint64(r))
			continue
		}
		head := lockHead64(headAddr)
		if head == 0 {
			unlockHead64(headAddr, 0)
			a.leaveRegion(uint64(r))
			continue
		}
		last := head
		hdr := (*BlockHeader)(a.GetPtr(last - blockHeaderSize64))
		next := hdr.NextFree
		bytes := int32(hdr.Size)
		count = 1
		for count < max && next != 0 {
			last = next
			hdr = (*BlockHeader)(a.GetPtr(last - blockHeaderSize64))
			next = hdr.NextFree
			bytes += int32(hdr.Size)
			count++
		}
		region.activeCount.Add(int32(count))
		region.liveBytes.Add(bytes)
		hdr.NextFree = 0
		unlockHead64(headAddr, next)
		a.leaveRegion(uint64(r))
		return head, last, count
	}
	return 0, 0, 0
}

func (a *Arena) Alloc(size uint64) uint64 {
	if size == 0 {
		return 0
	}
	allocSize := blockSize64(size)
	if class, rounded := GetSizeClass(uint32(allocSize)); class != -1 {
		if head, _, count := a.PopBatch(class, 1); count > 0 {
			return head
		}
		allocSize = uint64(rounded)
	}
	if allocSize > TLABChunkSize/2 {
		return a.AllocDense(size)
	}
	t := a.acquireTLAB()
	defer a.releaseTLAB(t)
	if uint64(t.rem) < allocSize || (t.offset != 0 && a.NeedsEvacuation(t.offset)) {
		a.refundTLAB(t)
		t.offset = a.reserve(TLABChunkSize, TLABChunkSize/64)
		t.rem = TLABChunkSize
		t.regionIdx = t.offset / RegionSize
		t.claims = TLABChunkSize / 64
	}
	off := t.offset
	hdr := (*BlockHeader)(a.GetPtr(off))
	hdr.Size = allocSize
	hdr.NextFree = 0
	t.offset += allocSize
	t.rem -= uint32(allocSize)
	t.claims--
	return off + blockHeaderSize64
}

// AddActiveCount adjusts claims for a reservation already owned by the caller.
func (a *Arena) AddActiveCount(offset uint64, delta int32) {
	if offset == 0 || delta == 0 {
		return
	}
	idx := offset / RegionSize
	region := &a.regions[idx]
	region.enter()
	region.activeCount.Add(delta)
	a.leaveRegion(idx)
}

// AllocBatch reserves one payload with count block claims. For raw storage
// split into independently freed blocks, use AllocChunk.
func (a *Arena) AllocBatch(size uint64, count int32) uint64 {
	total := blockSize64(size)
	off := a.reserve(total, count)
	hdr := (*BlockHeader)(a.GetPtr(off))
	hdr.Size = total
	hdr.NextFree = 0
	return off + blockHeaderSize64
}

func (a *Arena) Free(offset uint64) {
	if offset == 0 {
		return
	}
	hdr := (*BlockHeader)(a.GetPtr(offset - blockHeaderSize64))
	idx := (offset - blockHeaderSize64) / RegionSize
	region := &a.regions[idx]
	size := hdr.Size
	live := region.liveBytes.Add(-int32(size))
	start := idx * RegionSize
	bump := atomic.LoadUint64(&a.meta.bumpOffset)
	if bump > start+(RegionSize*2)/5 && live > 0 && float64(live)/RegionSize < RelocationWatermark {
		region.evacuate.Store(1)
	}
	class, rounded := GetSizeClass(uint32(size))
	// Exact-sized blocks may only join a class if they physically fill it.
	if class != -1 && uint64(rounded) == size && region.evacuate.Load() == 0 {
		headAddr := &region.freeHeads[class]
		head := lockHead64(headAddr)
		hdr.NextFree = head
		unlockHead64(headAddr, offset)
	}
	// This block's own claim prevents purge until after its list update.
	if region.activeCount.Add(-1) == 0 {
		a.purgeRegion(idx)
	}
}

func (a *Arena) reserve(size uint64, count int32) uint64 {
	if size == 0 || size > RegionSize || count <= 0 {
		panic("utils: invalid arena reservation")
	}
	for {
		old := atomic.LoadUint64(&a.meta.bumpOffset)
		start := old
		rem := uint64(RegionSize) - old%RegionSize
		if size > rem {
			start += rem
		}
		end := start + size
		if start >= a.size || end > a.size {
			panic("utils: mmap arena OOM")
		}
		idx := start / RegionSize
		region := &a.regions[idx]
		region.enter()
		if !atomic.CompareAndSwapUint64(&a.meta.bumpOffset, old, end) {
			a.leaveRegion(idx)
			continue
		}
		region.activeCount.Add(count)
		region.liveBytes.Add(int32(size))
		a.leaveRegion(idx)
		return start
	}
}

func (a *Arena) purgeRegion(idx uint64) {
	region := &a.regions[idx]
	if region.activeCount.Load() != 0 || !region.users.CompareAndSwap(0, -1) {
		return
	}
	defer region.users.Store(0)
	if region.activeCount.Load() != 0 {
		return
	}
	for i := range region.freeHeads {
		// A region lease prevents any concurrent PopBatch while users == -1.
		atomic.StoreUint64(&region.freeHeads[i], 0)
	}
	region.liveBytes.Store(0)
	region.evacuate.Store(0)
	start := idx * RegionSize
	end := min(start+RegionSize, a.size)
	page := uint64(unix.Getpagesize())
	// For large mappings the region table itself spans multiple regions.
	// Preserve every metadata page, not just pages in region zero.
	start = max(start, (a.dataStart+page-1)&^(page-1))
	end &^= page - 1
	if start >= end {
		return
	}
	ptr := unsafe.Add(a.basePtr, uintptr(start))
	_ = unix.Madvise(unsafe.Slice((*byte)(ptr), int(end-start)), unix.MADV_DONTNEED)
}

// lockHead64 returns the payload offset without the lock bit.
func lockHead64(addr *uint64) uint64 {
	spins := 0
	for {
		old := atomic.LoadUint64(addr)
		if old&1 == 0 && atomic.CompareAndSwapUint64(addr, old, old|1) {
			return old
		}
		spins++
		if spins == 16 {
			runtime.Gosched()
			spins = 0
		}
	}
}

func unlockHead64(addr *uint64, offset uint64) {
	atomic.StoreUint64(addr, offset)
}

//go:nosplit
func (a *Arena) BasePtr() unsafe.Pointer {
	return a.basePtr
}

func (a *Arena) GetPtr(offset uint64) unsafe.Pointer {
	if offset == 0 {
		return nil
	}
	return unsafe.Add(a.basePtr, uintptr(offset))
}

//go:nosplit
func (a *Arena) GetOffset(ptr unsafe.Pointer) uint64 {
	if ptr == nil {
		return 0
	}
	diff := uintptr(ptr) - uintptr(a.basePtr)
	if uint64(diff) >= a.size {
		panic("utils: pointer outside arena")
	}
	return uint64(diff)
}

// Close is idempotent after a successful unmap.
func (a *Arena) Close() error {
	if a.basePtr == nil {
		return nil
	}
	b := unsafe.Slice((*byte)(a.basePtr), int(a.size))
	err := unix.Munmap(b)
	if err == nil {
		a.basePtr = nil
		a.meta = nil
		a.regions = nil
	}
	return err
}

func blockSize64(size uint64) uint64 {
	if size > RegionSize-blockHeaderSize64 {
		panic("utils: allocation exceeds one arena region")
	}
	total := ((size + 7) &^ uint64(7)) + blockHeaderSize64
	if total > RegionSize {
		panic("utils: allocation exceeds one arena region")
	}
	return total
}

func (r *Region) enter() {
	for {
		n := r.users.Load()
		if n >= 0 && r.users.CompareAndSwap(n, n+1) {
			return
		}
		runtime.Gosched()
	}
}

func (a *Arena) leaveRegion(idx uint64) {
	r := &a.regions[idx]
	if r.users.Add(-1) == 0 && r.activeCount.Load() == 0 {
		a.purgeRegion(idx)
	}
}

func (a *Arena) acquireTLAB() *localTLAB {
	for {
		if v := a.tlabPool.Get(); v != nil {
			t := v.(*localTLAB)
			if t.inUse.CompareAndSwap(false, true) {
				return t
			}
			continue
		}
		for t := a.tlabHead.Load(); t != nil; t = t.next {
			if t.inUse.CompareAndSwap(false, true) {
				return t
			}
		}
		t := &localTLAB{}
		t.inUse.Store(true)
		for {
			head := a.tlabHead.Load()
			t.next = head
			if a.tlabHead.CompareAndSwap(head, t) {
				return t
			}
		}
	}
}

func (a *Arena) releaseTLAB(t *localTLAB) {
	t.inUse.Store(false)
	a.tlabPool.Put(t)
}

func (a *Arena) refundTLAB(t *localTLAB) {
	if t.offset == 0 {
		return
	}
	region := &a.regions[t.regionIdx]
	region.enter()
	region.liveBytes.Add(-int32(t.rem))
	region.activeCount.Add(-t.claims)
	t.offset, t.rem, t.claims = 0, 0, 0
	a.leaveRegion(t.regionIdx)
}
