// Arena provides a shared mmap allocator with configurable reclamation.
// Stored objects must not contain pointers into the Go heap. Allocator safety does
// not make access to payloads concurrent-safe, or protect readers after Free.
package utils

import (
	"errors"
	"math/bits"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	RegionSize     = 2 * 1024 * 1024
	TLABChunkSize  = 64 * 1024
	SizeClassCount = 18
	MaxArenaSize = 1<<32 - 1
	MaxRegions = (MaxArenaSize + RegionSize - 1) / RegionSize
	NumTLABs = 128
	RelocationWatermark = 0.40
	HeaderSize     = uint64(unsafe.Sizeof(BlockHeader{}))
	arenaMagic     = uint64(0x6164706172650001) // existing adaptive non-moving layout
	compactArenaMagic = uint64(0x6172656e61363402) // utils layout, version 2
	regionReusable = uint32(1)
	regionPurged   = uint32(2)
)

// Sizes include the 16-byte block header. The 8256 and 16448 classes hold
// an 8 KiB bitmap (including small container metadata) and a 16 KiB union buffer.
var sizeClasses = [SizeClassCount]uint64{
	64, 128, 272, 512, 1088, 1600, 2176, 4224, 8192,
	8256, 16448, 32832, 65600, 131136, 262208, 524352, 1048640, RegionSize,
}

// ArenaOptions selects reclamation policy on the shared allocator.
// NonMoving disables evacuation hints and defers page release until Trim.
// Its zero value preserves the original utils policy.
type ArenaOptions struct { NonMoving bool }

// Arena owns the mapping. Its small Go control objects stay GC-visible; payloads,
// region metadata, free-list heads and availability bitmaps live in the mapping.
// Close and opening an existing mapping require exclusive ownership. Concurrent
// mappings of the same file and crash recovery are deliberately unsupported.
type Arena struct {
	nonMoving bool
	basePtr      unsafe.Pointer
	size         uint64
	dataStart    uint64
	meta         *arenaMeta
	regions      []Region
	freeMasks    []uint64
	maskWords    int
	reserveMu    sync.Mutex // slow reservation/reclamation path only
	tlabPool     sync.Pool
	tlabHead     atomic.Pointer[localTLAB]
	regionReuses atomic.Uint64
	purgeCalls   atomic.Uint64
	purgedBytes  atomic.Uint64
	purgeErrors  atomic.Uint64
	ooms         atomic.Uint64
}

// Fixed-size, pointer-free on all supported architectures. All fields except
// bumpOffset are protected by reserveMu after construction. bumpOffset is a
// monotonic high-water mark, not the allocation cursor.
type arenaMeta struct {
	magic, mappingSize, bumpOffset, dataStart            uint64
	currentRegion, nextRegion, reusableHead, regionCount uint64
	_                                                    [64]byte
}

// users is a lease counter; -1 exclusively locks an empty region for reset or
// madvise. Each free head uses its low bit as a lock. Cursor writes are atomic;
// region selection, nextFree and flags are protected by reserveMu.
type Region struct {
	activeCount atomic.Int32
	liveBytes   atomic.Int32
	users       atomic.Int32
	evacuate    atomic.Uint32
	freeHeads   [SizeClassCount]uint64
	_ [SizeClassCount]uint32 // reserved: historical adaptive diagnostic counts
	cursor      uint64
	nextFree    uint64
	flags       uint32
	_           uint32
}

// BlockHeader precedes a payload. Size includes this header. NextFree is valid
// only on free blocks, or on an owned chain returned by PopBatch.
type BlockHeader struct {
	Size, NextFree uint64
}

type localTLAB struct {
	offset    uint64
	rem       uint32
	claims    int32
	regionIdx uint64
	inUse     atomic.Bool
	_         [4]byte
	next      *localTLAB // immutable after registration
	_         [88]byte
}

// NewArena maps a fixed-size arena with evacuation hints and automatic page release.
// The type parameter is retained for source compatibility.
func NewArena[T any](size uint64, fd int) (*Arena,error) { return NewArenaWithOptions(size,fd,ArenaOptions{}) }

