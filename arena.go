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

type Region struct {
	activeCount atomic.Int32  // 4 bytes
	liveBytes   atomic.Int32  // 4 bytes
	evacuate    atomic.Uint32 // 4 bytes
	users       atomic.Int32  // >= 0: allocator leases; -1: exclusive purge
	freeHeads   [10]uint64    // 80 bytes
	_           [32]byte      // 32 bytes: Pad to exactly 128 bytes (16 + 80 + 32)
}

type arenaMeta struct {
	bumpOffset uint32
	_          PadCacheLineMinus4 // Isolate highly contended bumpOffset from the regions array
	regions    [MaxRegions]Region
}

type blockHeader struct {
	size     uint32
	nextFree uint32
}

type BlockHeader struct {
	Size     uint32
	NextFree uint32
}

type Arena struct {
	basePtr  unsafe.Pointer
	size     uint32
	_        uint32
	meta     *arenaMeta
	tlabPool sync.Pool
	// Own reservations even if sync.Pool drops cached entries.
	tlabHead atomic.Pointer[localTLAB]
}

type localTLAB struct {
	offset    uint32
	rem       uint32
	claims    int32
	regionIdx uint32
	inUse     atomic.Bool
	_         [4]byte
	next      *localTLAB // Immutable after registration.
	_         [CacheLineSize - 32]byte
}

