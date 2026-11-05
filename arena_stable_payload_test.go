//go:build linux && (amd64 || arm64 || riscv64)

package utils

import "testing"

func TestPayloadCapacity(t *testing.T) {
	a := testArena(t, 4*RegionSize)
	if a.PayloadCapacity(0) != 0 {
		t.Fatal("nil capacity")
	}
	for _, size := range []uint64{1, 16, 64, 8192, 16384, RegionSize - HeaderSize} {
		off := a.Alloc(size)
		_, total := GetExtendedSizeClass(blockSize(size))
		if got := a.PayloadCapacity(off); got != total-HeaderSize {
			t.Fatalf("size %d: capacity %d", size, got)
		}
		a.Free(off)
	}
	off := a.AllocBatch(10, 1)
	if a.PayloadCapacity(off) != 16 {
		t.Fatal("exact allocation alignment")
	}
	mustPanic(t, func() { a.PayloadCapacity(off + 1) })
	mustPanic(t, func() { a.PayloadCapacity(a.dataStart) })
	mustPanic(t, func() { a.PayloadCapacity(a.size + HeaderSize) })
	h := (*BlockHeader)(a.GetPtr(off - HeaderSize))
	saved := h.Size
	for _, invalid := range []uint64{0, HeaderSize - 1, 25, ^uint64(0), RegionSize + 8} {
		h.Size = invalid
		mustPanic(t, func() { a.PayloadCapacity(off) })
	}
	h.Size = saved
	a.Free(off)
	assertEmpty(t, a)
}
