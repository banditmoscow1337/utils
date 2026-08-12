//go:build amd64 || arm64 || riscv64

package utils

import (
	"math/bits"
	"unsafe"
)

const (
	ctrlEmpty   uint8 = 0b10000000 // 0x80
	ctrlDeleted uint8 = 0b11111110 // 0xFE
	loadFactor        = 0.875      // 7/8 capacity threshold
)

type TableSlot[K comparable, V any] struct {
	Key K
	Val V
}

type SwissMap[K comparable, V any] struct {
	arena    *Arena
	hasher   func(K) uint64
	offset   uint32
	capacity uint32
	mask     uint32
	length   uint32
	dead     uint32 // Track tombstones to prevent infinite loops
	maxLen   uint32
}

func NewSwissMap[K comparable, V any](a *Arena, initialCap uint32, hasher func(K) uint64) *SwissMap[K, V] {
	if initialCap < 8 {
		initialCap = 8
	}
	// Round up to next power of 2
	initialCap = 1 << (32 - bits.LeadingZeros32(initialCap-1))

	t := &SwissMap[K, V]{
		arena:  a,
		hasher: hasher,
	}
	t.init(initialCap)
	return t
}

func (t *SwissMap[K, V]) Range(f func(key K, val V) bool) {
	if t.capacity == 0 {
		return
	}
	
	ctrl := t.ctrl()
	slots := t.slots()

	// t.capacity is strictly a power of 2 and minimally 8.
	// We can safely step by 8 bytes without bounds issues, 
	// especially since ctrl has the +8 mirror padding.
	for i := uint32(0); i < t.capacity; i += 8 {
		group := *(*uint64)(unsafe.Pointer(&ctrl[i]))
		
		// Valid H2 hashes have their MSB set to 0. 
		// Empty (0x80) and Deleted (0xFE) have their MSB set to 1.
		// By negating the group and masking the MSBs, we get a bitmask 
		// where ONLY live slots have a 1 in their high bit.
		liveMatches := (^group) & 0x8080808080808080

		for liveMatches != 0 {
			bitPos := bits.TrailingZeros64(liveMatches)
			slotIdx := i + uint32(bitPos>>3)

			if !f(slots[slotIdx].Key, slots[slotIdx].Val) {
				return
			}

			liveMatches &= liveMatches - 1 // Clear the lowest set bit
		}
	}
}

func (t *SwissMap[K, V]) init(cap uint32) {
	if t.arena == nil {
		panic("swissMap: uninitialized map with nil arena")
	}
	t.capacity = cap
	t.mask = cap - 1
	t.maxLen = uint32(float32(cap) * loadFactor)

	// Layout: [ctrl array (cap + 8)] + [padding] + [slots array (cap * sizeof(slot))]
	ctrlSize := cap + 8
	ctrlAlign := (ctrlSize + 7) &^ 7
	slotSize := uint32(unsafe.Sizeof(TableSlot[K, V]{}))
	totalSize := ctrlAlign + (cap * slotSize)

	// Ensure allocations stay entirely inside the arena, avoiding Go map bucket overhead
	t.offset = t.arena.AllocDense(totalSize)

	ctrl := t.ctrl()
	for i := range ctrl {
		ctrl[i] = ctrlEmpty
	}
}

func (t *SwissMap[K, V]) ctrl() []uint8 {
	ptr := t.arena.GetPtr(t.offset)
	return unsafe.Slice((*uint8)(ptr), t.capacity+8)
}

func (t *SwissMap[K, V]) slots() []TableSlot[K, V] {
	ctrlSize := t.capacity + 8
	ctrlAlign := (ctrlSize + 7) &^ 7
	ptr := unsafe.Add(t.arena.GetPtr(t.offset), ctrlAlign)
	return unsafe.Slice((*TableSlot[K, V])(ptr), t.capacity)
}

