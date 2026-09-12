package utils

import (
	"bytes"
	"cmp"
	"fmt"
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

func CompareField(name string, cmp func() error) error {
	if err := cmp(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func ComparePrimitive[T comparable](a, b T) error {
	if a != b {
		return fmt.Errorf("mismatch: %v != %v", a, b)
	}
	return nil
}

func CompareBytes(a, b []byte) error {
	if !bytes.Equal(a, b) {
		return fmt.Errorf("mismatch: %x != %x", a, b)
	}
	return nil
}

func CompareSlice[T any](a, b []T, elemCmp func(T, T) error) error {
	if len(a) != len(b) {
		return fmt.Errorf("length mismatch: %d != %d", len(a), len(b))
	}
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	for i := range a {
		if err := elemCmp(a[i], b[i]); err != nil {
			return fmt.Errorf("index [%d]: %w", i, err)
		}
	}
	return nil
}

func CompareMap[K comparable, V any](a, b map[K]V, valCmp func(V, V) error) error {
	if len(a) != len(b) {
		return fmt.Errorf("length mismatch: %d != %d", len(a), len(b))
	}
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	for k, v1 := range a {
		v2, exists := b[k]
		if !exists {
			return fmt.Errorf("key %v missing in b", k)
		}
		if err := valCmp(v1, v2); err != nil {
			return fmt.Errorf("key %v value: %w", k, err)
		}
	}
	return nil
}

// ComparePointer compares two pointers, handling nil cases.
func ComparePointer[T any](a, b *T, elemCmp func(T, T) error) error {
	if a == nil && b == nil {
		return nil
	}
	if a == nil && b != nil {
		return fmt.Errorf("mismatch: a is nil, b is not")
	}
	if a != nil && b == nil {
		return fmt.Errorf("mismatch: a is not nil, b is nil")
	}
	return elemCmp(*a, *b)
}

// ComparePointerKeyMap handles maps where the key is a pointer. 
// Standard lookup fails because unmarshalling allocates new addresses.
func ComparePointerKeyMap[K comparable, V any](a, b map[*K]V, valCmp func(V, V) error) error {
	if len(a) != len(b) {
		return fmt.Errorf("length mismatch: %d != %d", len(a), len(b))
	}
	
	// We must iterate because we cannot look up by pointer address
	for k1, v1 := range a {
		found := false
		for k2, v2 := range b {
			// Check if keys point to the same value (assuming Comparable Primitives for now)
			// Note: If K is complex, you need a keyCmp function passed in here too.
			if (k1 == nil && k2 == nil) || (k1 != nil && k2 != nil && *k1 == *k2) {
				if err := valCmp(v1, v2); err != nil {
					return fmt.Errorf("key %v value mismatch: %w", k1, err)
				}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("key %v (val: %v) missing in b (addresses changed during unmarshal)", k1, *k1)
		}
	}
	return nil
}