// NewArenaWithOptions maps size bytes; fd == -1 creates an anonymous mapping. A file must already
// be at least size bytes and must be zero-filled or use this exact format. Files
// from the original utils layout are rejected. Reopening requires the same policy. A file may be reopened after an orderly Close;
// callers are responsible for retaining their root offsets separately.
func NewArenaWithOptions(size uint64, fd int, options ArenaOptions) (*Arena, error) {
	magic := compactArenaMagic
	if options.NonMoving { magic = arenaMagic }
	if size > uint64(int(^uint(0)>>1)) {
		return nil, errors.New("arena: mapping exceeds int")
	}
	count := (size + RegionSize - 1) / RegionSize
	words := (count + 63) / 64
	masksStart := uint64(unsafe.Sizeof(arenaMeta{})) + count*uint64(unsafe.Sizeof(Region{}))
	dataStart := (masksStart + SizeClassCount*words*8 + 7) &^ uint64(7)
	if size < dataStart+64 {
		return nil, errors.New("arena: mapping cannot hold metadata and a block")
	}
	flags := unix.MAP_PRIVATE | unix.MAP_ANON
	if fd != -1 {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return nil, err
		}
		if st.Size < 0 || uint64(st.Size) < size {
			return nil, errors.New("arena: backing file is too small")
		}
		flags = unix.MAP_SHARED
	}
	b, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ|unix.PROT_WRITE, flags)
	if err != nil {
		return nil, err
	}
	a := &Arena{nonMoving:options.NonMoving,basePtr: unsafe.Pointer(&b[0]), size: size, dataStart: dataStart, maskWords: int(words)}
	a.meta = (*arenaMeta)(a.basePtr)
	a.regions = unsafe.Slice((*Region)(unsafe.Add(a.basePtr, unsafe.Sizeof(arenaMeta{}))), int(count))
	a.freeMasks = unsafe.Slice((*uint64)(unsafe.Add(a.basePtr, masksStart)), int(words)*SizeClassCount)
	if a.meta.magic == 0 && a.meta.mappingSize == 0 && a.meta.bumpOffset == 0 {
		// Anonymous mappings are already zero-filled; preserve lazy metadata pages.
        // A new backing file must have fully zeroed metadata.
        if fd != -1 {
            for _,v := range b[:dataStart] {
                if v != 0 { _ = unix.Munmap(b); return nil, errors.New("arena: nonzero uninitialized metadata") }
            }
        }
		a.meta.magic = magic
		a.meta.mappingSize = size
		a.meta.bumpOffset = dataStart
		a.meta.dataStart = dataStart
		a.meta.regionCount = count
		a.meta.nextRegion = dataStart / RegionSize
	} else if a.meta.magic != magic || a.meta.mappingSize != size ||
		a.meta.dataStart != dataStart || a.meta.regionCount != count ||
		a.meta.bumpOffset < dataStart || a.meta.bumpOffset > size ||
		a.meta.nextRegion < dataStart/RegionSize || a.meta.nextRegion > count ||
		a.meta.currentRegion > count || a.meta.reusableHead > count {
		_ = unix.Munmap(b)
		return nil, errors.New("arena: incompatible or invalid mapping metadata")
	}
	return a, nil
}