func (t *SwissMap[K, V]) Get(key K) (V, bool) {
	if t.capacity == 0 {
		var zero V
		return zero, false
	}

	h := t.hasher(key)
	h1 := uint32(h >> 7)
	h2 := uint8(h & 0x7F)

	ctrl := t.ctrl()
	slots := t.slots()
	idx := h1 & t.mask
	probe := uint32(1)

	for {
		if probe > t.capacity {
			panic("swissMap: infinite probe loop detected in Get")
		}
		group := *(*uint64)(unsafe.Pointer(&ctrl[idx]))
		matches := matchH2(group, h2)
		for matches != 0 {
			bitPos := bits.TrailingZeros64(matches)
			slotIdx := (idx + uint32(bitPos>>3)) & t.mask
			if slots[slotIdx].Key == key {
				return slots[slotIdx].Val, true
			}
			matches &= matches - 1 // Clear lowest set bit
		}

		if matchEmpty(group) != 0 {
			var zero V
			return zero, false
		}

		idx = (idx + probe) & t.mask
		probe++
	}
}

func (t *SwissMap[K, V]) Put(key K, val V) {
	if t.capacity == 0 {
		t.init(8)
	}

	h := t.hasher(key)
	h1 := uint32(h >> 7)
	h2 := uint8(h & 0x7F)

retry:
	ctrl := t.ctrl()
	slots := t.slots()
	idx := h1 & t.mask
	probe := uint32(1)
	var targetIdx uint32 = 0xFFFFFFFF

	for {
		if probe > t.capacity {
			panic("swissMap: infinite probe loop detected in Put")
		}

		group := *(*uint64)(unsafe.Pointer(&ctrl[idx]))
		matches := matchH2(group, h2)

		for matches != 0 {
			bitPos := bits.TrailingZeros64(matches)
			slotIdx := (idx + uint32(bitPos>>3)) & t.mask
			// Key found: Update the value and return immediately.
			if slots[slotIdx].Key == key {
				slots[slotIdx].Val = val
				return
			}
			matches &= matches - 1
		}

		if targetIdx == 0xFFFFFFFF {
			emptyOrDel := matchEmptyOrDeleted(group)
			if emptyOrDel != 0 {
				bitPos := bits.TrailingZeros64(emptyOrDel)
				targetIdx = (idx + uint32(bitPos>>3)) & t.mask
			}
		}

		if matchEmpty(group) != 0 {
			break // End of probe chain
		}

		idx = (idx + probe) & t.mask
		probe++
	}

	// Key was not found. This is a new insertion.
	// Validate the load factor before claiming the slot.
	if t.length+t.dead >= t.maxLen {
		if t.length <= t.maxLen/2 {
			t.rehash(t.capacity) // Clean tombstones in-place
		} else {
			t.rehash(t.capacity * 2) // Grow capacity
		}

		// The map layout has changed, rendering targetIdx invalid.
		// Jump back to re-evaluate the insertion against the new layout.
		goto retry
	}

	// Safe to execute insertion at the identified target index.
	isTombstone := ctrl[targetIdx] == ctrlDeleted
	slots[targetIdx].Key = key
	slots[targetIdx].Val = val
	ctrl[targetIdx] = h2

	if targetIdx < 8 {
		ctrl[t.capacity+targetIdx] = h2 // Mirror wrap
	}

	t.length++
	if isTombstone {
		t.dead--
	}
}

func (t *SwissMap[K, V]) Delete(key K) {
	if t.capacity == 0 {
		return
	}
	h := t.hasher(key)
	h1 := uint32(h >> 7)
	h2 := uint8(h & 0x7F)

	ctrl := t.ctrl()
	slots := t.slots()
	idx := h1 & t.mask
	probe := uint32(1)

	for {
		if probe > t.capacity {
			panic("swissMap: infinite probe loop detected in Delete")
		}
		group := *(*uint64)(unsafe.Pointer(&ctrl[idx]))
		matches := matchH2(group, h2)
		for matches != 0 {
			bitPos := bits.TrailingZeros64(matches)
			slotIdx := (idx + uint32(bitPos>>3)) & t.mask
			if slots[slotIdx].Key == key {
				ctrl[slotIdx] = ctrlDeleted
				if slotIdx < 8 {
					ctrl[t.capacity+slotIdx] = ctrlDeleted
				}
				var zeroKey K
				var zeroVal V
				slots[slotIdx].Key = zeroKey
				slots[slotIdx].Val = zeroVal
				t.length--
				t.dead++
				return
			}
			matches &= matches - 1
		}

		if matchEmpty(group) != 0 {
			return
		}

		idx = (idx + probe) & t.mask
		probe++
	}
}

