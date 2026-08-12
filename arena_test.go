package utils

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
)

// TestArena_BasicAllocAndFree validates fundamental mapping behavior, bounds checks,
// and pointer arithmetic.
func TestArena_BasicAllocAndFree(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	if arena.BasePtr() == nil {
		t.Fatal("expected non-nil base pointer")
	}

	// Test allocation
	off1 := arena.Alloc(64)
	if off1 == 0 {
		t.Fatal("expected non-zero offset")
	}

	ptr1 := arena.GetPtr(off1)
	if ptr1 == nil {
		t.Fatal("expected non-nil pointer from valid offset")
	}

	// Verify reverse pointer resolution
	if arena.GetOffset(ptr1) != off1 {
		t.Fatalf("offset mismatch: expected %d, got %d", off1, arena.GetOffset(ptr1))
	}

	// Null checks
	if arena.GetPtr(0) != nil {
		t.Fatal("GetPtr(0) should return nil")
	}
	if arena.GetOffset(nil) != 0 {
		t.Fatal("GetOffset(nil) should return 0")
	}

	arena.Free(off1)
}

// TestArena_GetSizeClass ensures allocations are strictly rounded to internal slabs.
func TestArena_GetSizeClass(t *testing.T) {
	tests := []struct {
		size        uint32
		expectedIdx int
		expectedSz  uint32
	}{
		{0, 0, 64},
		{32, 0, 64},
		{64, 0, 64},
		{65, 1, 128},
		{200, 2, 272},
		{500, 3, 512},
		{1000, 4, 1088},
		{1500, 5, 1600},
		{2000, 6, 2176},
		{4000, 7, 4224},
		{8000, 8, 8192},
		{8500, -1, 8500},
	}

	for _, tc := range tests {
		idx, sz := GetSizeClass(tc.size)
		if idx != tc.expectedIdx || sz != tc.expectedSz {
			t.Errorf("GetSizeClass(%d) = (%d, %d), expected (%d, %d)", 
				tc.size, idx, sz, tc.expectedIdx, tc.expectedSz)
		}
	}
}

// TestArena_PointerTagging verifies the ABA prevention mechanism for the lock-free free lists.
func TestArena_PointerTagging(t *testing.T) {
	tag := uint32(42)
	offset := uint32(1024)
	packed := packTaggedPtr(tag, offset)
	unpackedTag, unpackedOff := unpackTaggedPtr(packed)

	if unpackedTag != tag || unpackedOff != offset {
		t.Fatalf("Tagging mismatch: expected (%d, %d), got (%d, %d)", tag, offset, unpackedTag, unpackedOff)
	}
}

// TestArena_FreeListReuse ensures that objects freed into size-class lists are correctly 
// popped back out without bumping the global allocation offset.
func TestArena_FreeListReuse(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	// Sentinel allocation: keeps activeCount > 0, preventing purgeRegion 
	// from destroying the free list when the batch is freed.
	_ = arena.Alloc(8)

	var offsets []uint32
	for i := 0; i < 10; i++ {
		offsets = append(offsets, arena.Alloc(64)) // Note: actually fits class 1 (128 bytes) due to 8-byte header
	}

	bumpOff := atomic.LoadUint32(&arena.meta.bumpOffset)

	for _, off := range offsets {
		arena.Free(off)
	}

	var reOffsets []uint32
	for i := 0; i < 10; i++ {
		reOffsets = append(reOffsets, arena.Alloc(64))
	}

	if atomic.LoadUint32(&arena.meta.bumpOffset) != bumpOff {
		t.Fatal("bump offset moved; arena failed to reuse lock-free size class lists")
	}

	// Verify we got the exact same offsets back
	offMap := make(map[uint32]bool)
	for _, off := range offsets {
		offMap[off] = true
	}
	for _, off := range reOffsets {
		if !offMap[off] {
			t.Fatalf("reallocated offset %d was not in the original freed set", off)
		}
	}
}

// TestArena_EvacuationAndPurge validates the compaction heuristics and MADV_DONTNEED syscalls.
func TestArena_EvacuationAndPurge(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	// RegionSize is 2MB. We need to allocate roughly 1.5MB to stay in the same region, 
	// then drop the live bytes below the 40% RelocationWatermark.
	allocSize := uint32(1550) // Pushed to 1600 bytes internally due to headers + alignment
	allocCount := (1500 * 1024) / 1600 

	var offsets []uint32
	for i := 0; i < allocCount; i++ {
		offsets = append(offsets, arena.Alloc(allocSize))
	}

	// Free enough to breach the watermark threshold (< 800KB remaining live bytes)
	freeCount := (1000 * 1024) / 1600
	for i := 0; i < freeCount; i++ {
		arena.Free(offsets[i])
	}

	if !arena.NeedsEvacuation(offsets[freeCount]) {
		t.Fatal("expected region to be marked for evacuation after liveBytes dropped below RelocationWatermark")
	}

	// Free the remainder to trigger activeCount == 0, forcing purgeRegion execution.
	for i := freeCount; i < allocCount; i++ {
		arena.Free(offsets[i])
	}
	// If purgeRegion crashes during madvise, the test suite will fail.
}

// TestArena_ConcurrentAllocFree blasts the arena with parallel access to verify 
// atomics and lock-free lists don't corrupt under heavy contention.
func TestArena_ConcurrentAllocFree(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	var wg sync.WaitGroup
	numWorkers := 16
	allocsPerWorker := 5000

	wg.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		go func() {
			defer wg.Done()
			var localOffsets []uint32
			
			// Allocation phase
			for j := 0; j < allocsPerWorker; j++ {
				// Mix between size classes and direct large-alloc chunks
				sz := uint32(rand.Intn(2048) + 16)
				localOffsets = append(localOffsets, arena.Alloc(sz))
			}
			
			// Reclaim phase
			for _, off := range localOffsets {
				arena.Free(off)
			}
		}()
	}
	wg.Wait()
}

