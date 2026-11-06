package utils

import (
	"encoding/binary"
	"os"
	"testing"
)

func TestCompact64ClassPreservesDefaultPolicy(t *testing.T) {
	for _, nonMoving := range []bool{false, true} {
		a, err := NewArenaWithOptions(4*RegionSize, -1, ArenaOptions{NonMoving: nonMoving})
		if err != nil {
			t.Fatal(err)
		}
		guard := a.AllocDense(8)
		p := a.AllocDense(64)
		want := uint64(112)
		if nonMoving {
			want = 64
		}
		if a.PayloadCapacity(p) != want {
			t.Fatal("unexpected 64-byte class", nonMoving, a.PayloadCapacity(p))
		}
		a.Free(p)
		if q := a.AllocDense(64); q != p {
			t.Fatal("compact class did not reuse freed block")
		} else {
			a.Free(q)
		}
		a.Free(guard)
		assertEmpty(t, a)
		a.Close()
	}
}

func TestPriorClassTableFormatsRejected(t *testing.T) {
	for _, tc := range []struct {
		options ArenaOptions
		magic   uint64
	}{{ArenaOptions{}, 0x6172656e61363402}, {ArenaOptions{NonMoving: true}, 0x6164706172650001}} {
		f, err := os.CreateTemp(t.TempDir(), "prior-format")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err = f.Truncate(int64(4 * RegionSize)); err != nil {
			t.Fatal(err)
		}
		a, err := NewArenaWithOptions(4*RegionSize, int(f.Fd()), tc.options)
		if err != nil {
			t.Fatal(err)
		}
		if err = a.Close(); err != nil {
			t.Fatal(err)
		}
		var magic [8]byte
		binary.LittleEndian.PutUint64(magic[:], tc.magic)
		if _, err = f.WriteAt(magic[:], 0); err != nil {
			t.Fatal(err)
		}
		if opened, err := NewArenaWithOptions(4*RegionSize, int(f.Fd()), tc.options); err == nil {
			opened.Close()
			t.Fatal("old class-table layout accepted")
		}
	}
}
