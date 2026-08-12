package utils

import (
	"maps"
	"slices"
	"unsafe"
)

//go:fix inline
func SlicesEqualFast[S ~[]E, E comparable](s1, s2 S) bool {
	if len(s1) != len(s2) {
		return false
	}
	if len(s1) == 0 {
		return true
	}
	if unsafe.SliceData(s1) == unsafe.SliceData(s2) {
		return true
	}
	return slices.Equal(s1, s2)
}


//go:fix inline
func SlicesEqualFuncFast[S1 ~[]E1, S2 ~[]E2, E1, E2 any](s1 S1, s2 S2, eq func(E1, E2) bool) bool {
	if len(s1) != len(s2) {
		return false
	}
	if len(s1) == 0 {
		return true
	}
	if unsafe.Pointer(unsafe.SliceData(s1)) == unsafe.Pointer(unsafe.SliceData(s2)) {
		return true
	}
	return slices.EqualFunc(s1, s2, eq)
}

//go:fix inline
func MapsEqualFast[M ~map[K]V, K, V comparable](m1, m2 M) bool {
	if len(m1) != len(m2) {
		return false
	}
	if len(m1) == 0 {
		return true
	}

	if *(*uintptr)(unsafe.Pointer(&m1)) == *(*uintptr)(unsafe.Pointer(&m2)) {
		return true
	}
	return maps.Equal(m1, m2)
}


//go:fix inline
func MapsEqualFuncFast[M1 ~map[K]V1, M2 ~map[K]V2, K comparable, V1, V2 any](m1 M1, m2 M2, eq func(V1, V2) bool) bool {
	if len(m1) != len(m2) {
		return false
	}
	if len(m1) == 0 {
		return true
	}
	if *(*uintptr)(unsafe.Pointer(&m1)) == *(*uintptr)(unsafe.Pointer(&m2)) {
		return true
	}
	return maps.EqualFunc(m1, m2, eq)
}