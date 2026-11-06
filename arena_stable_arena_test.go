//go:build linux && (amd64 || arm64 || riscv64)

package utils

import (
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
)

func testArena(t *testing.T, size uint64) *Arena {
	t.Helper()
	a, err := newStableArena(size)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	return a
}

func mustPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("expected panic")
		}
	}()
	f()
}

func assertEmpty(t *testing.T, a *Arena) {
	t.Helper()
	if err := a.Trim(); err != nil {
		t.Fatal(err)
	}
	s := a.Stats()
	if s.ActiveClaims != 0 || s.ClaimedBytes != 0 || s.FreeListBytes != 0 || s.StrandedBytes != 0 {
		t.Fatalf("not empty after Trim: %+v", s)
	}
	for _, mask := range a.freeMasks {
		if mask != 0 {
			t.Fatal("stale free-region bitmap")
		}
	}
}

func fillBlock(a *Arena, off, size uint64, value byte) {
	b := unsafe.Slice((*byte)(a.GetPtr(off)), int(size))
	for i := range b {
		b[i] = value
	}
}

func TestLayoutAndPointerFreeMetadata(t *testing.T) {
	if HeaderSize != 16 || unsafe.Sizeof(arenaMeta{}) != 128 || unsafe.Sizeof(Region{}) != 256 || unsafe.Sizeof(localTLAB{}) != 128 {
		t.Fatalf("unexpected layout: %d/%d/%d/%d", HeaderSize, unsafe.Sizeof(arenaMeta{}), unsafe.Sizeof(Region{}), unsafe.Sizeof(localTLAB{}))
	}
	var check func(reflect.Type)
	check = func(typ reflect.Type) {
		switch typ.Kind() {
		case reflect.Array:
			check(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				check(typ.Field(i).Type)
			}
		case reflect.Pointer, reflect.UnsafePointer, reflect.Slice, reflect.Map, reflect.Interface, reflect.String, reflect.Chan, reflect.Func:
			t.Errorf("pointer-bearing mapped type: %v", typ)
		}
	}
	check(reflect.TypeFor[arenaMeta]())
	check(reflect.TypeFor[Region]())
	check(reflect.TypeFor[BlockHeader]())
}

func TestSizeClassesIncludeHeaders(t *testing.T) {
	for _, tc := range []struct{ payload, total uint64 }{{16, 64}, {48, 64}, {49, 80}, {64, 80}, {65, 128}, {8192, 8256}, {8200, 8256}, {16384, 16448}, {RegionSize - HeaderSize, RegionSize}} {
		_, size := GetExtendedSizeClass(blockSize(tc.payload))
		if size != tc.total {
			t.Errorf("payload %d: size %d, want %d", tc.payload, size, tc.total)
		}
	}
	for i, size := range sizeClasses {
		class, got := GetExtendedSizeClass(size)
		if class != i || got != size || size&7 != 0 {
			t.Fatalf("bad class %d", i)
		}
		if i > 0 {
			previous := uint64(0)
			for _, candidate := range sizeClasses {
				if candidate < size && candidate > previous {
					previous = candidate
				}
			}
			class, _ = GetExtendedSizeClass(previous + 1)
			if class != i {
				t.Fatalf("bad lower boundary of class %d", i)
			}
		}
	}
	if class, _ := GetExtendedSizeClass(RegionSize + 1); class != -1 {
		t.Fatal("oversize class")
	}
}

func TestBasicAndBounds(t *testing.T) {
	a := testArena(t, 4*RegionSize)
	if a.Alloc(0) != 0 || a.AllocDense(0) != 0 || a.AllocZeroed(0) != 0 || a.GetPtr(0) != nil || a.GetOffset(nil) != 0 {
		t.Fatal("zero semantics")
	}
	a.Free(0)
	for _, size := range []uint64{1, 7, 8, 16, 63, 64, 8192, 16384, 131072} {
		off := a.Alloc(size)
		if off&7 != 0 || a.GetOffset(a.GetPtr(off)) != off {
			t.Fatal("alignment/offset")
		}
		h := (*BlockHeader)(a.GetPtr(off - HeaderSize))
		if h.Size < size+HeaderSize {
			t.Fatal("undersized payload")
		}
		fillBlock(a, off, size, 0xc3)
		a.Free(off)
	}
	if a.NeedsEvacuation(a.dataStart) {
		t.Fatal("non-moving arena requests evacuation")
	}
	mustPanic(t, func() { a.Alloc(^uint64(0)) })
	mustPanic(t, func() { a.Alloc(RegionSize) })
	mustPanic(t, func() { a.CheckedPtr(a.size) })
	mustPanic(t, func() { a.GetOffset(unsafe.Add(a.BasePtr(), uintptr(a.size))) })
	mustPanic(t, func() { a.AllocChunk(0, 1) })
	mustPanic(t, func() { a.AllocBatch(64, 0) })
	assertEmpty(t, a)
}

