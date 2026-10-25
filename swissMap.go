//go:build amd64 || arm64 || riscv64

package utils

import (
	"math/bits"
	"reflect"
	"unsafe"
)

const (
	ctrlEmpty64   uint8 = 0b10000000
	ctrlDeleted64 uint8 = 0b11111110
	loadFactor64        = 0.875
)

type TableSlot64[K comparable, V any] struct {
	Key K
	Val V
}

// SwissMap stores control bytes in an Arena64. Slots without Go pointers
// also live there. Pointer-bearing slots live on the Go heap so the GC can
// trace their contents. Mapped tables still fit within one arena region.
// Concurrent mutation is not supported.
type SwissMap[K comparable, V any] struct {
	arena     *Arena
	hasher    func(K) uint64
	offset    uint64
	heapSlots []TableSlot64[K, V]
	onHeap    bool
	capacity  uint32
	mask      uint32
	length    uint32
	dead      uint32 // Track tombstones to prevent infinite loops
	maxLen    uint32
}

// NewSwissMap constructs a map backed by a 64-bit arena.
func NewSwissMap[K comparable, V any](a *Arena, initialCap uint32, hasher func(K) uint64) *SwissMap[K, V] {
	if initialCap < 8 {
		initialCap = 8
	}
	// The mapped control array and pointer-free slots fit in one region.
	// Avoid overflowing uint32 while rounding up to a power of two.
	if initialCap > 1<<31 {
		panic("swissMap64: capacity overflow")
	}
	initialCap = 1 << (32 - bits.LeadingZeros32(initialCap-1))

	t := &SwissMap[K, V]{
		arena:  a,
		hasher: hasher,
		onHeap: unsafe.Sizeof(TableSlot64[K, V]{}) == 0 ||
			swissHasPointers(reflect.TypeFor[K]()) || swissHasPointers(reflect.TypeFor[V]()),
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
		panic("swissMap64: uninitialized map with nil arena")
	}
	// Layout: [ctrl array (cap + 8)] + [padding] + [pointer-free slots].
	ctrlSize := uint64(cap) + 8
	ctrlAlign := (ctrlSize + 7) &^ uint64(7)
	slotSize := uint64(unsafe.Sizeof(TableSlot64[K, V]{}))
	totalSize := ctrlAlign
	if !t.onHeap {
		totalSize += uint64(cap) * slotSize
	}
	if totalSize > RegionSize-blockHeaderSize64 {
		panic("swissMap64: table exceeds one arena region")
	}
	var heapSlots []TableSlot64[K, V]
	if t.onHeap {
		heapSlots = make([]TableSlot64[K, V], cap)
	}

	newOffset := t.arena.AllocDense(totalSize)
	t.offset = newOffset
	t.heapSlots = heapSlots
	t.capacity = cap
	t.mask = cap - 1
	t.maxLen = uint32(float32(cap) * loadFactor64)

	ctrl := t.ctrl()
	for i := range ctrl {
		ctrl[i] = ctrlEmpty64
	}
}

func (t *SwissMap[K, V]) ctrl() []uint8 {
	ptr := t.arena.GetPtr(t.offset)
	return unsafe.Slice((*uint8)(ptr), t.capacity+8)
}

func (t *SwissMap[K, V]) slots() []TableSlot64[K, V] {
	if t.onHeap {
		return t.heapSlots
	}
	ctrlSize := uint64(t.capacity) + 8
	ctrlAlign := (ctrlSize + 7) &^ uint64(7)
	ptr := unsafe.Add(t.arena.GetPtr(t.offset), uintptr(ctrlAlign))
	return unsafe.Slice((*TableSlot64[K, V])(ptr), t.capacity)
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
			panic("swissMap64: infinite probe loop detected in Get")
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
			panic("swissMap64: infinite probe loop detected in Put")
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
	isTombstone := ctrl[targetIdx] == ctrlDeleted64
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
			panic("swissMap64: infinite probe loop detected in Delete")
		}
		group := *(*uint64)(unsafe.Pointer(&ctrl[idx]))
		matches := matchH2(group, h2)
		for matches != 0 {
			bitPos := bits.TrailingZeros64(matches)
			slotIdx := (idx + uint32(bitPos>>3)) & t.mask
			if slots[slotIdx].Key == key {
				ctrl[slotIdx] = ctrlDeleted64
				if slotIdx < 8 {
					ctrl[t.capacity+slotIdx] = ctrlDeleted64
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

// Close releases the mapped table and any heap slots.
func (t *SwissMap[K, V]) Close() {
	if t.offset != 0 {
		t.arena.Free(t.offset)
		t.offset = 0
		t.heapSlots = nil
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
				ctrl[i] = ctrlEmpty64
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

		var tempOff uint64
		var tempSlots []TableSlot64[K, V]
		if t.onHeap {
			tempSlots = make([]TableSlot64[K, V], t.length)
		} else {
			slotSize := uint64(unsafe.Sizeof(TableSlot64[K, V]{}))
			tempOff = t.arena.AllocDense(uint64(t.length) * slotSize)
			tempSlots = unsafe.Slice((*TableSlot64[K, V])(t.arena.GetPtr(tempOff)), t.length)
		}

		ctrl := t.ctrl()
		slots := t.slots()
		var writeIdx uint32

		for i := uint32(0); i < t.capacity; i++ {
			c := ctrl[i]
			if c != ctrlEmpty64 && c != ctrlDeleted64 {
				tempSlots[writeIdx] = slots[i]
				writeIdx++
			}
		}

		// Wipe the current struct to prep for pure re-insertion.
		for i := range ctrl {
			ctrl[i] = ctrlEmpty64
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

		if tempOff != 0 {
			t.arena.Free(tempOff)
		}
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
		if c != ctrlEmpty64 && c != ctrlDeleted64 {
			t.Put(oldSlots[i].Key, oldSlots[i].Val)
		}
	}

	if oldOffset != 0 {
		t.arena.Free(oldOffset)
	}
}

func swissHasPointers(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Array:
		return swissHasPointers(t.Elem())
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if swissHasPointers(t.Field(i).Type) {
				return true
			}
		}
		return false
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr, reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return false
	default:
		return true
	}
}

func matchH2(group uint64, h2 uint8) uint64 {
	x := group ^ (uint64(h2) * 0x0101010101010101)
	return (x - 0x0101010101010101) &^ x & 0x8080808080808080
}

func matchEmpty(group uint64) uint64 {
	x := group ^ 0x8080808080808080
	return (x - 0x0101010101010101) &^ x & 0x8080808080808080
}

func matchEmptyOrDeleted(group uint64) uint64 {
	return group & 0x8080808080808080
}
