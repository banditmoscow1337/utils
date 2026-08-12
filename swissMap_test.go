//go:build amd64 || arm64 || riscv64

package utils

import (
	"math/bits"
	"strconv"
	"testing"
)

func FastHasherInt(x int) uint64 {
	return FastHasher(uint64(x))
}

func StringHasher(k string) uint64 {
	return uint64(HashString(k))
}

// ==============================================================================
// TESTS
// ==============================================================================

func TestSwissMap_BasicOperations(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	sm := NewSwissMap[int, string](arena, 16, FastHasherInt)

	// Test Put and Get
	sm.Put(1, "one")
	sm.Put(2, "two")
	sm.Put(3, "three")

	if val, ok := sm.Get(2); !ok || val != "two" {
		t.Errorf("expected to find key 2 with value 'two', got %v (ok: %v)", val, ok)
	}

	if val, ok := sm.Get(99); ok {
		t.Errorf("expected not to find key 99, got %v", val)
	}

	// Test Update
	sm.Put(2, "two-updated")
	if val, ok := sm.Get(2); !ok || val != "two-updated" {
		t.Errorf("expected updated value 'two-updated', got %v", val)
	}

	// Test Delete
	sm.Delete(2)
	if _, ok := sm.Get(2); ok {
		t.Error("expected key 2 to be deleted")
	}

	// Verify others remain
	if val, ok := sm.Get(1); !ok || val != "one" {
		t.Errorf("expected key 1 to remain after deleting key 2, got %v", val)
	}
}

func TestSwissMap_GrowAndRehash(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	// Initialize with minimum capacity to force multiple rehashes
	sm := NewSwissMap[int, int](arena, 8, FastHasherInt)
	
	const insertCount = 1000
	for i := 0; i < insertCount; i++ {
		sm.Put(i, i*10)
	}

	// Verify all items survived the rehash
	for i := 0; i < insertCount; i++ {
		val, ok := sm.Get(i)
		if !ok {
			t.Fatalf("key %d missing after rehashes", i)
		}
		if val != i*10 {
			t.Fatalf("key %d has wrong value: expected %d, got %d", i, i*10, val)
		}
	}
}

func TestSwissMap_TombstoneReclamation(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	sm := NewSwissMap[int, int](arena, 16, FastHasherInt)

	// Insert items
	for i := 0; i < 10; i++ {
		sm.Put(i, i)
	}

	// Delete odd keys to create tombstones
	for i := 1; i < 10; i += 2 {
		sm.Delete(i)
	}

	// Re-insert new keys that should ideally reclaim tombstone slots
	for i := 100; i < 105; i++ {
		sm.Put(i, i)
	}

	// Verify integrity of all expected live keys
	expectedLive := []int{0, 2, 4, 6, 8, 100, 101, 102, 103, 104}
	for _, key := range expectedLive {
		if val, ok := sm.Get(key); !ok || val != key {
			t.Errorf("expected key %d to be intact, got %v (ok: %v)", key, val, ok)
		}
	}

	// Verify deleted keys are actually gone
	deleted := []int{1, 3, 5, 7, 9}
	for _, key := range deleted {
		if _, ok := sm.Get(key); ok {
			t.Errorf("expected key %d to be deleted", key)
		}
	}
}

func TestSwissMap_Range(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	sm := NewSwissMap[int, int](arena, 32, FastHasherInt)
	expectedMap := make(map[int]int)

	for i := 0; i < 20; i++ {
		sm.Put(i, i*2)
		expectedMap[i] = i * 2
	}

	// Delete a few to ensure Range skips ctrlDeleted
	sm.Delete(5)
	sm.Delete(10)
	delete(expectedMap, 5)
	delete(expectedMap, 10)

	seen := make(map[int]int)
	sm.Range(func(key int, val int) bool {
		seen[key] = val
		return true // continue iteration
	})

	if len(seen) != len(expectedMap) {
		t.Fatalf("Range visited %d elements, expected %d", len(seen), len(expectedMap))
	}

	for k, v := range expectedMap {
		if seen[k] != v {
			t.Errorf("Range missed or mismatched key %d, expected val %d, got %d", k, v, seen[k])
		}
	}
}