func TestTinyMappingAndTailFallback(t *testing.T) {
	for _, size := range []uint64{0, 1, 128, ^uint64(0)} {
		if a, err := newStableArena(size); err == nil {
			a.Close()
			t.Fatalf("accepted invalid size %d", size)
		}
	}
	a := testArena(t, 4096)
	var offsets []uint64
	for i := 0; i < 10; i++ {
		offsets = append(offsets, a.Alloc(16))
	}
	for _, off := range offsets {
		a.Free(off)
	}
	assertEmpty(t, a)
	if a.Alloc(16) == 0 {
		t.Fatal("tiny mapping failed to reuse")
	}
}

func TestBitmapReuseAndExplicitZeroing(t *testing.T) {
	for _, size := range []uint64{8192, 8200, 16384} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			a := testArena(t, 4*RegionSize)
			guard := a.Alloc(8)
			*(*uint64)(a.GetPtr(guard)) = 0x123456789abcdef
			p := a.Alloc(size)
			fillBlock(a, p, size, 0xa5)
			a.Free(p)
			q := a.Alloc(size)
			if q != p {
				t.Fatalf("free block not reused: %d != %d", q, p)
			}
			if *(*byte)(a.GetPtr(q)) != 0xa5 {
				t.Fatal("Alloc unexpectedly clears reused payload")
			}
			a.Free(q)
			z := a.AllocZeroed(size)
			if z != p {
				t.Fatal("zeroed allocation did not reuse")
			}
			for _, v := range unsafe.Slice((*byte)(a.GetPtr(z)), int(size)) {
				if v != 0 {
					t.Fatal("dirty AllocZeroed")
				}
			}
			if err := a.Trim(); err != nil {
				t.Fatal(err)
			}
			if got := *(*uint64)(a.GetPtr(guard)); got != 0x123456789abcdef {
				t.Fatal("trim damaged live neighbor")
			}
			a.Free(z)
			a.Free(guard)
			assertEmpty(t, a)
		})
	}
}

func TestEveryClassReusesWithLiveNeighbor(t *testing.T) {
	for class, total := range sizeClasses {
		// A full-region block has no room for a live neighbor: it uses region reuse.
		if total == RegionSize {
			continue
		}
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			a := testArena(t, 4*RegionSize)
			p := a.AllocDense(total - HeaderSize)
			guard := a.AllocDense(8)
			a.Free(p)
			q := a.AllocDense(total - HeaderSize)
			if q != p {
				t.Fatalf("class %d not reused", class)
			}
			a.Free(q)
			a.Free(guard)
			assertEmpty(t, a)
		})
	}
}

func TestPopBatchAndRawClaims(t *testing.T) {
	a := testArena(t, 4*RegionSize)
	guard := a.AllocDense(16)
	var blocks [10]uint64
	for i := range blocks {
		blocks[i] = a.AllocDense(64)
	}
	for _, p := range blocks {
		a.Free(p)
	}
	class, _ := GetExtendedSizeClass(blockSize(64))
	head, tail, n := a.PopBatch(class, 5)
	if n != 5 || head == 0 || tail == 0 {
		t.Fatal("bad batch", head, tail, n)
	}
	if h := (*BlockHeader)(a.GetPtr(tail - HeaderSize)); h.NextFree != 0 {
		t.Fatal("unterminated batch")
	}
	for off := head; off != 0; {
		next := (*BlockHeader)(a.GetPtr(off - HeaderSize)).NextFree
		a.Free(off)
		off = next
	}
	chunk := a.AllocChunk(640, 10)
	for i := uint64(0); i < 10; i++ {
		h := (*BlockHeader)(a.GetPtr(chunk + i*64))
		h.Size, h.NextFree = 64, 0
	}
	for i := uint64(0); i < 10; i++ {
		a.Free(chunk + i*64 + HeaderSize)
	}
	p := a.AllocBatch(80, 2)
	a.AddActiveCount(p, -1)
	a.Free(p)
	a.Free(guard)
	assertEmpty(t, a)
}

