//go:build linux && (amd64 || arm64 || riscv64)

package utils

import (
	"os"
	"sync"
	"testing"
)

func TestDefaultIndexActivationRetainsFreeLists(t *testing.T) {
	a, err := NewArena[uint64]((indexedRegionThreshold+4)*RegionSize, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	p := a.AllocDense(16)
	guard := a.AllocDense(16)
	a.Free(p)
	if a.mode.Load()&modeIndexReady != 0 {
		t.Fatal("small default arena indexed early")
	}
	offsets := make([]uint64, indexedRegionThreshold)
	for i := range offsets {
		offsets[i] = a.AllocBatch(RegionSize-HeaderSize, 1)
	}
	if a.mode.Load()&modeIndexReady == 0 {
		t.Fatal("large default arena failed to enable index")
	}
	class, _ := GetSizeClass(64)
	q, _, n := a.PopBatch(class, 1)
	if n != 1 || q != p {
		t.Fatalf("existing free block lost: %d %d want %d", q, n, p)
	}
	a.Free(q)
	a.Free(guard)
	for _, off := range offsets {
		a.Free(off)
	}
	if err = a.Trim(); err != nil {
		t.Fatal(err)
	}
	if a.Stats().ActiveClaims != 0 {
		t.Fatal("claims stranded")
	}
}
func TestDefaultIndexedFileReopen(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "indexed")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	size := (indexedRegionThreshold + 4) * RegionSize
	if err = f.Truncate(int64(size)); err != nil {
		t.Fatal(err)
	}
	a, err := NewArena[uint64](size, int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	p := a.AllocDense(16)
	guard := a.AllocDense(16)
	*(*uint64)(a.GetPtr(guard)) = 0x12345678
	a.Free(p)
	offsets := make([]uint64, indexedRegionThreshold)
	for i := range offsets {
		offsets[i] = a.AllocBatch(RegionSize-HeaderSize, 1)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = NewArena[uint64](size, int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.mode.Load()&modeIndexReady == 0 {
		t.Fatal("reopened large arena has no index")
	}
	q := a.Alloc(16)
	if q != p || *(*uint64)(a.GetPtr(guard)) != 0x12345678 {
		t.Fatal("reopened index lost block or damaged payload")
	}
	a.Free(q)
	a.Free(guard)
	for _, off := range offsets {
		a.Free(off)
	}
}
func TestConcurrentDefaultIndexActivation(t *testing.T) {
	a, err := NewArena[uint64]((indexedRegionThreshold+16)*RegionSize, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	guard := a.Alloc(16)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5000; i++ {
				off := a.Alloc(64)
				*(*uint64)(a.GetPtr(off)) = 0x87654321
				if *(*uint64)(a.GetPtr(off)) != 0x87654321 {
					t.Error("live payload changed")
				}
				a.Free(off)
			}
		}()
	}
	offsets := make([]uint64, indexedRegionThreshold)
	for i := range offsets {
		offsets[i] = a.AllocBatch(RegionSize-HeaderSize, 1)
	}
	wg.Wait()
	a.Free(guard)
	for _, off := range offsets {
		a.Free(off)
	}
	if err = a.Trim(); err != nil {
		t.Fatal(err)
	}
	if a.Stats().ActiveClaims != 0 {
		t.Fatal("concurrent index activation stranded claims")
	}
}
