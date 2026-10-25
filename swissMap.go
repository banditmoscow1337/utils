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
	ctrlChunkSize       = 1 << 18 // Several chunks fit in each 2 MiB region.
	ctrlChunkMask       = ctrlChunkSize - 1
)

type TableSlot64[K comparable, V any] struct {
	Key K
	Val V
}

// SwissMap stores control bytes in an Arena. Slots without Go pointers also
// live there. Tables larger than one region are split into arena allocations.
// Pointer-bearing slots live on the Go heap so the GC can trace their contents.
// Concurrent mutation is not supported.
type SwissMap[K comparable, V any] struct {
	arena    *Arena
	hasher   func(K) uint64
	offset   uint64 // Contiguous table offset, or zero for a segmented table.
	table    swissTable[K, V]
	onHeap   bool
	capacity uint32
	mask     uint32
	length   uint32
	dead     uint32 // Track tombstones to prevent infinite loops
	maxLen   uint32
}

// swissTable owns one contiguous allocation or separate control/slot chunks.
// The slices of chunk descriptors live on the Go heap; their data lives in the
// arena, except when the slots contain pointers and must remain GC-visible.
type swissTable[K comparable, V any] struct {
	offset      uint64
	ctrlOffsets []uint64
	ctrlChunks  [][]uint8
	slotOffsets []uint64
	slotChunks  [][]TableSlot64[K, V]
	heapSlots   []TableSlot64[K, V]
	slotShift   uint
	slotMask    uint32
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
		group := t.table.group(ctrl, i)

		// Valid H2 hashes have their MSB set to 0.
		// Empty (0x80) and Deleted (0xFE) have their MSB set to 1.
		// By negating the group and masking the MSBs, we get a bitmask
		// where ONLY live slots have a 1 in their high bit.
		liveMatches := (^group) & 0x8080808080808080

		for liveMatches != 0 {
			bitPos := bits.TrailingZeros64(liveMatches)
			slotIdx := i + uint32(bitPos>>3)

			slot := t.table.slot(slots, slotIdx)
			if !f(slot.Key, slot.Val) {
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
	if cap < 8 || cap&(cap-1) != 0 {
		panic("swissMap64: invalid capacity")
	}
	// A small table retains the original contiguous layout and its fast path.
	ctrlSize := uint64(cap) + 8
	ctrlAlign := (ctrlSize + 7) &^ uint64(7)
	slotSize := uint64(unsafe.Sizeof(TableSlot64[K, V]{}))
	maxBlock := uint64(RegionSize) - blockHeaderSize64
	if !t.onHeap && slotSize > maxBlock {
		panic("swissMap64: slot exceeds one arena region")
	}
	totalSize := ctrlAlign
	if !t.onHeap {
		totalSize += uint64(cap) * slotSize
	}
	table := swissTable[K, V]{}
	committed := false
	defer func() {
		if !committed {
			table.free(t.arena)
		}
	}()
	if t.onHeap {
		table.heapSlots = make([]TableSlot64[K, V], cap)
	}
	if totalSize <= maxBlock {
		table.offset = t.arena.AllocDense(totalSize)
	} else {
		for start := uint64(0); start < uint64(cap); start += ctrlChunkSize {
			n := min(uint64(ctrlChunkSize), uint64(cap)-start)
			off := t.arena.AllocDense(n + 8)
			table.ctrlOffsets = append(table.ctrlOffsets, off)
			table.ctrlChunks = append(table.ctrlChunks,
				unsafe.Slice((*uint8)(t.arena.GetPtr(off)), int(n+8)))
		}
		if !t.onHeap {
			budget := maxBlock
			if slotSize <= RegionSize/4 {
				budget = RegionSize / 4
			}
			slotsPerChunk := uint32(1) << (bits.Len64(budget/slotSize) - 1)
			table.slotShift = uint(bits.TrailingZeros32(slotsPerChunk))
			table.slotMask = slotsPerChunk - 1
			for start := uint32(0); start < cap; start += slotsPerChunk {
				n := min(slotsPerChunk, cap-start)
				off := t.arena.AllocDense(uint64(n) * slotSize)
				table.slotOffsets = append(table.slotOffsets, off)
				table.slotChunks = append(table.slotChunks,
					unsafe.Slice((*TableSlot64[K, V])(t.arena.GetPtr(off)), int(n)))
			}
		}
	}
	t.offset = table.offset
	t.table = table
	t.capacity = cap
	t.mask = cap - 1
	t.maxLen = uint32(float32(cap) * loadFactor64)

	ctrl := t.ctrl()
	if ctrl != nil {
		for i := range ctrl {
			ctrl[i] = ctrlEmpty64
		}
	} else {
		for _, chunk := range t.table.ctrlChunks {
			for i := range chunk {
				chunk[i] = ctrlEmpty64
			}
		}
	}
	committed = true
}

func (t *SwissMap[K, V]) ctrl() []uint8 {
	if t.offset == 0 {
		return nil
	}
	ptr := t.arena.GetPtr(t.offset)
	return unsafe.Slice((*uint8)(ptr), t.capacity+8)
}

func (t *SwissMap[K, V]) slots() []TableSlot64[K, V] {
	if t.table.heapSlots != nil {
		return t.table.heapSlots
	}
	if t.offset == 0 {
		return nil
	}
	ctrlSize := uint64(t.capacity) + 8
	ctrlAlign := (ctrlSize + 7) &^ uint64(7)
	ptr := unsafe.Add(t.arena.GetPtr(t.offset), uintptr(ctrlAlign))
	return unsafe.Slice((*TableSlot64[K, V])(ptr), t.capacity)
}

// The extra eight bytes in each control chunk mirror the next chunk's first
// eight bytes. For the final chunk, they mirror the beginning of the table.
func (s *swissTable[K, V]) group(ctrl []uint8, idx uint32) uint64 {
	if ctrl != nil {
		return *(*uint64)(unsafe.Pointer(&ctrl[idx]))
	}
	chunk := s.ctrlChunks[idx/ctrlChunkSize]
	return *(*uint64)(unsafe.Pointer(&chunk[idx&ctrlChunkMask]))
}

func (s *swissTable[K, V]) control(ctrl []uint8, idx uint32) uint8 {
	if ctrl != nil {
		return ctrl[idx]
	}
	return s.ctrlChunks[idx/ctrlChunkSize][idx&ctrlChunkMask]
}

func (s *swissTable[K, V]) setControl(ctrl []uint8, cap, idx uint32, value uint8) {
	if ctrl != nil {
		ctrl[idx] = value
		if idx < 8 {
			ctrl[cap+idx] = value
		}
		return
	}
	chunkIdx := idx / ctrlChunkSize
	pos := idx & ctrlChunkMask
	s.ctrlChunks[chunkIdx][pos] = value
	if pos < 8 {
		previous := (int(chunkIdx) + len(s.ctrlChunks) - 1) % len(s.ctrlChunks)
		end := min(uint32(ctrlChunkSize), cap-uint32(previous)*ctrlChunkSize)
		s.ctrlChunks[previous][end+pos] = value
	}
}

func (s *swissTable[K, V]) slot(slots []TableSlot64[K, V], idx uint32) *TableSlot64[K, V] {
	if slots != nil {
		return &slots[idx]
	}
	return &s.slotChunks[idx>>s.slotShift][idx&s.slotMask]
}

func (s *swissTable[K, V]) free(arena *Arena) {
	if s.offset != 0 {
		arena.Free(s.offset)
	}
	for _, off := range s.ctrlOffsets {
		arena.Free(off)
	}
	for _, off := range s.slotOffsets {
		arena.Free(off)
	}
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
		group := t.table.group(ctrl, idx)
		matches := matchH2(group, h2)
		for matches != 0 {
			bitPos := bits.TrailingZeros64(matches)
			slotIdx := (idx + uint32(bitPos>>3)) & t.mask
			slot := t.table.slot(slots, slotIdx)
			if slot.Key == key {
				return slot.Val, true
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

		group := t.table.group(ctrl, idx)
		matches := matchH2(group, h2)

		for matches != 0 {
			bitPos := bits.TrailingZeros64(matches)
			slotIdx := (idx + uint32(bitPos>>3)) & t.mask
			// Key found: Update the value and return immediately.
			slot := t.table.slot(slots, slotIdx)
			if slot.Key == key {
				slot.Val = val
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
			if t.capacity >= 1<<31 {
				panic("swissMap64: capacity overflow")
			}
			t.rehash(t.capacity * 2) // Grow capacity
		}

		// The map layout has changed, rendering targetIdx invalid.
		// Jump back to re-evaluate the insertion against the new layout.
		goto retry
	}

	// Safe to execute insertion at the identified target index.
	isTombstone := t.table.control(ctrl, targetIdx) == ctrlDeleted64
	slot := t.table.slot(slots, targetIdx)
	slot.Key = key
	slot.Val = val
	t.table.setControl(ctrl, t.capacity, targetIdx, h2)

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
		group := t.table.group(ctrl, idx)
		matches := matchH2(group, h2)
		for matches != 0 {
			bitPos := bits.TrailingZeros64(matches)
			slotIdx := (idx + uint32(bitPos>>3)) & t.mask
			slot := t.table.slot(slots, slotIdx)
			if slot.Key == key {
				t.table.setControl(ctrl, t.capacity, slotIdx, ctrlDeleted64)
				var zeroKey K
				var zeroVal V
				slot.Key = zeroKey
				slot.Val = zeroVal
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
	if t.capacity != 0 {
		t.table.free(t.arena)
		t.table = swissTable[K, V]{}
		t.offset = 0
		t.capacity = 0
		t.mask = 0
		t.maxLen = 0
		t.length = 0
		t.dead = 0
	}
}

func (t *SwissMap[K, V]) rehash(newCap uint32) {
	if newCap == t.capacity {
		// An empty map can discard all tombstones without allocating.
		if t.length == 0 {
			ctrl := t.ctrl()
			if ctrl != nil {
				for i := range ctrl {
					ctrl[i] = ctrlEmpty64
				}
			} else {
				for _, chunk := range t.table.ctrlChunks {
					for i := range chunk {
						chunk[i] = ctrlEmpty64
					}
				}
			}
			slots := t.slots()
			if slots != nil {
				clear(slots)
			} else {
				for _, chunk := range t.table.slotChunks {
					clear(chunk)
				}
			}
			t.dead = 0
			return
		}
	}
	if newCap == t.capacity && t.table.offset != 0 {
		// Preserve the original small-table compaction path.
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

	// Growth and large-table compaction use a fresh table. The old table
	// remains leased until its last entry has been reinserted.
	oldTable := t.table
	oldCtrl := t.ctrl()
	oldSlots := t.slots()
	oldCap := t.capacity

	t.init(newCap)
	t.length = 0
	t.dead = 0

	for i := uint32(0); i < oldCap; i++ {
		c := oldTable.control(oldCtrl, i)
		if c != ctrlEmpty64 && c != ctrlDeleted64 {
			slot := oldTable.slot(oldSlots, i)
			t.Put(slot.Key, slot.Val)
		}
	}
	oldTable.free(t.arena)
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