func TestSwissMap_EdgeCases(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	// Map configured to force capacity rounding up
	sm := NewSwissMap[int, int](arena, 3, FastHasherInt)
	if sm.capacity < 8 {
		t.Errorf("expected capacity to be rounded up to at least 8, got %d", sm.capacity)
	}

	// Range on an empty map
	emptyMap := &SwissMap[int, int]{}
	count := 0
	emptyMap.Range(func(k int, v int) bool {
		count++
		return true
	})
	if count != 0 {
		t.Errorf("expected 0 iterations on empty map, got %d", count)
	}

	// Get on an empty map
	if _, ok := emptyMap.Get(1); ok {
		t.Error("Get on uninitialized map should safely return false")
	}
}

// TestSwissMap_InPlaceCompaction forces the map to fill with tombstones 
// until it triggers the in-place memory compaction branch inside Put.
func TestSwissMap_InPlaceCompaction(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	sm := NewSwissMap[int, int](arena, 16, FastHasherInt)
	initialCap := sm.capacity

	// maxLen for cap 16 is 14 (16 * 0.875)
	// We need length + dead >= 14, and length <= 7 to trigger in-place rehash.
	
	// 1. Insert 14 items (length=14, dead=0)
	for i := 0; i < 14; i++ {
		sm.Put(i, i)
	}

	// 2. Delete 10 items (length=4, dead=10)
	for i := 0; i < 10; i++ {
		sm.Delete(i)
	}

	if sm.dead != 10 || sm.length != 4 {
		t.Fatalf("pre-condition failed: expected len=4, dead=10, got len=%d, dead=%d", sm.length, sm.dead)
	}

	// 3. Put 1 item. length(4) + dead(10) = 14 >= maxLen(14). 
	// length(4) <= maxLen/2 (7). This MUST trigger t.rehash(t.capacity).
	sm.Put(99, 99)

	if sm.capacity != initialCap {
		t.Errorf("expected in-place compaction to retain capacity %d, got %d", initialCap, sm.capacity)
	}
	if sm.dead != 0 {
		t.Errorf("expected tombstones to be cleared, got dead=%d", sm.dead)
	}
	if sm.length != 5 {
		t.Errorf("expected length to be 5, got %d", sm.length)
	}
	
	// Verify data integrity
	if val, ok := sm.Get(99); !ok || val != 99 {
		t.Errorf("failed to get newly inserted key post-compaction")
	}
	for i := 10; i < 14; i++ {
		if val, ok := sm.Get(i); !ok || val != i {
			t.Errorf("failed to get surviving key %d post-compaction", i)
		}
	}
}

// TestSwissMap_CollisionsAndWrap ensures probing works correctly across the array boundary.
func TestSwissMap_CollisionsAndWrap(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	// Use a hasher that forces all keys to the exact same hash (worst case collision).
	badHasher := func(k int) uint64 { return 0xDEADBEEF }
	sm := NewSwissMap[int, int](arena, 16, badHasher)

	// Insert enough items to wrap around the physical backing array
	for i := 0; i < 12; i++ {
		sm.Put(i, i*10)
	}

	for i := 0; i < 12; i++ {
		val, ok := sm.Get(i)
		if !ok || val != i*10 {
			t.Errorf("failed to retrieve key %d under severe collision", i)
		}
	}
}

