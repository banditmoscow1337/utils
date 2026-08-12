package utils

import (
	"fmt"
	"testing"
	"unsafe"
)

// ==============================================================================
// UNIT TESTS
// ==============================================================================

func TestFastHasher_Correctness(t *testing.T) {
	h1 := FastHasher(42)
	h2 := FastHasher(42)
	if h1 != h2 {
		t.Fatalf("FastHasher is non-deterministic: %d != %d", h1, h2)
	}

	h3 := FastHasher(43)
	if h1 == h3 {
		t.Fatalf("FastHasher produced collision on sequential integers: %d", h1)
	}
}

func TestMapHash_Wrappers(t *testing.T) {
	strVal := "test_string_for_maphash"
	byteVal := []byte(strVal)

	if HashString(strVal) != HashBytes(byteVal) {
		t.Errorf("HashString and HashBytes produced different hashes for identical bytes")
	}

	if uint64(HashBytes(byteVal)) != HashBytes64(byteVal) && unsafe.Sizeof(int(0)) == 8 {
		t.Errorf("HashBytes and HashBytes64 mismatched on 64-bit architecture")
	}
}

func TestHashPointer(t *testing.T) {
	intHasher := func(v int) int { return v * 100 }

	var nilPtr *int
	if h := HashPointer(nilPtr, intHasher); h != 0 {
		t.Errorf("expected 0 for nil pointer, got %d", h)
	}

	val := 42
	ptr := &val
	if h := HashPointer(ptr, intHasher); h != 4200 {
		t.Errorf("expected 4200 for valid pointer, got %d", h)
	}
}

func TestEqPointer(t *testing.T) {
	v1, v2, v3 := 100, 100, 200

	p1 := &v1
	p2 := &v2
	p3 := &v3
	var pNil1, pNil2 *int

	tests := []struct {
		name     string
		a, b     *int
		expected bool
	}{
		{"both nil", pNil1, pNil2, true},
		{"same pointer", p1, p1, true},
		{"equal values, diff pointers", p1, p2, true},
		{"different values", p1, p3, false},
		{"first nil", pNil1, p1, false},
		{"second nil", p1, pNil1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if res := EqPointer(tt.a, tt.b); res != tt.expected {
				t.Errorf("EqPointer(%v, %v) = %v; want %v", tt.a, tt.b, res, tt.expected)
			}
		})
	}
}

func TestHashInteger_ValidSizes(t *testing.T) {
	// Size 1
	var u8 uint8 = 255
	var i8 int8 = -1
	if HashInteger(u8) != HashInteger(i8) {
		t.Errorf("HashInteger(uint8(255)) != HashInteger(int8(-1)) under raw bit reinterpret")
	}

	// Size 2
	var u16 uint16 = 65535
	if h := HashInteger(u16); h == 0 {
		t.Errorf("unexpected zero hash for uint16")
	}

	// Size 4
	var u32 uint32 = 0xDEADBEEF
	if h := HashInteger(u32); h == 0 {
		t.Errorf("unexpected zero hash for uint32")
	}

	// Size 8
	var u64 uint64 = 0xDEADBEEFCAFEBAB1
	if h := HashInteger(u64); h == 0 {
		t.Errorf("unexpected zero hash for uint64")
	}
}

func TestHashInteger_UnsupportedSizePanic(t *testing.T) {
	type InvalidStruct [3]byte // Sizeof = 3

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected HashInteger to panic on unsupported size 3")
		}
	}()

	_ = HashInteger(InvalidStruct{1, 2, 3})
}

func TestHashMap_OrderInvariance(t *testing.T) {
	m1 := map[string]int{"a": 1, "b": 2, "c": 3}
	m2 := map[string]int{"c": 3, "b": 2, "a": 1}

	strHasher := func(s string) int { return HashString(s) }
	intHasher := func(i int) int { return i }

	h1 := HashMap(m1, strHasher, intHasher)
	h2 := HashMap(m2, strHasher, intHasher)

	if h1 != h2 {
		t.Fatalf("HashMap non-deterministic across identical maps: %d != %d", h1, h2)
	}

	m3 := map[string]int{"a": 1, "b": 2, "c": 4}
	if h1 == HashMap(m3, strHasher, intHasher) {
		t.Errorf("HashMap produced collision for different map values")
	}

	if hEmpty := HashMap(map[string]int{}, strHasher, intHasher); hEmpty != 0 {
		t.Errorf("expected 0 for empty map, got %d", hEmpty)
	}
}

// ==============================================================================
// BENCHMARKS
// ==============================================================================

func BenchmarkFastHasher(b *testing.B) {
	b.ReportAllocs()
	var h uint64
	for i := 0; i < b.N; i++ {
		h = FastHasher(uint64(i))
	}
	_ = h
}

func BenchmarkStrHasher(b *testing.B) {
	inputs := []string{
		"short",
		"medium_length_string_for_hash",
		"a_very_long_string_that_exceeds_standard_cache_line_boundaries_and_tests_loop_unrolling_throughput",
	}

	for _, str := range inputs {
		b.Run(fmt.Sprintf("Len_%d", len(str)), func(b *testing.B) {
			b.ReportAllocs()
			var h uint64
			for i := 0; i < b.N; i++ {
				h = uint64(HashString(str))
			}
			_ = h
		})
	}
}

func BenchmarkHashString_MapHash(b *testing.B) {
	inputs := []string{
		"short",
		"medium_length_string_for_hash",
		"a_very_long_string_that_exceeds_standard_cache_line_boundaries_and_tests_loop_unrolling_throughput",
	}

	for _, str := range inputs {
		b.Run(fmt.Sprintf("Len_%d", len(str)), func(b *testing.B) {
			b.ReportAllocs()
			var h int
			for i := 0; i < b.N; i++ {
				h = HashString(str)
			}
			_ = h
		})
	}
}

func BenchmarkHashInteger(b *testing.B) {
	b.Run("Uint8", func(b *testing.B) {
		v := uint8(255)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = HashInteger(v)
		}
	})

	b.Run("Uint32", func(b *testing.B) {
		v := uint32(0xDEADBEEF)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = HashInteger(v)
		}
	})

	b.Run("Uint64", func(b *testing.B) {
		v := uint64(0xDEADBEEFCAFEBAB1)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = HashInteger(v)
		}
	})
}

func BenchmarkEqPointer(b *testing.B) {
	v1, v2 := 100, 100
	p1, p2 := &v1, &v2

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = EqPointer(p1, p2)
	}
}

func BenchmarkHashMap(b *testing.B) {
	strHasher := func(s string) int { return HashString(s) }
	intHasher := func(i int) int { return i }

	sizes := []int{10, 100, 1000}

	for _, size := range sizes {
		m := make(map[string]int, size)
		for i := 0; i < size; i++ {
			m[fmt.Sprintf("key_%d", i)] = i
		}

		b.Run(fmt.Sprintf("Size_%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = HashMap(m, strHasher, intHasher)
			}
		})
	}
}