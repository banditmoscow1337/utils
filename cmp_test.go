package utils

import (
	"cmp"
	"fmt"
	"strconv"
	"testing"
)

// Helper to easily generate pointers for tests
//go:fix inline
func ptr[T any](v T) *T {
	return new(v)
}

func TestCmpPointerVal(t *testing.T) {
	tests := []struct {
		name string
		a, b *int
		want int
	}{
		{"both nil", nil, nil, 0},
		{"a nil", nil, ptr(1), -1},
		{"b nil", ptr(1), nil, 1},
		{"equal values", ptr(5), ptr(5), 0},
		{"a less than b", ptr(3), ptr(5), -1},
		{"a greater than b", ptr(7), ptr(5), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CmpPointerVal(tt.a, tt.b); got != tt.want {
				t.Errorf("CmpPointerVal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCmpMap(t *testing.T) {
	cmpK := cmp.Compare[string]
	cmpV := cmp.Compare[int]

	tests := []struct {
		name string
		a, b map[string]int
		want int
	}{
		{
			name: "equal maps",
			a:    map[string]int{"x": 1, "y": 2},
			b:    map[string]int{"x": 1, "y": 2},
			want: 0,
		},
		{
			name: "a is shorter",
			a:    map[string]int{"x": 1},
			b:    map[string]int{"x": 1, "y": 2},
			want: -1,
		},
		{
			name: "b is shorter",
			a:    map[string]int{"x": 1, "y": 2},
			b:    map[string]int{"x": 1},
			want: 1,
		},
		{
			name: "both empty",
			a:    map[string]int{},
			b:    map[string]int{},
			want: 0,
		},
		{
			name: "different keys",
			a:    map[string]int{"a": 1, "c": 3},
			b:    map[string]int{"b": 2, "c": 3},
			want: -1, // "a" < "b"
		},
		{
			name: "different values",
			a:    map[string]int{"x": 1, "y": 2},
			b:    map[string]int{"x": 1, "y": 5},
			want: -1, // 2 < 5
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keysA := make([]string, 0)
			keysB := make([]string, 0)
			if got := CmpMap(tt.a, tt.b, cmpK, cmpV, &keysA, &keysB); got != tt.want {
				t.Errorf("CmpMap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCmpPointerValMapSlice(t *testing.T) {
	cmpK := cmp.Compare[int]

	tests := []struct {
		name string
		a, b map[int][]*string
		want int
	}{
		{
			name: "equal",
			a:    map[int][]*string{1: {ptr("a"), ptr("b")}},
			b:    map[int][]*string{1: {ptr("a"), ptr("b")}},
			want: 0,
		},
		{
			name: "slice length mismatch",
			a:    map[int][]*string{1: {ptr("a")}},
			b:    map[int][]*string{1: {ptr("a"), ptr("b")}},
			want: -1,
		},
		{
			name: "slice value mismatch",
			a:    map[int][]*string{1: {ptr("a"), ptr("b")}},
			b:    map[int][]*string{1: {ptr("a"), ptr("c")}},
			want: -1, // "b" < "c"
		},
		{
			name: "nil inside slice",
			a:    map[int][]*string{1: {ptr("a"), nil}},
			b:    map[int][]*string{1: {ptr("a"), ptr("c")}},
			want: -1, // nil < ptr
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keysA := make([]int, 0)
			keysB := make([]int, 0)
			if got := CmpPointerValMapSlice(tt.a, tt.b, cmpK, &keysA, &keysB); got != tt.want {
				t.Errorf("CmpPointerValMapSlice() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- Benchmarks ---
func BenchmarkCmpMap(b *testing.B) {
	sizes := []int{10, 100, 1000}
	
	for _, size := range sizes {
		b.Run(fmt.Sprintf("Size_%d", size), func(b *testing.B) {
			m1 := make(map[string]int, size)
			m2 := make(map[string]int, size)
			for i := 0; i < size; i++ {
				key := strconv.Itoa(i)
				m1[key] = i
				m2[key] = i
			}

			keysA := make([]string, 0, size)
			keysB := make([]string, 0, size)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Passing pre-allocated slices to test the optimization
				CmpMap(m1, m2, cmp.Compare[string], cmp.Compare[int], &keysA, &keysB)
			}
		})
	}
}

func BenchmarkCmpPointerValMapSlice(b *testing.B) {
	m1 := map[int][]*float64{
		1: {ptr(1.1), ptr(2.2), ptr(3.3)},
		2: {ptr(4.4), nil, ptr(5.5)},
	}
	m2 := map[int][]*float64{
		1: {ptr(1.1), ptr(2.2), ptr(3.3)},
		2: {ptr(4.4), nil, ptr(5.5)},
	}

	keysA := make([]int, 0, len(m1))
	keysB := make([]int, 0, len(m2))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CmpPointerValMapSlice(m1, m2, cmp.Compare[int], &keysA, &keysB)
	}
}