// TestSwissMap_InternalMatchers isolates the bitwise operations to ensure no false positives.
func TestSwissMap_InternalMatchers(t *testing.T) {
	// Construct a 64-bit group with distinct bytes:
	// Byte 0: 0x80 (Empty)
	// Byte 1: 0xFE (Deleted)
	// Byte 2: 0x42 (Target match)
	// Byte 3: 0x11 (Random)
	// Byte 4: 0x42 (Target match again)
	// Bytes 5-7: 0x00
	var group uint64 = 0x000000421142FE80

	emptyMatches := matchEmpty(group)
	if emptyMatches == 0 || (emptyMatches&0x80) == 0 {
		t.Errorf("matchEmpty failed to find 0x80 at byte 0. Result: %016x", emptyMatches)
	}

	emptyOrDelMatches := matchEmptyOrDeleted(group)
	if emptyOrDelMatches == 0 || (emptyOrDelMatches&0x8080) != 0x8080 {
		t.Errorf("matchEmptyOrDeleted failed to find empty/deleted at bytes 0 and 1. Result: %016x", emptyOrDelMatches)
	}

	h2Matches := matchH2(group, 0x42)
	// Expect matches at byte 2 and byte 4 (0x80 shifted by 16 and 32)
	expectedH2 := uint64(0x0000008000800000)
	if h2Matches != expectedH2 {
		t.Errorf("matchH2 failed. expected %016x, got %016x", expectedH2, h2Matches)
	}
}

// TestFastHasherInt_HighBitFailure proves that changes in the upper 32 bits
// never cascade down to the bits used by SwissMap.
func TestFastHasherInt_HighBitFailure(t *testing.T) {
	k1 := int(1 << 50)
	k2 := int(2 << 50)

	hA := FastHasherInt(k1)
	hB := FastHasherInt(k2)

	// Simulate SwissMap bit extraction for a map of capacity 1024 (mask 1023)
	h1A, h2A := uint32(hA>>7), uint8(hA&0x7F)
	h1B, h2B := uint32(hB>>7), uint8(hB&0x7F)

	idxA := h1A & 1023
	idxB := h1B & 1023

	if idxA == idxB && h2A == h2B {
		t.Fatalf("FATAL: 100%% collision. h2 and index are identical despite totally different input keys.\nhA: %064b\nhB: %064b", hA, hB)
	}
}

// TestHasher_StrictAvalanche measures if flipping 1 input bit flips ~50% of output bits.
func TestHasher_StrictAvalanche(t *testing.T) {
	const trials = 10000
	var totalFlippedBits int
	var totalBits int

	for i := 0; i < trials; i++ {
		baseKey := i * 0x1337BEEF
		baseHash := FastHasherInt(baseKey)

		// Flip each bit in the 64-bit integer
		for bit := 0; bit < 64; bit++ {
			mutatedKey := baseKey ^ (1 << bit)
			mutatedHash := FastHasherInt(mutatedKey)

			diff := baseHash ^ mutatedHash
			totalFlippedBits += bits.OnesCount64(diff)
			totalBits += 64
		}
	}

	ratio := float64(totalFlippedBits) / float64(totalBits)
	if ratio < 0.48 || ratio > 0.52 {
		t.Errorf("Avalanche failure: expected ~0.50, got %.4f. The hasher is not scrambling bits effectively.", ratio)
	}
}

// TestHasher_ChiSquare simulates inserting keys into buckets to test uniform distribution.
func TestHasher_ChiSquare(t *testing.T) {
	const numKeys = 100_000
	const numBuckets = 1024 // Must be power of 2 for bitmasking

	buckets := make([]int, numBuckets)
	for i := 0; i < numKeys; i++ {
		h := FastHasherInt(i)
		// Emulate SwissMap index calculation
		idx := uint32(h>>7) & (numBuckets - 1)
		buckets[idx]++
	}

	// Calculate Chi-Square statistic
	expected := float64(numKeys) / float64(numBuckets)
	var chiSquare float64
	for _, count := range buckets {
		diff := float64(count) - expected
		chiSquare += (diff * diff) / expected
	}

	// For 1023 degrees of freedom, the 99th percentile critical value is ~1131.
	// A value significantly higher indicates clustering (bad distribution).
	t.Logf("Chi-Square Statistic: %.2f (Ideal is roughly around %d)", chiSquare, numBuckets-1)
	if chiSquare > 1150 {
		t.Errorf("Distribution failed Chi-Square test. Keys are clustering.")
	}
}