// Close releases the backing control and slot allocations back to the arena.
func (t *SwissMap[K, V]) Close() {
	if t.offset != 0 {
		t.arena.Free(t.offset)
		t.offset = 0
		t.capacity = 0
		t.length = 0
		t.dead = 0
	}
}

func (t *SwissMap[K, V]) rehash(newCap uint32) {
	if newCap == t.capacity {
		// Fast path: if the map only contains tombstones, just wipe it without allocating.
		if t.length == 0 {
			ctrl := t.ctrl()
			for i := range ctrl {
				ctrl[i] = ctrlEmpty
			}
			slots := t.slots()
			for i := range slots {
				var zeroKey K
				var zeroVal V
				slots[i].Key = zeroKey
				slots[i].Val = zeroVal
			}
			t.dead = 0
			return
		}

		// Allocate a contiguous block from the Arena, completely bypassing the Go heap.
		slotSize := uint32(unsafe.Sizeof(TableSlot[K, V]{}))
		tempOff := t.arena.AllocDense(t.length * slotSize)
		tempSlots := unsafe.Slice((*TableSlot[K, V])(t.arena.GetPtr(tempOff)), t.length)

		ctrl := t.ctrl()
		slots := t.slots()
		var writeIdx uint32

		for i := uint32(0); i < t.capacity; i++ {
			c := ctrl[i]
			if c != ctrlEmpty && c != ctrlDeleted {
				tempSlots[writeIdx] = slots[i]
				writeIdx++
			}
		}

		// Wipe the current struct to prep for pure re-insertion.
		for i := range ctrl {
			ctrl[i] = ctrlEmpty
		}
		for i := range slots {
			var zeroKey K
			var zeroVal V
			slots[i].Key = zeroKey
			slots[i].Val = zeroVal
		}

		t.length = 0
		t.dead = 0

		for i := uint32(0); i < writeIdx; i++ {
			t.Put(tempSlots[i].Key, tempSlots[i].Val)
		}

		// Return the contiguous block to the arena immediately.
		t.arena.Free(tempOff)
		return
	}

	// Capacity growth branch
	oldCtrl := t.ctrl()
	oldSlots := t.slots()
	oldCap := t.capacity
	oldOffset := t.offset

	t.init(newCap)
	t.length = 0
	t.dead = 0

	for i := uint32(0); i < oldCap; i++ {
		c := oldCtrl[i]
		if c != ctrlEmpty && c != ctrlDeleted {
			t.Put(oldSlots[i].Key, oldSlots[i].Val)
		}
	}

	if oldOffset != 0 {
		t.arena.Free(oldOffset)
	}
}

// matchH2 finds bytes equal to h2. Returns uint64 with MSB set for matching bytes.
func matchH2(group uint64, h2 uint8) uint64 {
	x := group ^ (uint64(h2) * 0x0101010101010101)
	return (x - 0x0101010101010101) &^ x & 0x8080808080808080
}

// matchEmpty strictly isolates bytes equal to 0x80.
func matchEmpty(group uint64) uint64 {
	x := group ^ 0x8080808080808080
	return (x - 0x0101010101010101) &^ x & 0x8080808080808080
}

// matchEmptyOrDeleted catches any byte with the MSB set (0x80 or 0xFE).
func matchEmptyOrDeleted(group uint64) uint64 {
	return group & 0x8080808080808080
}