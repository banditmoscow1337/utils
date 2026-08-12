package utils

import (
	"hash/maphash"
	"unsafe"
)

//go:fix inline
func FastHasher(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

var seed = maphash.MakeSeed()

func HashString(s string) int {
	return int(maphash.String(seed, s))
}

func HashBytes(b []byte) int {
	return int(maphash.Bytes(seed, b))
}

func HashBytes64(b []byte) uint64 {
	return maphash.Bytes(seed, b)
}

func HashPointer[T comparable](p *T, hasher func(T) int) int {
	if p == nil {
		return 0
	}
	return hasher(*p)
}

func EqPointer[T comparable](a, b *T) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func HashInteger[T comparable](n T) int {
	var u uint64
	switch unsafe.Sizeof(n) {
	case 8:
		u = *(*uint64)(unsafe.Pointer(&n))
	case 4:
		u = uint64(*(*uint32)(unsafe.Pointer(&n)))
	case 2:
		u = uint64(*(*uint16)(unsafe.Pointer(&n)))
	case 1:
		u = uint64(*(*uint8)(unsafe.Pointer(&n)))
	default:
		// Acts as a compile-time guard.
		panic("unsupported generic size") 
	}

	u = FastHasher(u)
	
	return int(u)
}

func HashMap[K comparable, V any](m map[K]V, hk func(K) int, hv func(V) int) int {
	h := 0
	for k, v := range m {
		kh := hk(k)
		vh := hv(v)
		
		pairHash := kh ^ (vh + 0x9e3779b9 + (kh << 6) + (kh >> 2))
		h += pairHash
	}
	return h
}