func NewArena[T any](size uint32, fd int) (*Arena, error) {
	if uint64(size) < uint64(unsafe.Sizeof(arenaMeta{}))+8 {
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

	arena := &Arena{
		basePtr: unsafe.Pointer(&b[0]),
		size:    size,
	}
	arena.meta = (*arenaMeta)(arena.basePtr)
	if atomic.LoadUint32(&arena.meta.bumpOffset) == 0 {
		atomic.StoreUint32(&arena.meta.bumpOffset, uint32(unsafe.Sizeof(arenaMeta{}))+8)
	}

	return arena, nil
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

func (a *Arena) NeedsEvacuation(offset uint32) bool {
	if offset == 0 {
		return false
	}
	return a.meta.regions[offset/RegionSize].evacuate.Load() == 1
}

func (a *Arena) AllocDense(size uint32) uint32 {
	if size == 0 {
		return 0
	}
	total := blockSize(size)
	if class, rounded := GetSizeClass(total); class != -1 {
		total = rounded
	}
	return a.AllocBatch(total-8, 1)
}

// AllocChunk reserves raw bytes including caller-written block headers.
// count is the number of block claims transferred to the caller.
func (a *Arena) AllocChunk(size uint32, count int32) uint32 {
	aligned := (uint64(size) + 7) &^ uint64(7)
	if aligned == 0 || aligned > RegionSize {
		panic("utils: invalid arena chunk size")
	}
	return a.reserve(uint32(aligned), count)
}

func (a *Arena) PopBatch(classIdx int, max int) (headOff, tailOff uint32, count int) {
	if classIdx < 0 || classIdx >= SizeClassCount || max <= 0 {
		return 0, 0, 0
	}
	startRegion := atomic.LoadUint32(&a.meta.bumpOffset) / RegionSize
	if startRegion >= MaxRegions {
		startRegion = MaxRegions - 1
	}
	for r := int(startRegion); r >= 0; r-- {
		region := &a.meta.regions[r]
		headAddr := &region.freeHeads[classIdx]
		if region.evacuate.Load() == 1 || uint32(atomic.LoadUint64(headAddr)) == 0 {
			continue
		}
		// Retain the lease until every removed block has restored its claims.
		region.enter()
		if region.evacuate.Load() == 1 {
			a.leaveRegion(uint32(r))
			continue
		}
		for {
			curr := atomic.LoadUint64(headAddr)
			tag, head := unpackTaggedPtr(curr)
			if head == 0 {
				break
			}
			last := head
			hdr := (*BlockHeader)(a.GetPtr(last - 8))
			next := atomic.LoadUint32(&hdr.NextFree)
			bytes := int32(hdr.Size)
			count = 1
			for count < max && next != 0 {
				last = next
				hdr = (*BlockHeader)(a.GetPtr(last - 8))
				next = atomic.LoadUint32(&hdr.NextFree)
				bytes += int32(hdr.Size)
				count++
			}
			if atomic.CompareAndSwapUint64(headAddr, curr, packTaggedPtr(tag+1, next)) {
				region.activeCount.Add(int32(count))
				region.liveBytes.Add(bytes)
				atomic.StoreUint32(&hdr.NextFree, 0)
				a.leaveRegion(uint32(r))
				return head, last, count
			}
		}
		a.leaveRegion(uint32(r))
	}
	return 0, 0, 0
}

func (a *Arena) Alloc(size uint32) uint32 {
	if size == 0 {
		return 0
	}
	allocSize := blockSize(size)
	if class, rounded := GetSizeClass(allocSize); class != -1 {
		if head, _, count := a.PopBatch(class, 1); count > 0 {
			return head
		}
		allocSize = rounded
	}
	if allocSize > TLABChunkSize/2 {
		return a.AllocDense(size)
	}
	t := a.acquireTLAB()
	defer a.releaseTLAB(t)
	if t.rem < allocSize || (t.offset != 0 && a.NeedsEvacuation(t.offset)) {
		a.refundTLAB(t)
		t.offset = a.reserve(TLABChunkSize, TLABChunkSize/64)
		t.rem = TLABChunkSize
		t.regionIdx = t.offset / RegionSize
		t.claims = TLABChunkSize / 64
	}
	off := t.offset
	hdr := (*BlockHeader)(a.GetPtr(off))
	hdr.Size = allocSize
	atomic.StoreUint32(&hdr.NextFree, 0)
	t.offset += allocSize
	t.rem -= allocSize
	t.claims--
	return off + 8
}

// AddActiveCount adjusts claims for a reservation already owned by the caller.
func (a *Arena) AddActiveCount(offset uint32, delta int32) {
	if offset == 0 || delta == 0 {
		return
	}
	idx := offset / RegionSize
	region := &a.meta.regions[idx]
	region.enter()
	region.activeCount.Add(delta)
	a.leaveRegion(idx)
}

// AllocBatch reserves one payload with count block claims. For raw storage
// split into independently freed blocks, use AllocChunk.
func (a *Arena) AllocBatch(size uint32, count int32) uint32 {
	total := blockSize(size)
	off := a.reserve(total, count)
	hdr := (*BlockHeader)(a.GetPtr(off))
	hdr.Size = total
	atomic.StoreUint32(&hdr.NextFree, 0)
	return off + 8
}

func (a *Arena) Free(offset uint32) {
	if offset == 0 {
		return
	}
	hdr := (*BlockHeader)(a.GetPtr(offset - 8))
	idx := (offset - 8) / RegionSize
	region := &a.meta.regions[idx]
	size := hdr.Size
	live := region.liveBytes.Add(-int32(size))
	start := uint64(idx) * RegionSize
	bump := uint64(atomic.LoadUint32(&a.meta.bumpOffset))
	if bump > start+(RegionSize*2)/5 && live > 0 && float64(live)/RegionSize < RelocationWatermark {
		region.evacuate.Store(1)
	}
	class, rounded := GetSizeClass(size)
	// Exact-sized blocks may only join a class if they physically fill it.
	if class != -1 && rounded == size && region.evacuate.Load() == 0 {
		headAddr := &region.freeHeads[class]
		for {
			curr := atomic.LoadUint64(headAddr)
			tag, head := unpackTaggedPtr(curr)
			atomic.StoreUint32(&hdr.NextFree, head)
			if atomic.CompareAndSwapUint64(headAddr, curr, packTaggedPtr(tag+1, offset)) {
				break
			}
		}
	}
	// Until this decrement, ownership of this block itself prevents purge.
	if region.activeCount.Add(-1) == 0 {
		a.purgeRegion(idx)
	}
}

// reserve publishes the bump and reservation counters under one region lease.
func (a *Arena) reserve(size uint32, count int32) uint32 {
	if size == 0 || size > RegionSize || count <= 0 {
		panic("utils: invalid arena reservation")
	}
	for {
		old := atomic.LoadUint32(&a.meta.bumpOffset)
		start := uint64(old)
		rem := uint64(RegionSize - old%RegionSize)
		if uint64(size) > rem {
			start += rem
		}
		end := start + uint64(size)
		if start >= uint64(a.size) || end > uint64(a.size) {
			panic("art: mmap arena OOM")
		}
		idx := uint32(start) / RegionSize
		region := &a.meta.regions[idx]
		region.enter()
		if !atomic.CompareAndSwapUint32(&a.meta.bumpOffset, old, uint32(end)) {
			a.leaveRegion(idx)
			continue
		}
		region.activeCount.Add(count)
		region.liveBytes.Add(int32(size))
		a.leaveRegion(idx)
		return uint32(start)
	}
}

func (a *Arena) purgeRegion(idx uint32) {
	region := &a.meta.regions[idx]
	if region.activeCount.Load() != 0 || !region.users.CompareAndSwap(0, -1) {
		return
	}
	defer region.users.Store(0)
	if region.activeCount.Load() != 0 {
		return
	}
	for i := range region.freeHeads {
		tag, _ := unpackTaggedPtr(atomic.LoadUint64(&region.freeHeads[i]))
		atomic.StoreUint64(&region.freeHeads[i], packTaggedPtr(tag+1, 0))
	}
	region.liveBytes.Store(0)
	region.evacuate.Store(0)
	start := uint64(idx) * RegionSize
	end := min(start+RegionSize, uint64(a.size))
	page := uint64(unix.Getpagesize())
	if idx == 0 {
		metaEnd := uint64(unsafe.Sizeof(arenaMeta{})) + 8
		start = (metaEnd + page - 1) &^ (page - 1)
	}
	end &^= page - 1
	if start >= end {
		return
	}
	ptr := unsafe.Add(a.basePtr, uintptr(start))
	_ = unix.Madvise(unsafe.Slice((*byte)(ptr), int(end-start)), unix.MADV_DONTNEED)
}

func packTaggedPtr(tag, offset uint32) uint64 {
	return (uint64(tag) << 32) | uint64(offset)
}

func unpackTaggedPtr(val uint64) (uint32, uint32) {
	return uint32(val >> 32), uint32(val)
}

//go:nosplit
func (a *Arena) BasePtr() unsafe.Pointer {
	return a.basePtr
}

//go:fix inline
func (a *Arena) GetPtr(offset uint32) unsafe.Pointer {
	if offset == 0 {
		return nil
	}
	return unsafe.Add(a.basePtr, uintptr(offset))
}

//go:nosplit
func (a *Arena) GetOffset(ptr unsafe.Pointer) uint32 {
	if ptr == nil {
		return 0
	}
	diff := uintptr(ptr) - uintptr(a.basePtr)
	if diff >= uintptr(a.size) {
		panic("art: pointer outside arena")
	}
	return uint32(diff)
}

//go:nosplit
func (a *Arena) Close() error {
	if a.basePtr == nil {
		return nil
	}

	b := unsafe.Slice((*byte)(a.basePtr), a.size)
	err := unix.Munmap(b)
	if err == nil {
		a.basePtr = nil
	}
	return err
}

// A block may not straddle regions: accounting and purge are region-local.
func blockSize(size uint32) uint32 {
	total := ((uint64(size) + 7) &^ uint64(7)) + 8
	if total > RegionSize {
		panic("utils: allocation exceeds one arena region")
	}
	return uint32(total)
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

func (a *Arena) leaveRegion(idx uint32) {
	r := &a.meta.regions[idx]
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
			continue // A cached alias is already held by a registry borrower.
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
	region := &a.meta.regions[t.regionIdx]
	region.enter()
	// liveBytes includes the unused tail until this explicit refund.
	region.liveBytes.Add(-int32(t.rem))
	region.activeCount.Add(-t.claims)
	t.offset, t.rem, t.claims = 0, 0, 0
	a.leaveRegion(t.regionIdx)
}
