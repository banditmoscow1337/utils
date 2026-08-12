package utils

import (
	"errors"
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
	NumTLABs = 128
)

type Region struct {
	activeCount atomic.Int32  // 4 bytes
	liveBytes   atomic.Int32  // 4 bytes
	evacuate    atomic.Uint32 // 4 bytes
	_           uint32        // 4 bytes
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
}

type localTLAB struct {
	offset    uint32
	rem       uint32
	claims    int32
	liveDelta int32
	regionIdx uint32
	_         [CacheLineSize - 20]byte 
}

func NewArena[T any](size uint32, fd int) (*Arena, error) {
	if size == 0 || size > MaxArenaSize {
		return nil, errors.New("invalid arena size")
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
		arena.meta.bumpOffset = uint32(unsafe.Sizeof(arenaMeta{})) + 8
	}

	// Standard heap allocation. sync.Pool will manage caching.
	arena.tlabPool.New = func() any {
		return &localTLAB{}
	}

	return arena, nil
}

func GetSizeClass(size uint32) (int, uint32) {
	switch {
	case size <= 64: return 0, 64
	case size <= 128: return 1, 128
	case size <= 272: return 2, 272
	case size <= 512: return 3, 512
	case size <= 1088: return 4, 1088
	case size <= 1600: return 5, 1600
	case size <= 2176: return 6, 2176
	case size <= 4224: return 7, 4224
	case size <= 8192: return 8, 8192
	default: return -1, size
	}
}

func (a *Arena) NeedsEvacuation(offset uint32) bool {
	if offset == 0 {
		return false
	}
	return a.meta.regions[offset/RegionSize].evacuate.Load() == 1
}

func (a *Arena) AllocDense(size uint32) uint32 {
	return a.AllocBatch(size, 1)
}

func (a *Arena) AllocChunk(size uint32, count int32) uint32 {
	return a.AllocBatch(size, count) - 8
}

func (a *Arena) PopBatch(classIdx int, max int) (headOff, tailOff uint32, count int) {
	startRegion := atomic.LoadUint32(&a.meta.bumpOffset) / RegionSize
	
	for r := int(startRegion); r >= 0; r-- {
		if a.meta.regions[r].evacuate.Load() == 1 {
			continue
		}

		headAddr := &a.meta.regions[r].freeHeads[classIdx]
		for {
			curr := atomic.LoadUint64(headAddr)
			tag, head := unpackTaggedPtr(curr)
			if head == 0 {
				break
			}

			currNode := head
			hdr := (*blockHeader)(a.GetPtr(currNode - 8))
			nextOff := hdr.nextFree
			count = 1

			for count < max && nextOff != 0 {
				currNode = nextOff
				hdr = (*blockHeader)(a.GetPtr(currNode - 8))
				nextOff = hdr.nextFree
				count++
			}

			newVal := packTaggedPtr(tag+1, nextOff)
			if atomic.CompareAndSwapUint64(headAddr, curr, newVal) {
				tailHdr := (*blockHeader)(a.GetPtr(currNode - 8))
				tailHdr.nextFree = 0
				return head, currNode, count
			}
		}
	}
	return 0, 0, 0
}

func (a *Arena) Alloc(size uint32) uint32 {
	if size == 0 {
		return 0
	}
	align := (size + 7) &^ 7
	totalSize := align + 8

	classIdx, classSize := GetSizeClass(totalSize)
	allocSize := totalSize
	if classIdx != -1 {
		head, _, count := a.PopBatch(classIdx, 1)
		if count > 0 {
			return head
		}
		allocSize = classSize
	}

	// TLAB Fast-Path for standard objects
	if allocSize <= TLABChunkSize/2 {
		t := a.tlabPool.Get().(*localTLAB)
		
		if t.rem < allocSize {
			// Refund unused claims back to the region
			if t.claims > 0 {
				if a.meta.regions[t.regionIdx].activeCount.Add(-t.claims) == 0 {
					a.purgeRegion(t.regionIdx)
				}
			}
			if t.liveDelta > 0 {
				a.meta.regions[t.regionIdx].liveBytes.Add(t.liveDelta)
			}
			
			t.offset = a.allocRaw(TLABChunkSize)
			t.rem = TLABChunkSize
			t.regionIdx = t.offset / RegionSize
			
			// Pre-claim maximum theoretical objects for this chunk.
			// The smallest alloc is 64 bytes, so TLABChunkSize/64 is the strict max.
			t.claims = TLABChunkSize / 64
			a.meta.regions[t.regionIdx].activeCount.Add(t.claims)
			t.liveDelta = 0
		}

		hdrOff := t.offset
		hdr := (*BlockHeader)(a.GetPtr(hdrOff))
		hdr.Size = allocSize
		hdr.NextFree = 0
		
		resOff := hdrOff + 8
		
		t.offset += allocSize
		t.rem -= allocSize
		t.claims-- // Consume a pre-claimed token locally
		t.liveDelta += int32(allocSize)

		a.tlabPool.Put(t)
		return resOff
	}

	if classIdx != -1 {
		return a.AllocDense(classSize - 8)
	}
	return a.AllocDense(size)
}

