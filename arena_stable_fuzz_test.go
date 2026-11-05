//go:build linux && (amd64 || arm64 || riscv64)

package utils

import (
	"testing"
	"unsafe"
)

func FuzzArenaOwnership(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0, 8, 16, 32, 64, 128, 255})
	f.Add([]byte{255, 255, 255, 255, 7, 23, 39, 55, 71, 87, 103, 119})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			data = data[:1024]
		}
		a := testArena(t, 16*RegionSize)
		type slot struct {
			off, size uint64
			mark      byte
		}
		var slots [16]slot
		sizes := [...]uint64{16, 64, 200, 4096, 8192, 8200, 16384, 65536}
		check := func() {
			for _, s := range slots {
				if s.off == 0 {
					continue
				}
				b := unsafe.Slice((*byte)(a.GetPtr(s.off)), int(s.size))
				if b[0] != s.mark || b[len(b)-1] != s.mark^0x5a {
					t.Fatal("live allocation corrupted")
				}
			}
		}
		for step, v := range data {
			check()
			i := int(v & 15)
			if slots[i].off != 0 {
				a.Free(slots[i].off)
				slots[i] = slot{}
			}
			if v&16 == 0 {
				size := sizes[(int(v)>>5)&7]
				off := a.Alloc(size)
				mark := byte(step + i)
				b := unsafe.Slice((*byte)(a.GetPtr(off)), int(size))
				b[0], b[len(b)-1] = mark, mark^0x5a
				slots[i] = slot{off, size, mark}
			}
			if step%17 == 0 {
				if err := a.Trim(); err != nil {
					t.Fatal(err)
				}
			}
		}
		check()
		for _, s := range slots {
			a.Free(s.off)
		}
		assertEmpty(t, a)
	})
}
