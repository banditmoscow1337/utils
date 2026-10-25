package utils

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
)

func newTestArena64(t *testing.T, size uint64) *Arena64 {
	t.Helper()
	a, err := NewArena64[uint64](size, -1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func TestArena64AboveFourGiB(t *testing.T) {
	a, err := NewArena64[uint64](1<<32+RegionSize, -1)
	if err != nil {
		t.Skipf("cannot reserve a sparse 4 GiB virtual mapping: %v", err)
	}
	defer a.Close()
	// Only the mapping is large; this test touches a few pages. Advance the
	// bump to the end of the 4 GiB region to exercise the full-width offset.
	atomic.StoreUint64(&a.meta.bumpOffset, 1<<32-64)
	p := a.AllocDense(128)
	guard := a.AllocDense(128)
	if p <= 1<<32 || guard <= 1<<32 {
		t.Fatalf("offsets did not cross 4 GiB: %d, %d", p, guard)
	}
	if got := a.GetOffset(a.GetPtr(p)); got != p {
		t.Fatalf("round trip offset: got %d, want %d", got, p)
	}
	a.Free(p)
	q := a.Alloc(128)
	if q != p {
		t.Fatalf("free list failed to reuse a 64-bit offset: %d != %d", q, p)
	}
	*(*uint64)(a.GetPtr(q)) = 0x1234567890abcdef
	a.Free(guard)
	if got := *(*uint64)(a.GetPtr(q)); got != 0x1234567890abcdef {
		t.Fatalf("live data lost during region cleanup: %#x", got)
	}
	a.Free(q)
}

func TestArena64BatchClaimsAndFreeList(t *testing.T) {
	a := newTestArena64(t, 16*1024*1024)
	guard := a.AllocDense(48)
	off := a.AllocChunk(128, 2)
	p, q := off+blockHeaderSize64, off+64+blockHeaderSize64
	*(*BlockHeader64)(a.GetPtr(off)) = BlockHeader64{Size: 64}
	*(*BlockHeader64)(a.GetPtr(off + 64)) = BlockHeader64{Size: 64}
	r := &a.regions[off/RegionSize]
	a.Free(p)
	a.Free(q)
	head, tail, n := a.PopBatch(0, 2)
	if n != 2 || head != q || tail != p {
		t.Fatalf("PopBatch = (%d, %d, %d); want (%d, %d, 2)", head, tail, n, q, p)
	}
	if c, live := r.activeCount.Load(), r.liveBytes.Load(); c != 3 || live != 192 {
		t.Fatalf("restored claims/counts: active=%d live=%d; want 3, 192", c, live)
	}
	a.Free(p)
	a.Free(q)
	a.Free(guard)
}

func TestArena64ConcurrentReuse(t *testing.T) {
	a := newTestArena64(t, 64*1024*1024)
	const workers, rounds = 16, 1000
	var wg sync.WaitGroup
	errs := make(chan uint64, workers)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				size := uint64(48 + (i%3)*64)
				p := a.Alloc(size)
				want := uint64(worker+1)<<32 | uint64(i+1)
				*(*uint64)(a.GetPtr(p)) = want
				if i%4 == 0 {
					runtime.Gosched()
				}
				if got := *(*uint64)(a.GetPtr(p)); got != want {
					select {
					case errs <- p:
					default:
					}
					return
				}
				a.Free(p)
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for p := range errs {
		t.Fatalf("concurrent allocations overlapped at offset %d", p)
	}
}

func TestArena64MetadataAcrossRegions(t *testing.T) {
	if testing.Short() {
		t.Skip("reserves a large virtual address range")
	}
	// At 32 GiB the metadata extends into region 1. MADV_DONTNEED must
	// preserve those pages even when the region itself is purged.
	a, err := NewArena64[uint64](32<<30+RegionSize, -1)
	if err != nil {
		t.Skipf("cannot reserve a sparse 32 GiB virtual mapping: %v", err)
	}
	defer a.Close()
	if a.dataStart <= RegionSize {
		t.Fatal("test setup: metadata does not reach the second region")
	}
	last := &a.regions[len(a.regions)-1]
	last.evacuate.Store(7)
	a.purgeRegion(1)
	if last.evacuate.Load() != 7 {
		t.Fatal("purging region 1 discarded another region's metadata")
	}
}

func TestArena64Bounds(t *testing.T) {
	if a, err := NewArena64[byte](uint64(unsafe.Sizeof(arenaMeta64{})), -1); err == nil {
		_ = a.Close()
		t.Fatal("accepted a mapping too small for its metadata")
	}
	a := newTestArena64(t, RegionSize)
	if a.Alloc(0) != 0 || a.GetPtr(0) != nil || a.GetOffset(nil) != 0 {
		t.Fatal("incorrect zero-offset behavior")
	}
	a.Free(0)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("oversized allocation did not panic")
			}
		}()
		a.Alloc(^uint64(0))
	}()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("out-of-bounds pointer did not panic")
			}
		}()
		a.GetOffset(unsafe.Pointer(uintptr(a.BasePtr()) + uintptr(RegionSize+8)))
	}()
}

func BenchmarkArena64AllocFree(b *testing.B) {
	a, err := NewArena64[byte](16*1024*1024, -1)
	if err != nil {
		b.Fatal(err)
	}
	defer a.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := a.Alloc(128)
		a.Free(off)
	}
}

func BenchmarkArena64AllocFreeParallel(b *testing.B) {
	a, err := NewArena64[byte](64*1024*1024, -1)
	if err != nil {
		b.Fatal(err)
	}
	defer a.Close()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			off := a.Alloc(128)
			a.Free(off)
		}
	})
}