// TestArena_OutOfBoundsPanic verifies strict boundary defense in ptr resolution.
func TestArena_OutOfBoundsPanic(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected GetOffset to panic when resolving a pointer outside the arena mapping")
		}
	}()

	invalidPtr := unsafe.Pointer(uintptr(arena.BasePtr()) + uintptr(MaxArenaSize+4096))
	arena.GetOffset(invalidPtr)
}

// TestArena_ZeroSize validates fast paths for zero-size allocations.
func TestArena_ZeroSize(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	if off := arena.Alloc(0); off != 0 {
		t.Fatalf("Alloc(0) returned %d, expected 0", off)
	}
	
	// Free(0) should safely return without panicking or altering state
	arena.Free(0)
}

// TestArena_AllocBatch ensures batch allocations correctly bump counts and offsets.
func TestArena_AllocBatch(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	batchSize := uint32(64)
	count := int32(10)
	off := arena.AllocBatch(batchSize, count)

	hdr := (*BlockHeader)(arena.GetPtr(off - 8))
	expectedSize := ((batchSize + 7) &^ 7) + 8
	if hdr.Size != expectedSize {
		t.Errorf("expected batch header size %d, got %d", expectedSize, hdr.Size)
	}

	regionIdx := off / RegionSize
	active := arena.meta.regions[regionIdx].activeCount.Load()
	if active != count {
		t.Errorf("expected active count %d, got %d", count, active)
	}
}

// TestArena_OOMPanic verifies the allocator panics instead of overflowing the mmap region.
func TestArena_OOMPanic(t *testing.T) {
	// Allocate a restricted arena size to easily trigger OOM
	arena, err := NewArena[int](RegionSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected OOM panic on exceeding arena boundaries")
		}
	}()

	// Force an allocation larger than the physical arena
	arena.Alloc(RegionSize + 1024)
}

// TestArena_PurgeRegionZero ensures purging region 0 protects the arenaMeta header.
func TestArena_PurgeRegionZero(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	// Manually trigger a purge on region 0
	arena.purgeRegion(0)

	// If metadata was unmapped via madvise, accessing bumpOffset would likely fault 
	// or return 0 on the next read depending on OS behavior.
	bump := atomic.LoadUint32(&arena.meta.bumpOffset)
	if bump == 0 {
		t.Fatal("FATAL: metadata corrupted or zeroed after purgeRegion(0)")
	}
}

// TestArena_PopBatchSkipsEvacuated verifies lock-free lists are ignored if a region is evacuating.
func TestArena_PopBatchSkipsEvacuated(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	off := arena.Alloc(64)
	arena.Free(off) // Pushes to the freeHeads array

	// Force the evacuation flag to mimic a compaction event
	regionIdx := off / RegionSize
	arena.meta.regions[regionIdx].evacuate.Store(1)

	classIdx, _ := GetSizeClass(64 + 8) 
	head, _, count := arena.PopBatch(classIdx, 1)

	if count != 0 || head != 0 {
		t.Fatalf("PopBatch failed to respect evacuation flag; got count=%d, head=%d", count, head)
	}
}

// TestArena_CloseIdempotency ensures closing multiple times doesn't double-munmap.
func TestArena_CloseIdempotency(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	
	if err := arena.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := arena.Close(); err != nil {
		t.Fatalf("subsequent Close failed: %v", err)
	}
}

// ==============================================================================
// BENCHMARKS
// ==============================================================================

// BenchmarkArena_Alloc_Sequential measures single-thread raw bumping throughput.
func BenchmarkArena_Alloc_Sequential(b *testing.B) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		b.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	b.ResetTimer()
	b.ReportAllocs()
	
	// Reset loop to prevent hitting memory limits during prolonged `-benchtime`
	for b.Loop() {
		off := arena.Alloc(128)
		arena.Free(off) // Immediately free so we test the pop/push loop
	}
}

// BenchmarkArena_AllocFree_Parallel hammers the free-list PopBatch/Push logic to 
// observe contention scaling on size classes.
func BenchmarkArena_AllocFree_Parallel(b *testing.B) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		b.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			off := arena.Alloc(256)
			arena.Free(off)
		}
	})
}

// BenchmarkArena_LargeAlloc_Parallel measures concurrent contention on the raw bump
// allocator for sizes exceeding standard class lists.
func BenchmarkArena_LargeAlloc_Parallel(b *testing.B) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		b.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			off := arena.Alloc(4096)
			arena.Free(off)
		}
	})
}

// BenchmarkArena_AllocBatch measures throughput of chunked allocations vs single bumps.
func BenchmarkArena_AllocBatch(b *testing.B) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		b.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	// Represent a realistic TLAB chunk: 16 items of 64 bytes each
	chunkSize := uint32(64 * 16)
	items := int32(16)

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		off := arena.AllocBatch(chunkSize, items)

		arena.AddActiveCount(off, -(items - 1))
		arena.Free(off)
	}
}

// BenchmarkArena_GetPtr ensures the //go:nosplit pointer arithmetic remains zero-cost.
func BenchmarkArena_GetPtr(b *testing.B) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		b.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()
	
	off := arena.Alloc(64)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = arena.GetPtr(off)
	}
}