// TestSwissMap_DifferentialFuzz subjects the map to a randomized sequence of
// operations and verifies its state against the standard Go map.
func TestSwissMap_DifferentialFuzz(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	sm := NewSwissMap[int, int](arena, 8, FastHasherInt)
	stdMap := make(map[int]int)

	const iterations = 50000
	const keyRange = 2000 // Keep key range constrained to force collisions and updates

	// deterministic seed for reproducibility
	var seed uint64 = 0x1337 
	randInt := func() int {
		seed ^= seed >> 12
		seed ^= seed << 25
		seed ^= seed >> 27
		return int(seed * 0x2545F4914F6CDD1D)
	}

	for i := 0; i < iterations; i++ {
		op := randInt() % 3
		key := randInt() % keyRange

		switch op {
		case 0, 1: // 66% chance to Put (simulate growth and updates)
			val := randInt()
			sm.Put(key, val)
			stdMap[key] = val
		case 2: // 33% chance to Delete
			sm.Delete(key)
			delete(stdMap, key)
		}

		// Periodically verify absolute state parity
		if i%5000 == 0 {
			if sm.length != uint32(len(stdMap)) {
				t.Fatalf("length mismatch at iter %d: swiss=%d, std=%d", i, sm.length, len(stdMap))
			}
			for k, expectedVal := range stdMap {
				actualVal, ok := sm.Get(k)
				if !ok || actualVal != expectedVal {
					t.Fatalf("data mismatch at iter %d for key %d: expected %d, got %d (ok=%v)", i, k, expectedVal, actualVal, ok)
				}
			}
		}
	}
}

func TestSwissMap_ZeroValuesAndLifecycle(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	sm := NewSwissMap[int, int](arena, 8, FastHasherInt)

	// 1. Zero-value key and value
	sm.Put(0, 0)
	if val, ok := sm.Get(0); !ok || val != 0 {
		t.Errorf("failed to handle zero-value key/value, got %v (ok: %v)", val, ok)
	}

	// 2. Lifecycle: Close the map
	sm.Close()
	if sm.capacity != 0 || sm.length != 0 || sm.offset != 0 {
		t.Errorf("Close failed to wipe map metadata")
	}

	// 3. Get on closed map should be safe and return false
	if _, ok := sm.Get(0); ok {
		t.Errorf("Get on closed map returned true")
	}

	// 4. Put on closed map should safely re-initialize
	sm.Put(1, 100)
	if val, ok := sm.Get(1); !ok || val != 100 {
		t.Errorf("failed to recover and re-initialize after Close, got %v (ok: %v)", val, ok)
	}
}

func TestSwissMap_UpdateWithoutGrowth(t *testing.T) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		t.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	sm := NewSwissMap[int, int](arena, 16, FastHasherInt)
	
	// Fill exactly to maxLen to avoid capacity growth
	maxLen := int(float32(16) * 0.875)
	for i := 0; i < maxLen; i++ {
		sm.Put(i, i)
	}
	
	initialCap := sm.capacity

	// Overwrite all existing keys
	for i := 0; i < maxLen; i++ {
		sm.Put(i, i*10)
	}

	if sm.capacity != initialCap {
		t.Errorf("map incorrectly grew capacity during updates. initial: %d, current: %d", initialCap, sm.capacity)
	}
	
	for i := 0; i < maxLen; i++ {
		if val, ok := sm.Get(i); !ok || val != i*10 {
			t.Errorf("update failed for key %d: got %d", i, val)
		}
	}
}

// ==============================================================================
// BENCHMARKS
// ==============================================================================

func BenchmarkSwissMap_Put(b *testing.B) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		b.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	sm := NewSwissMap[int, int](arena, 1024, FastHasherInt)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		sm.Put(i, i)
	}
}

func BenchmarkStdMap_Put(b *testing.B) {
	m := make(map[int]int, 1024)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		m[i] = i
	}
}

func BenchmarkSwissMap_GetHit(b *testing.B) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		b.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	sm := NewSwissMap[int, int](arena, 16384, FastHasherInt)
	for i := 0; i < 10000; i++ {
		sm.Put(i, i)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		sm.Get(i % 10000)
	}
}

