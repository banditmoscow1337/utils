package utils

import (
	"maps"
	"slices"
	"strconv"
	"testing"
)

// ==============================================================================
// UNIT TESTS
// ==============================================================================

func TestSlicesEqualFast(t *testing.T) {
	s1 := []int{1, 2, 3}
	s2 := []int{1, 2, 3}
	s3 := []int{1, 2, 4}
	s4 := []int{1, 2}
	var sNil []int
	var sEmpty = []int{}

	tests := []struct {
		name     string
		a, b     []int
		expected bool
	}{
		{"same pointer", s1, s1, true},
		{"different pointer, same content", s1, s2, true},
		{"different content", s1, s3, false},
		{"different length", s1, s4, false},
		{"both nil", sNil, sNil, true},
		{"both empty", sEmpty, sEmpty, true},
		{"nil and empty", sNil, sEmpty, true}, // len == 0 fast path
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if res := SlicesEqualFast(tt.a, tt.b); res != tt.expected {
				t.Errorf("SlicesEqualFast() = %v; want %v", res, tt.expected)
			}
		})
	}
}

func TestSlicesEqualFuncFast(t *testing.T) {
	s1 := []int{1, 2, 3}
	s2 := []string{"1", "2", "3"}
	s3 := []string{"1", "2", "4"}

	eqFunc := func(a int, b string) bool {
		return strconv.Itoa(a) == b
	}

	if !SlicesEqualFuncFast(s1, s2, eqFunc) {
		t.Error("expected true for functionally equal slices")
	}
	if SlicesEqualFuncFast(s1, s3, eqFunc) {
		t.Error("expected false for functionally different slices")
	}

	// Test identical pointers early exit with same types
	eqFuncInt := func(a, b int) bool { return a == b }
	if !SlicesEqualFuncFast(s1, s1, eqFuncInt) {
		t.Error("expected true for identical pointers")
	}
}

func TestMapsEqualFast(t *testing.T) {
	m1 := map[string]int{"a": 1, "b": 2}
	m2 := map[string]int{"a": 1, "b": 2}
	m3 := map[string]int{"a": 1, "b": 3}
	m4 := map[string]int{"a": 1}
	var mNil map[string]int
	mEmpty := make(map[string]int)

	tests := []struct {
		name     string
		a, b     map[string]int
		expected bool
	}{
		{"same pointer", m1, m1, true},
		{"different pointer, same content", m1, m2, true},
		{"different content", m1, m3, false},
		{"different length", m1, m4, false},
		{"both nil", mNil, mNil, true},
		{"both empty", mEmpty, mEmpty, true},
		{"nil and empty", mNil, mEmpty, true}, // len == 0 fast path
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if res := MapsEqualFast(tt.a, tt.b); res != tt.expected {
				t.Errorf("MapsEqualFast() = %v; want %v", res, tt.expected)
			}
		})
	}
}

func TestMapsEqualFuncFast(t *testing.T) {
	m1 := map[string]int{"a": 1, "b": 2}
	m2 := map[string]string{"a": "1", "b": "2"}
	m3 := map[string]string{"a": "1", "b": "3"}

	eqFunc := func(a int, b string) bool {
		return strconv.Itoa(a) == b
	}

	if !MapsEqualFuncFast(m1, m2, eqFunc) {
		t.Error("expected true for functionally equal maps")
	}
	if MapsEqualFuncFast(m1, m3, eqFunc) {
		t.Error("expected false for functionally different maps")
	}
}

// ==============================================================================
// BENCHMARKS
// ==============================================================================

func BenchmarkSlicesEqualFast_SamePointer(b *testing.B) {
	s := make([]int, 10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SlicesEqualFast(s, s)
	}
}

func BenchmarkStdSlicesEqual_SamePointer(b *testing.B) {
	s := make([]int, 10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = slices.Equal(s, s)
	}
}

func BenchmarkSlicesEqualFast_DiffPointer(b *testing.B) {
	s1 := make([]int, 10000)
	s2 := make([]int, 10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SlicesEqualFast(s1, s2)
	}
}

func BenchmarkStdSlicesEqual_DiffPointer(b *testing.B) {
	s1 := make([]int, 10000)
	s2 := make([]int, 10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = slices.Equal(s1, s2)
	}
}

func BenchmarkMapsEqualFast_SamePointer(b *testing.B) {
	m := make(map[int]int, 1000)
	for i := 0; i < 1000; i++ {
		m[i] = i
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = MapsEqualFast(m, m)
	}
}

func BenchmarkStdMapsEqual_SamePointer(b *testing.B) {
	m := make(map[int]int, 1000)
	for i := 0; i < 1000; i++ {
		m[i] = i
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = maps.Equal(m, m)
	}
}

func BenchmarkMapsEqualFast_DiffPointer(b *testing.B) {
	m1 := make(map[int]int, 1000)
	m2 := make(map[int]int, 1000)
	for i := 0; i < 1000; i++ {
		m1[i] = i
		m2[i] = i
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = MapsEqualFast(m1, m2)
	}
}

func BenchmarkStdMapsEqual_DiffPointer(b *testing.B) {
	m1 := make(map[int]int, 1000)
	m2 := make(map[int]int, 1000)
	for i := 0; i < 1000; i++ {
		m1[i] = i
		m2[i] = i
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = maps.Equal(m1, m2)
	}
}