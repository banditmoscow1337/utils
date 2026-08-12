package utils

import (
	"cmp"
	"slices"
)

func CmpMap[K comparable, V any](a, b map[K]V, cmpK func(K, K) int, cmpV func(V, V) int, keysA, keysB *[]K) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}

	if len(a) == 0 {
		return 0
	}

	*keysA = (*keysA)[:0]
	for k := range a {
		*keysA = append(*keysA, k)
	}
	slices.SortFunc(*keysA, cmpK)

	*keysB = (*keysB)[:0]
	for k := range b {
		*keysB = append(*keysB, k)
	}
	slices.SortFunc(*keysB, cmpK)

	for i := range *keysA {
		if c := cmpK((*keysA)[i], (*keysB)[i]); c != 0 {
			return c
		}
		if c := cmpV(a[(*keysA)[i]], b[(*keysB)[i]]); c != 0 {
			return c
		}
	}
	return 0
}

// CmpPointerValMapSlice now requires the caller to provide the scratch slices.
func CmpPointerValMapSlice[K comparable, V cmp.Ordered](a, b map[K][]*V, cmpK func(K, K) int, keysA, keysB *[]K) int {
	return CmpMap(a, b, cmpK, func(sa, sb []*V) int {
		if len(sa) != len(sb) {
			if len(sa) < len(sb) {
				return -1
			}
			return 1
		}
		for i := range sa {
			if c := CmpPointerVal(sa[i], sb[i]); c != 0 {
				return c
			}
		}
		return 0
	}, keysA, keysB)
}

// CmpPointerVal constrains T to cmp.Ordered.
func CmpPointerVal[T cmp.Ordered](a, b *T) int {
	if a == b {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	
	if *a < *b {
		return -1
	}
	if *a > *b {
		return 1
	}
	return 0
}