//go:build riscv64

package utils

const CacheLineSize = 64

type (
	PadCacheLineMinus4  [CacheLineSize - 4]byte
	PadCacheLineMinus8  [CacheLineSize - 8]byte
	PadCacheLineMinus16 [CacheLineSize - 16]byte
	PadCacheLineMinus32 [CacheLineSize - 32]byte
)