func TestChurnBeyondMappingCapacity(t *testing.T) {
	a := testArena(t, 4*RegionSize)
	guard := a.Alloc(8)
	*(*uint64)(a.GetPtr(guard)) = 0xbeef
	const size = 256 * 1024
	var offsets [16]uint64
	var warmHigh uint64
	for cycle := 0; cycle < 300; cycle++ {
		for i := range offsets {
			offsets[i] = a.Alloc(size)
			b := unsafe.Slice((*byte)(a.GetPtr(offsets[i])), size)
			b[0], b[size-1] = byte(i+1), byte(i+2)
		}
		if cycle == 10 {
			warmHigh = a.Stats().HighWaterMark
		}
		for i, off := range offsets {
			b := unsafe.Slice((*byte)(a.GetPtr(off)), size)
			if b[0] != byte(i+1) || b[size-1] != byte(i+2) {
				t.Fatal("live block overwritten")
			}
			a.Free(off)
		}
		if *(*uint64)(a.GetPtr(guard)) != 0xbeef {
			t.Fatal("live guard overwritten")
		}
		if cycle%25 == 0 {
			if err := a.Trim(); err != nil {
				t.Fatal(err)
			}
		}
	}
	s := a.Stats()
	if s.RegionReuses == 0 || s.HighWaterMark > warmHigh || s.OOMs != 0 {
		t.Fatalf("churn did not stabilize after warmup: high=%d stats=%+v", warmHigh, s)
	}
	a.Free(guard)
	assertEmpty(t, a)
}

func TestExactAllocationChurnAndFullRegion(t *testing.T) {
	a := testArena(t, 3*RegionSize)
	guard := a.AllocDense(16)
	*(*uint64)(a.GetPtr(guard)) = 123
	for i := 0; i < 2500; i++ {
		p := a.AllocBatch(40000, 1)
		*(*uint64)(a.GetPtr(p)) = uint64(i)
		a.Free(p)
	}
	if a.Stats().RegionReuses == 0 {
		t.Fatal("exact allocation regions not reused")
	}
	for i := 0; i < 20; i++ {
		p := a.Alloc(RegionSize - HeaderSize)
		fillBlock(a, p, RegionSize-HeaderSize, byte(i))
		a.Free(p)
	}
	if *(*uint64)(a.GetPtr(guard)) != 123 {
		t.Fatal("guard changed")
	}
	a.Free(guard)
	assertEmpty(t, a)
}

func TestOOMRecoveryAndIdleTLABDrain(t *testing.T) {
	a := testArena(t, 2*RegionSize)
	p := a.Alloc(RegionSize - HeaderSize)
	mustPanic(t, func() { a.Alloc(RegionSize - HeaderSize) })
	if a.Stats().OOMs != 1 {
		t.Fatal("OOM not counted")
	}
	a.Free(p)
	q := a.Alloc(RegionSize - HeaderSize)
	if q != p {
		t.Fatal("OOM left allocator unusable")
	}
	a.Free(q)
	// Simulate idle TLABs whose sync.Pool entries have been dropped by GC.
	var idle []*localTLAB
	for i := 0; i < 60; i++ {
		tlab := a.acquireTLAB()
		tlab.offset = a.reserve(TLABChunkSize, TLABChunkSize/64)
		tlab.regionIdx = tlab.offset / RegionSize
		tlab.rem, tlab.claims = TLABChunkSize, TLABChunkSize/64
		idle = append(idle, tlab)
	}
	for _, tlab := range idle {
		tlab.inUse.Store(false)
	}
	runtime.GC()
	// Neither region can fit this until reserve drains the idle registrations.
	large := a.Alloc(RegionSize - HeaderSize)
	a.Free(large)
	if a.Stats().OOMs != 1 {
		t.Fatal("idle reservations caused a false OOM")
	}
	assertEmpty(t, a)
}

func TestTrimProtectsBusyTLAB(t *testing.T) {
	a := testArena(t, 3*RegionSize)
	tlab := a.acquireTLAB()
	tlab.offset = a.reserve(TLABChunkSize, TLABChunkSize/64)
	tlab.regionIdx = tlab.offset / RegionSize
	tlab.rem, tlab.claims = TLABChunkSize, TLABChunkSize/64
	*(*uint64)(a.GetPtr(tlab.offset)) = 0xfee
	if err := a.Trim(); err != nil {
		t.Fatal(err)
	}
	if *(*uint64)(a.GetPtr(tlab.offset)) != 0xfee || a.Stats().ActiveClaims == 0 {
		t.Fatal("busy TLAB reclaimed")
	}
	a.releaseTLAB(tlab)
	assertEmpty(t, a)
}

