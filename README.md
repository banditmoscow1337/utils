# Benchmark Results

**Environment:**
- **OS:** darwin
- **Arch:** arm64
- **CPU:** Apple M1
- **Package:** `github.com/banditmoscow1337/utils`

| Benchmark | Iterations | ns/op | B/op | allocs/op |
|-----------|------------|-------|------|-----------|
| BenchmarkArena_Alloc_Sequential-8 | 64962202 | 18.29 | 0 | 0 |
| BenchmarkArena_AllocFree_Parallel-8 | 3048301 | 392.6 | 0 | 0 |
| BenchmarkArena_LargeAlloc_Parallel-8 | 3265160 | 529.2 | 0 | 0 |
| BenchmarkArena_AllocBatch-8 | 244356 | 4973 | 0 | 0 |
| BenchmarkArena_GetPtr-8 | 1000000000 | 0.3132 | 0 | 0 |
| BenchmarkCmpMap/Size_10-8 | 1676319 | 720.6 | 0 | 0 |
| BenchmarkCmpMap/Size_100-8 | 81204 | 14307 | 0 | 0 |
| BenchmarkCmpMap/Size_1000-8 | 5511 | 216379 | 0 | 0 |
| BenchmarkCmpPointerValMapSlice-8 | 11032778 | 107.7 | 0 | 0 |
| BenchmarkFastHasher-8 | 1000000000 | 0.3192 | 0 | 0 |
| BenchmarkStrHasher/Len_5-8 | 208531758 | 5.429 | 0 | 0 |
| BenchmarkStrHasher/Len_29-8 | 329357823 | 3.632 | 0 | 0 |
| BenchmarkStrHasher/Len_98-8 | 181101777 | 6.643 | 0 | 0 |
| BenchmarkHashString_MapHash/Len_5-8 | 222550965 | 5.387 | 0 | 0 |
| BenchmarkHashString_MapHash/Len_29-8 | 328231760 | 3.617 | 0 | 0 |
| BenchmarkHashString_MapHash/Len_98-8 | 165556831 | 6.617 | 0 | 0 |
| BenchmarkHashInteger/Uint8-8 | 1000000000 | 0.3138 | 0 | 0 |
| BenchmarkHashInteger/Uint32-8 | 1000000000 | 0.3132 | 0 | 0 |
| BenchmarkHashInteger/Uint64-8 | 1000000000 | 0.3170 | 0 | 0 |
| BenchmarkEqPointer-8 | 1000000000 | 0.3183 | 0 | 0 |
| BenchmarkHashMap/Size_10-8 | 8057665 | 148.8 | 0 | 0 |
| BenchmarkHashMap/Size_100-8 | 957321 | 1211 | 0 | 0 |
| BenchmarkHashMap/Size_1000-8 | 89036 | 13538 | 0 | 0 |
| BenchmarkSlicesEqualFast_SamePointer-8 | 1000000000 | 0.3155 | 0 | 0 |
| BenchmarkStdSlicesEqual_SamePointer-8 | 369622 | 3185 | 0 | 0 |
| BenchmarkSlicesEqualFast_DiffPointer-8 | 357169 | 3266 | 0 | 0 |
| BenchmarkStdSlicesEqual_DiffPointer-8 | 368538 | 3216 | 0 | 0 |
| BenchmarkMapsEqualFast_SamePointer-8 | 953017870 | 1.260 | 0 | 0 |
| BenchmarkStdMapsEqual_SamePointer-8 | 85903 | 14163 | 0 | 0 |
| BenchmarkMapsEqualFast_DiffPointer-8 | 85765 | 14019 | 0 | 0 |
| BenchmarkStdMapsEqual_DiffPointer-8 | 84163 | 14271 | 0 | 0 |
| BenchmarkSwissMap_Put-8 | 33094964 | 58.92 | 0 | 0 |
| BenchmarkStdMap_Put-8 | 20690947 | 79.15 | 58 | 0 |
| BenchmarkSwissMap_GetHit-8 | 211022974 | 5.689 | 0 | 0 |
| BenchmarkStdMap_GetHit-8 | 187654717 | 6.420 | 0 | 0 |
| BenchmarkSwissMap_GetMiss-8 | 166188652 | 7.239 | 0 | 0 |
| BenchmarkSwissMap_StringKeys-8 | 100000000 | 10.75 | 0 | 0 |
| BenchmarkSwissMap_Delete-8 | 47831950 | 48.77 | 0 | 0 |
| BenchmarkStdMap_Delete-8 | 22178088 | 90.90 | 0 | 0 |
| BenchmarkSwissMap_Range-8 | 109732 | 11096 | 0 | 0 |
| BenchmarkStdMap_Range-8 | 25438 | 47353 | 0 | 0 |
| BenchmarkSwissMap_MixedWorkload-8 | 209808289 | 5.744 | 0 | 0 |
| BenchmarkSwissMap_Range_HighlyFragmented-8 | 121644 | 9759 | 0 | 0 |
| BenchmarkStdMap_Range_HighlyFragmented-8 | 18111 | 65848 | 0 | 0 |