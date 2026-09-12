package utils

import (
	"math/rand"
	"time"
)

const MaxDepth = 2 // Controls the maximum nesting level for recursive structs

func RandomCount(r *rand.Rand) int {
	return 1 + r.Intn(3) // Generate slices/maps with 1 to 3 elements for brevity in testing
}

func GenerateBool(r *rand.Rand, _ int) bool {
	return r.Intn(2) == 1
}

func GenerateInt(r *rand.Rand, _ int) int {
	return r.Int()
}

func GenerateInt8(r *rand.Rand, _ int) int8 {
	return int8(r.Intn(256) - 128)
}

func GenerateInt16(r *rand.Rand, _ int) int16 {
	return int16(r.Intn(65536) - 32768)
}

func GenerateInt32(r *rand.Rand, _ int) int32 {
	return r.Int31()
}

func GenerateInt64(r *rand.Rand, _ int) int64 {
	return r.Int63()
}

func GenerateUint(r *rand.Rand, _ int) uint {
	return uint(r.Uint64())
}

func GenerateUint8(r *rand.Rand, _ int) uint8 {
	return uint8(r.Intn(256))
}

func GenerateUint16(r *rand.Rand, _ int) uint16 {
	return uint16(r.Intn(65536))
}

func GenerateUint32(r *rand.Rand, _ int) uint32 {
	return r.Uint32()
}

func GenerateUint64(r *rand.Rand, _ int) uint64 {
	return r.Uint64()
}

func GenerateUintptr(r *rand.Rand, _ int) uintptr {
	return uintptr(r.Uint64())
}

func GenerateFloat32(r *rand.Rand, _ int) float32 {
	return r.Float32()
}

func GenerateFloat64(r *rand.Rand, _ int) float64 {
	return r.Float64()
}

func GenerateComplex64(r *rand.Rand, _ int) complex64 {
	return complex(r.Float32(), r.Float32())
}

func GenerateComplex128(r *rand.Rand, _ int) complex128 {
	return complex(r.Float64(), r.Float64())
}

func GenerateString(r *rand.Rand, _ int) string {
	return RandomString(r, 5+r.Intn(15))
}

func GenerateByte(r *rand.Rand, _ int) byte {
	return byte(r.Intn(256))
}

func GenerateBytes(r *rand.Rand, _ int) []byte {
	return RandomBytes(r, 3+r.Intn(7))
}

func GenerateRune(r *rand.Rand, _ int) rune {
	return r.Int31()
}

func GenerateTime(r *rand.Rand, _ int) time.Time {
	return RandomTime(r)
}

//endregion

// region Slice Generators

func GenerateSlice[T any](r *rand.Rand, depth int, generator func(*rand.Rand, int) T) []T {
	s := make([]T, RandomCount(r))
	for i := range s {
		s[i] = generator(r, depth)
	}
	return s
}

func GenerateSliceSlice[T any](r *rand.Rand, depth int, generator func(*rand.Rand, int) T) [][]T {
	s := make([][]T, RandomCount(r))
	for i := range s {
		s[i] = GenerateSlice(r, depth, generator)
	}
	return s
}

// Helper function for byte slice comparison
func BytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// GeneratePointer randomly returns either a nil pointer or a pointer to a generated value.
func GeneratePointer[T any](r *rand.Rand, depth int, generator func(*rand.Rand, int) T) *T {
	// Return nil pointers occasionally to test that case.
	if r.Intn(4) == 0 {
		return nil
	}
	v := generator(r, depth)
	return &v
}

// region Map Generators

func GenerateMap[K comparable, V any](r *rand.Rand, depth int, keyGen func(*rand.Rand, int) K, valGen func(*rand.Rand, int) V) map[K]V {
	count := RandomCount(r)
	m := make(map[K]V, count)
	for i := 0; i < count; i++ {
		m[keyGen(r, depth)] = valGen(r, depth)
	}
	return m
}

//endregion

// Helper functions for random generation
func RandomString(r *rand.Rand, length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[r.Intn(len(charset))]
	}
	return string(b)
}

func RandomBytes(r *rand.Rand, length int) []byte {
	b := make([]byte, length)
	r.Read(b)
	return b
}

func RandomTime(r *rand.Rand) time.Time {
	return time.Now().Add(time.Duration(r.Int63n(1000000)) * time.Second)
}

func RandomTimePtr(r *rand.Rand) *time.Time {
	t := RandomTime(r)
	return &t
}