func (a *Arena) AddActiveCount(offset uint32, delta int32) {
	if offset == 0 {
		return
	}
	regionIdx := offset / RegionSize
	a.meta.regions[regionIdx].activeCount.Add(delta)
}

func (a *Arena) AllocBatch(size uint32, count int32) uint32 {
	align := (size + 7) &^ 7
	totalSize := align + 8
	for {
		oldOffset := atomic.LoadUint32(&a.meta.bumpOffset)
		rem := RegionSize - (oldOffset % RegionSize)
		startOffset := uint64(oldOffset)
		if totalSize > rem {
			startOffset += uint64(rem)
		}
		newOffset := startOffset + uint64(totalSize)

		if newOffset > uint64(a.size) || startOffset >= uint64(a.size) {
			panic("art: mmap arena OOM")
		}
		if atomic.CompareAndSwapUint32(&a.meta.bumpOffset, oldOffset, uint32(newOffset)) {
			regionIdx := uint32(startOffset) / RegionSize
			a.meta.regions[regionIdx].activeCount.Add(count)
			a.meta.regions[regionIdx].liveBytes.Add(int32(totalSize))
			hdr := (*BlockHeader)(a.GetPtr(uint32(startOffset)))
			hdr.Size = totalSize
			hdr.NextFree = 0
			return uint32(startOffset) + 8
		}
	}
}

func (a *Arena) Free(offset uint32) {
	if offset == 0 {
		return
	}
	headerOffset := offset - 8
	hdr := (*BlockHeader)(a.GetPtr(headerOffset))
	regionIdx := headerOffset / RegionSize
	region := &a.meta.regions[regionIdx]
	currentLive := region.liveBytes.Add(-int32(hdr.Size))

	regionStart := regionIdx * RegionSize
	bumpOff := atomic.LoadUint32(&a.meta.bumpOffset)
	if bumpOff > regionStart+((RegionSize*2)/5) &&
		currentLive > 0 && (float64(currentLive)/float64(RegionSize)) < RelocationWatermark {
		region.evacuate.Store(1)
	}

	classIdx, _ := GetSizeClass(hdr.Size)
	if classIdx != -1 && region.evacuate.Load() == 0 {
		headAddr := &region.freeHeads[classIdx]
		for {
			curr := atomic.LoadUint64(headAddr)
			tag, head := unpackTaggedPtr(curr)
			hdr.NextFree = head
			newVal := packTaggedPtr(tag+1, offset)
			if atomic.CompareAndSwapUint64(headAddr, curr, newVal) {
				break
			}
		}
	}

	if region.activeCount.Add(-1) == 0 {
		a.purgeRegion(regionIdx)
	}
}

// allocRaw claims a contiguous block from the global offset without modifying 
// region metadata or writing batch headers.
func (a *Arena) allocRaw(size uint32) uint32 {
	align := (size + 7) &^ 7
	for {
		oldOffset := atomic.LoadUint32(&a.meta.bumpOffset)
		rem := RegionSize - (oldOffset % RegionSize)
		startOffset := uint64(oldOffset)
		
		if align > rem {
			startOffset += uint64(rem)
		}
		
		newOffset := startOffset + uint64(align)
		if newOffset > uint64(a.size) || startOffset >= uint64(a.size) {
			panic("art: mmap arena OOM")
		}
		
		if atomic.CompareAndSwapUint32(&a.meta.bumpOffset, oldOffset, uint32(newOffset)) {
			return uint32(startOffset)
		}
	}
}

func (a *Arena) purgeRegion(idx uint32) {
	region := &a.meta.regions[idx]
	region.evacuate.Store(0)
	for i := 0; i < 10; i++ {
		atomic.StoreUint64(&region.freeHeads[i], 0)
	}

    startOff := idx * RegionSize
    length := uint32(RegionSize)

    if idx == 0 {
        metaSize := uint32(unsafe.Sizeof(arenaMeta{})) + 8
        const pageAlign = 65536
        alignedMeta := (metaSize + pageAlign - 1) &^ (pageAlign - 1)
        if alignedMeta >= length {
            return
        }
        startOff = alignedMeta
        length -= alignedMeta
    }

    ptr := unsafe.Pointer(uintptr(a.basePtr) + uintptr(startOff))
	unix.Madvise(unsafe.Slice((*byte)(ptr), length), unix.MADV_DONTNEED)
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
	return unsafe.Pointer(uintptr(a.basePtr) + uintptr(offset))
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
	a.basePtr = nil
	return err
}