func TestMetadataSpanningRegions(t *testing.T) {
	// A sparse shared file avoids reserving 18 GiB of anonymous commit charge.
	f, err := os.CreateTemp(t.TempDir(), "large-layout")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const size = 9000*RegionSize + 123
	if err = f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	a, err := openStableArena(size, int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	if a.dataStart <= RegionSize {
		t.Fatal("test needs multi-region metadata")
	}
	p := a.Alloc(8192)
	a.Free(p)
	assertEmpty(t, a)
	if a.meta.magic != arenaMagic || a.meta.mappingSize != a.size {
		t.Fatal("metadata purged")
	}
	q := a.Alloc(8192)
	fillBlock(a, q, 8192, 7)
	a.Free(q)
	assertEmpty(t, a)
}

func TestOffsetsAboveFourGiB(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "wide-offsets")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const n = 2050
	const size = (n + 2) * RegionSize
	if err = f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	a, err := openStableArena(size, int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	offsets := make([]uint64, n)
	for i := range offsets {
		offsets[i] = a.AllocDense(RegionSize - HeaderSize)
		*(*uint64)(a.GetPtr(offsets[i])) = uint64(i + 1)
	}
	last := offsets[n-1]
	if last <= 1<<32 || a.GetOffset(a.GetPtr(last)) != last {
		t.Fatal("offset truncated")
	}
	for i, off := range offsets {
		if *(*uint64)(a.GetPtr(off)) != uint64(i+1) {
			t.Fatal("wide-offset payload overwritten")
		}
		a.Free(off)
	}
	q := a.AllocDense(RegionSize - HeaderSize)
	if q != last {
		t.Fatal("wide-offset region not reused")
	}
	a.Free(q)
	assertEmpty(t, a)
}

func TestPartialLastRegion(t *testing.T) {
	a := testArena(t, RegionSize+4096+123)
	// Fill almost all of the first region, then use the unaligned final region.
	first := a.AllocBatch(RegionSize-a.dataStart-HeaderSize, 1)
	last := a.Alloc(2000)
	fillBlock(a, last, 2000, 0x37)
	if last/RegionSize != 1 {
		t.Fatal("did not exercise final region")
	}
	a.Free(last)
	a.Free(first)
	assertEmpty(t, a)
}

func TestFileReopenAndFormatRejection(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "arena")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const size = 4 * RegionSize
	if err = f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	a, err := openStableArena(size, int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	p := a.Alloc(8192)
	fillBlock(a, p, 8192, 0x71)
	dead := a.Alloc(64)
	a.Free(dead)
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = openStableArena(size, int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, v := range unsafe.Slice((*byte)(a.GetPtr(p)), 8192) {
		if v != 0x71 {
			t.Fatal("live data lost across reopen")
		}
	}
	a.Free(p)
	assertEmpty(t, a)
	if other, err := openStableArena(size-4096, int(f.Fd())); err == nil {
		other.Close()
		t.Fatal("accepted wrong mapping size")
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	var magic [8]byte
	binary.LittleEndian.PutUint64(magic[:], 0x6172656e61363401) // utils format
	if _, err = f.WriteAt(magic[:], 0); err != nil {
		t.Fatal(err)
	}
	if other, err := openStableArena(size, int(f.Fd())); err == nil {
		other.Close()
		t.Fatal("accepted utils mapping")
	}
	if other, err := openStableArena(size+4096, int(f.Fd())); err == nil {
		other.Close()
		t.Fatal("accepted short file")
	}
}

func TestConcurrentAllocFreeTrimAndStats(t *testing.T) {
	a := testArena(t, 32*RegionSize)
	var wg sync.WaitGroup
	var stop atomic.Bool
	var bad atomic.Bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			if err := a.Trim(); err != nil {
				bad.Store(true)
			}
			_ = a.Stats()
			runtime.Gosched()
		}
	}()
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			sizes := [...]uint64{16, 64, 8192, 16384, 65536, 262144}
			for i := 0; i < 1500; i++ {
				size := sizes[(i+id)%len(sizes)]
				off := a.Alloc(size)
				b := unsafe.Slice((*byte)(a.GetPtr(off)), int(size))
				v := byte(id + 1)
				b[0], b[len(b)-1] = v, v+1
				if i%7 == 0 {
					runtime.Gosched()
				}
				if b[0] != v || b[len(b)-1] != v+1 {
					bad.Store(true)
				}
				a.Free(off)
			}
		}(worker)
	}
	workers.Wait()
	stop.Store(true)
	wg.Wait()
	if bad.Load() {
		t.Fatal("concurrent corruption/trim failure")
	}
	assertEmpty(t, a)
}

func newStableArena(size uint64) (*Arena, error) {
	return NewArenaWithOptions(size, -1, ArenaOptions{NonMoving: true})
}
func openStableArena(size uint64, fd int) (*Arena, error) {
	return NewArenaWithOptions(size, fd, ArenaOptions{NonMoving: true})
}