func BenchmarkStdMap_GetHit(b *testing.B) {
	m := make(map[int]int, 16384)
	for i := 0; i < 10000; i++ {
		m[i] = i
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = m[i%10000]
	}
}

func BenchmarkSwissMap_GetMiss(b *testing.B) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		b.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	sm := NewSwissMap[int, int](arena, 16384, FastHasherInt)
	for i := 0; i < 10000; i++ {
		sm.Put(i, i)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		sm.Get(i + 10000) // Guaranteed miss
	}
}

func BenchmarkSwissMap_StringKeys(b *testing.B) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		b.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	sm := NewSwissMap[string, int](arena, 1024, StringHasher)
	
	// Pre-generate keys to prevent string allocation overhead during benchmark
	keys := make([]string, 10000)
	for i := range keys {
		keys[i] = "key-" + strconv.Itoa(i)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		sm.Put(keys[i%10000], i)
	}
}

func BenchmarkSwissMap_Delete(b *testing.B) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		b.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	// Pre-allocate and populate the map for b.N keys outside the timer
	sm := NewSwissMap[int, int](arena, uint32(b.N), FastHasherInt)
	for i := 0; i < b.N; i++ {
		sm.Put(i, i)
	}

	b.ResetTimer()
	b.ReportAllocs()

	// Measure pure deletion performance
	for i := 0; i < b.N; i++ {
		sm.Delete(i)
	}
}

func BenchmarkStdMap_Delete(b *testing.B) {
	// Pre-allocate and populate the standard map for b.N keys
	m := make(map[int]int, b.N)
	for i := 0; i < b.N; i++ {
		m[i] = i
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		delete(m, i)
	}
}

func BenchmarkSwissMap_Range(b *testing.B) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		b.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()
	sm := NewSwissMap[int, int](arena, 8192, FastHasherInt)
	for i := 0; i < 5000; i++ {
		sm.Put(i, i)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var sum int
		sm.Range(func(k int, v int) bool {
			sum += v
			return true
		})
		_ = sum
	}
}

func BenchmarkStdMap_Range(b *testing.B) {
	m := make(map[int]int, 8192)
	for i := 0; i < 5000; i++ {
		m[i] = i
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var sum int
		for _, v := range m {
			sum += v
		}
		_ = sum
	}
}

func BenchmarkSwissMap_MixedWorkload(b *testing.B) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		b.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()
	sm := NewSwissMap[int, int](arena, 4096, FastHasherInt)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		key := i % 2000
		if i%3 == 0 {
			sm.Put(key, key)
		} else if i%5 == 0 {
			sm.Delete(key)
		} else {
			sm.Get(key)
		}
	}
}

// BenchmarkSwissMap_Range_HighlyFragmented measures iteration performance when 
// the map contains a vast majority of tombstones, punishing open-addressing designs.
func BenchmarkSwissMap_Range_HighlyFragmented(b *testing.B) {
	arena, err := NewArena[int](MaxArenaSize, -1)
	if err != nil {
		b.Fatalf("failed to init arena: %v", err)
	}
	defer arena.Close()

	sm := NewSwissMap[int, int](arena, 65536, FastHasherInt)
	
	// Insert 50,000 items
	for i := 0; i < 50000; i++ {
		sm.Put(i, i)
	}
	// Delete 49,000 of them, leaving a massive sparse array
	for i := 0; i < 49000; i++ {
		sm.Delete(i)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var sum int
		sm.Range(func(k int, v int) bool {
			sum += v
			return true
		})
		_ = sum
	}
}

func BenchmarkStdMap_Range_HighlyFragmented(b *testing.B) {
	m := make(map[int]int, 65536)
	
	for i := 0; i < 50000; i++ {
		m[i] = i
	}
	for i := 0; i < 49000; i++ {
		delete(m, i)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var sum int
		for _, v := range m {
			sum += v
		}
		_ = sum
	}
}