// GetSizeClass retains the original nine small classes and stays inlineable.
// Its argument includes HeaderSize. NonMoving clients can use GetExtendedSizeClass.
func GetSizeClass(size uint64) (int, uint64) {
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

func largeSizeClass(size uint64) (int,uint64) {
    for i:=9;i<SizeClassCount;i++ { if size<=sizeClasses[i] { return i,sizeClasses[i] } }
    return -1,size
}
func GetExtendedSizeClass(size uint64) (int,uint64) {
    class,total:=GetSizeClass(size)
    if class<0 { return largeSizeClass(total) }
    return class,total
}
const blockHeaderSize64 = HeaderSize
func blockSize64(size uint64) uint64 { return blockSize(size) }

func blockSize(size uint64) uint64 {
	if size > RegionSize-HeaderSize {
		panic("arena: allocation exceeds one region")
	}
	return ((size + 7) &^ uint64(7)) + HeaderSize
}

// Alloc returns an eight-byte-aligned payload offset, or zero for size == 0.
// Reused memory is dirty. A request may be rounded up to a size class.
func (a *Arena) Alloc(size uint64) uint64 {
	if size == 0 {
		return 0
	}
	class, total := GetSizeClass(blockSize(size))
	if class<0 && a.nonMoving { class,total=largeSizeClass(total) }
	if head, _, n := a.PopBatch(class, 1); n != 0 {
		return head
	}
	if total > TLABChunkSize/2 {
		return a.allocDirect(total)
	}
	t := a.acquireTLAB()
	defer a.releaseTLAB(t)
	if uint64(t.rem) < total || (t.offset!=0 && a.NeedsEvacuation(t.offset)) {
		a.refundTLAB(t)
		off := a.tryReserve(TLABChunkSize, TLABChunkSize/64)
		if off == 0 {
			a.drainIdleTLABs()
			off = a.tryReserve(TLABChunkSize, TLABChunkSize/64)
		}
		// A partial region may fit this block without fitting a whole TLAB.
		if off == 0 {
			return a.allocDirect(total)
		}
		t.offset = off
		t.rem, t.claims = TLABChunkSize, TLABChunkSize/64
		t.regionIdx = t.offset / RegionSize
	}
	off := t.offset
	h := (*BlockHeader)(a.ptr(off))
	h.Size, h.NextFree = total, 0
	t.offset += total
	t.rem -= uint32(total)
	t.claims--
	return off + HeaderSize
}

// AllocZeroed clears the requested payload bytes, including on a free-list hit.
func (a *Arena) AllocZeroed(size uint64) uint64 {
	off := a.Alloc(size)
	if off != 0 {
		clear(unsafe.Slice((*byte)(a.ptr(off)), int(size)))
	}
	return off
}

// AllocDense bypasses the TLAB but still reuses blocks and rounds to a class.
func (a *Arena) AllocDense(size uint64) uint64 {
	if size == 0 {
		return 0
	}
	class, total := GetSizeClass(blockSize(size))
	if class<0 && a.nonMoving { class,total=largeSizeClass(total) }
	if head, _, n := a.PopBatch(class, 1); n != 0 {
		return head
	}
	return a.allocDirect(total)
}

func (a *Arena) allocDirect(total uint64) uint64 {
	off := a.reserve(total, 1)
	h := (*BlockHeader)(a.ptr(off))
	h.Size, h.NextFree = total, 0
	return off + HeaderSize
}

// AllocBatch reserves one exact-sized payload with count claims. The caller must
// return count-1 extra claims with AddActiveCount before the single final Free.
// To carve independent blocks with their own headers, use AllocChunk instead.
func (a *Arena) AllocBatch(size uint64, count int32) uint64 {
	total := blockSize(size)
	off := a.reserve(total, count)
	h := (*BlockHeader)(a.ptr(off))
	h.Size, h.NextFree = total, 0
	return off + HeaderSize
}

// AllocChunk reserves raw bytes, including headers the caller must initialize.
// Blocks must cover the charged bytes exactly; count is their total claim count.
func (a *Arena) AllocChunk(size uint64, count int32) uint64 {
	if size == 0 || size > RegionSize {
		panic("arena: invalid chunk size")
	}
	return a.reserve((size+7)&^uint64(7), count)
}

// AddActiveCount changes only claims, not charged bytes, for an owned reservation.
// It must not surrender a claim still used by a reader or an unfinished allocator.
func (a *Arena) AddActiveCount(offset uint64, delta int32) {
	if offset == 0 || delta == 0 {
		return
	}
	_ = a.ptr(offset)
	idx := offset / RegionSize
	r := &a.regions[idx]
	r.enter()
	n := r.activeCount.Add(delta)
	a.leaveRegion(idx)
	if n < 0 {
		panic("arena: negative claim count")
	}
}

// PopBatch returns an owned chain from one region, with the last NextFree == 0.
// An availability bitmap avoids walking every region on each allocation miss.
func (a *Arena) PopBatch(class, max int) (head, tail uint64, count int) {
	if class < 0 || class >= SizeClassCount || max <= 0 {
		return
	}
	// The bitmap is a conservative index: keep a bit set when a pop empties a
	// list, and clear it on the next miss under the head lock. Hot alloc/free
	// pairs then avoid two atomic bitmap writes and a global counter per pair.
	highest := (atomic.LoadUint64(&a.meta.bumpOffset)-1)/RegionSize
    words := int(highest/64)+1
    idx := highest
    w,mask := 0,uint64(0)
    first := true
    for {
        r:=&a.regions[idx]
        // Resolve the newest region directly; NonMoving misses use the bitmap.
        if (!first || atomic.LoadUint64(&r.freeHeads[class])!=0) && r.evacuate.Load()==0 {
            r.enter()
            if r.evacuate.Load()==0 {
                head=lockHead64(&r.freeHeads[class])
                if head!=0 {
                    tail=head
                    h:=(*BlockHeader)(a.ptr(tail-HeaderSize))
                    next,charged:=h.NextFree,int32(h.Size)
                    count=1
                    for count<max && next!=0 {
                        tail=next
                        h=(*BlockHeader)(a.ptr(tail-HeaderSize))
                        next=h.NextFree
                        charged+=int32(h.Size)
                        count++
                    }
                    r.activeCount.Add(int32(count))
                    r.liveBytes.Add(charged)
                    h.NextFree=0
                    unlockHead64(&r.freeHeads[class],next)
                    a.leaveRegion(idx)
                    return
                }
                if a.nonMoving { a.clearAvailable(class,idx) }
                unlockHead64(&r.freeHeads[class],0)
            }
            a.leaveRegion(idx)
        }
        if !a.nonMoving {
            if idx==0 { return 0,0,0 }
            idx--
            continue
        }
        first=false
        for mask==0 {
            if w==words { return 0,0,0 }
            mask=atomic.LoadUint64(&a.freeMasks[class*a.maskWords+w])
            w++
        }
        bit:=bits.TrailingZeros64(mask)
        mask&=mask-1
        idx=uint64((w-1)*64+bit)
    }
}

// Free releases an owned block. No reader may retain its payload. An empty region
// becomes reusable immediately. NonMoving defers physical page release to Trim.
func (a *Arena) Free(offset uint64) {
	if offset == 0 {
		return
	}
	h:=(*BlockHeader)(a.ptr(offset-HeaderSize))
    idx:=(offset-HeaderSize)/RegionSize
    r:=&a.regions[idx]
    size:=h.Size
    live:=r.liveBytes.Add(-int32(size))
    if !a.nonMoving {
        start:=idx*RegionSize
        cursor:=atomic.LoadUint64(&r.cursor)
        if cursor>start+(RegionSize*2)/5 && live>0 && float64(live)/RegionSize<RelocationWatermark {
            r.evacuate.Store(1)
        }
    }
    class,rounded:=GetSizeClass(size)
    if class<0 && a.nonMoving { class,rounded=largeSizeClass(rounded) }
    if class!=-1 && rounded==size && r.evacuate.Load()==0 {
        head:=lockHead64(&r.freeHeads[class])
        h.NextFree=head
        if a.nonMoving { a.setAvailable(class,idx) }
        unlockHead64(&r.freeHeads[class],offset)
    }
	// This block's claim protects its header until its free-list update is done.
	if r.activeCount.Add(-1) == 0 {
		a.recycleRegion(idx)
	}
}

func (a *Arena) setAvailable(class int, idx uint64) {
	addr := &a.freeMasks[class*a.maskWords+int(idx/64)]
	bit := uint64(1) << (idx % 64)
	if atomic.LoadUint64(addr)&bit == 0 {
		atomic.OrUint64(addr, bit)
	}
}
func (a *Arena) clearAvailable(class int,idx uint64) {
    addr:=&a.freeMasks[class*a.maskWords+int(idx/64)]
    bit:=uint64(1)<<(idx%64)
    if atomic.LoadUint64(addr)&bit!=0 { atomic.AndUint64(addr,^bit) }
}
func (a *Arena) regionStart(idx uint64) uint64 { return max(idx*RegionSize, a.dataStart) }
func (a *Arena) regionEnd(idx uint64) uint64   { return min((idx+1)*RegionSize, a.size) &^ uint64(7) }

func (a *Arena) reserve(size uint64, count int32) uint64 {
	if size == 0 || size > RegionSize || count <= 0 {
		panic("arena: invalid reservation")
	}
	if off := a.tryReserve(size, count); off != 0 {
		return off
	}
	// Lost sync.Pool entries are still registered in tlabHead. Reclaim idle
	// reservations before reporting OOM; do not invalidate an in-use TLAB.
	a.drainIdleTLABs()
	if off := a.tryReserve(size, count); off != 0 {
		return off
	}
	a.ooms.Add(1)
	panic("arena: mmap arena OOM")
}

func (a *Arena) tryReserve(size uint64, count int32) uint64 {
	a.reserveMu.Lock()
	defer a.reserveMu.Unlock()
	if a.meta == nil {
		panic("arena: closed")
	}
	for {
		if ref := a.meta.currentRegion; ref != 0 {
			idx := ref - 1
			r := &a.regions[idx]
			if r.cursor+size <= a.regionEnd(idx) {
				r.enter()
				off := r.cursor
				atomic.StoreUint64(&r.cursor,r.cursor+size)
				r.activeCount.Add(count)
				r.liveBytes.Add(int32(size))
				if r.cursor > atomic.LoadUint64(&a.meta.bumpOffset) {
					atomic.StoreUint64(&a.meta.bumpOffset, r.cursor)
				}
				// A positive claim count prevents a reset; no recursive reserveMu.
				r.users.Add(-1)
				return off
			}
		}
		// Empty regions are intrusive-list entries, not Go heap allocations.
		prev := uint64(0)
		selected := false
		for ref := a.meta.reusableHead; ref != 0; {
			idx := ref - 1
			r := &a.regions[idx]
			if a.regionEnd(idx)-a.regionStart(idx) >= size {
				if prev == 0 {
					a.meta.reusableHead = r.nextFree
				} else {
					a.regions[prev-1].nextFree = r.nextFree
				}
				r.nextFree, r.flags = 0, 0
				a.meta.currentRegion = ref
				a.regionReuses.Add(1)
				selected = true
				break
			}
			prev, ref = ref, r.nextFree
		}
		if selected {
			continue
		}
		if a.meta.nextRegion == uint64(len(a.regions)) {
			return 0
		}
		idx := a.meta.nextRegion
		a.meta.nextRegion++
		r := &a.regions[idx]
		atomic.StoreUint64(&r.cursor,a.regionStart(idx))
		if a.regionEnd(idx)-r.cursor < size {
			r.flags, r.nextFree = regionReusable, a.meta.reusableHead
			a.meta.reusableHead = idx + 1
			continue
		}
		a.meta.currentRegion = idx + 1
	}
}

func (a *Arena) recycleRegion(idx uint64) {
	r := &a.regions[idx]
	if r.activeCount.Load() != 0 {
		return
	}
	a.reserveMu.Lock()
	defer a.reserveMu.Unlock()
	if r.flags&regionReusable != 0 || r.activeCount.Load() != 0 || !r.users.CompareAndSwap(0, -1) {
		return
	}
	defer r.users.Store(0)
	if r.activeCount.Load() != 0 {
		return
	}
	for class := range r.freeHeads {
		if atomic.LoadUint64(&r.freeHeads[class])!=0 { atomic.StoreUint64(&r.freeHeads[class],0) }
		if a.nonMoving { a.clearAvailable(class, idx) }
	}
	r.liveBytes.Store(0)
	r.evacuate.Store(0)
	atomic.StoreUint64(&r.cursor,a.regionStart(idx))
	if a.meta.currentRegion == idx+1 {
		a.meta.currentRegion = 0
	}
	r.flags, r.nextFree = regionReusable, a.meta.reusableHead
	a.meta.reusableHead = idx + 1
    if !a.nonMoving { _=a.purgePages(idx,r) }
}

func (a *Arena) purgeRegion(idx uint64) { a.recycleRegion(idx) }

func (a *Arena) purgePages(idx uint64,r *Region) error {
    page:=uint64(unix.Getpagesize())
    start:=(a.regionStart(idx)+page-1)&^(page-1)
    end:=a.regionEnd(idx)&^(page-1)
    if start<end {
        a.purgeCalls.Add(1)
        if err:=unix.Madvise(unsafe.Slice((*byte)(a.ptr(start)),int(end-start)),unix.MADV_DONTNEED);err!=nil {
            a.purgeErrors.Add(1)
            return err
        }
        a.purgedBytes.Add(end-start)
    }
    r.flags|=regionPurged
    return nil
}

// Trim refunds idle TLAB reservations and releases full pages of empty regions.
// It may run alongside Alloc/Free, but busy TLABs are skipped. No live object is
// moved. Run after a quiescent workload for a complete trim. A failed madvise
// leaves the region reusable and will be retried on a later Trim.
func (a *Arena) Trim() error {
	a.drainIdleTLABs()
	a.reserveMu.Lock()
	defer a.reserveMu.Unlock()
	if a.meta == nil {
		return errors.New("arena: closed")
	}
	var firstErr error
	for ref:=a.meta.reusableHead;ref!=0; {
        r:=&a.regions[ref-1]
        idx:=ref-1
        ref=r.nextFree
        if r.flags&regionPurged!=0 || !r.users.CompareAndSwap(0,-1) { continue }
        if err:=a.purgePages(idx,r);err!=nil && firstErr==nil { firstErr=err }
        r.users.Store(0)
    }
	return firstErr
}

// NeedsEvacuation is a relocation hint; the allocator never moves live objects.
// NonMoving arenas never set this hint.
func (a *Arena) NeedsEvacuation(offset uint64) bool {
    if offset==0 { return false }
    return a.regions[offset/RegionSize].evacuate.Load()!=0
}

func lockHead64(addr *uint64) uint64 {
	for spins := 0; ; spins++ {
		old := atomic.LoadUint64(addr)
		if old&1 == 0 && atomic.CompareAndSwapUint64(addr, old, old|1) {
			return old
		}
		if spins == 16 {
			runtime.Gosched()
			spins = 0
		}
	}
}
func unlockHead64(addr *uint64, off uint64) { atomic.StoreUint64(addr, off) }
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
		a.recycleRegion(idx)
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
func (a *Arena) releaseTLAB(t *localTLAB) { t.inUse.Store(false); a.tlabPool.Put(t) }
func (a *Arena) refundTLAB(t *localTLAB) {
	if t.offset == 0 {
		return
	}
	idx := t.regionIdx
	r := &a.regions[idx]
	r.enter()
	r.liveBytes.Add(-int32(t.rem))
	r.activeCount.Add(-t.claims)
	t.offset, t.rem, t.claims = 0, 0, 0
	a.leaveRegion(idx)
}
func (a *Arena) drainIdleTLABs() {
	for t := a.tlabHead.Load(); t != nil; t = t.next {
		if t.inUse.CompareAndSwap(false, true) {
			a.refundTLAB(t)
			t.inUse.Store(false)
		}
	}
}

//go:nosplit
func (a *Arena) BasePtr() unsafe.Pointer { return a.basePtr }
func (a *Arena) ptr(offset uint64) unsafe.Pointer { return unsafe.Add(a.basePtr,uintptr(offset)) }
// GetPtr resolves an owned offset without bounds or provenance checks.
// CheckedPtr validates mapping bounds; PayloadCapacity also validates its header.
func (a *Arena) GetPtr(offset uint64) unsafe.Pointer {
    if offset==0 { return nil }
    return unsafe.Add(a.basePtr,uintptr(offset))
}
func (a *Arena) CheckedPtr(offset uint64) unsafe.Pointer {
    if offset==0 { return nil }
    if a.basePtr==nil || offset>=a.size { panic("arena: offset outside live mapping") }
    return a.ptr(offset)
}
//go:nosplit
func (a *Arena) GetOffset(ptr unsafe.Pointer) uint64 {
	if ptr == nil {
		return 0
	}
	diff := uintptr(ptr) - uintptr(a.basePtr)
	if a.basePtr == nil || uint64(diff) >= a.size {
		panic("arena: pointer outside live mapping")
	}
	return uint64(diff)
}

// Close requires all callers, including payload readers, to be finished. It
// refunds idle reservations so orderly file reopening does not strand claims.
// It is idempotent after successful unmapping.
func (a *Arena) Close() error {
	if a.basePtr == nil {
		return nil
	}
	a.drainIdleTLABs()
	for t := a.tlabHead.Load(); t != nil; t = t.next {
		if t.inUse.Load() {
			return errors.New("arena: Close with an active allocator")
		}
	}
	err := unix.Munmap(unsafe.Slice((*byte)(a.basePtr), int(a.size)))
	if err == nil {
		a.basePtr, a.meta, a.regions, a.freeMasks = nil, nil, nil, nil
	}
	return err
}
