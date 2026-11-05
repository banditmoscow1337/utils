package utils

// PayloadCapacity returns the usable bytes in an owned allocation, including
// size-class slack, or zero for the nil offset. offset must be a payload start
// returned by this Arena, not an interior pointer. The caller must retain its
// claim throughout the call. This checks bounds and the header, not liveness,
// ownership by another Arena, or the provenance of arbitrary forged offsets.
func (a *Arena) PayloadCapacity(offset uint64) uint64 {
	if offset == 0 {
		return 0
	}
	if a.basePtr == nil || offset >= a.size || offset&7 != 0 || offset < a.dataStart+HeaderSize {
		panic("arena: invalid payload offset")
	}
	h := (*BlockHeader)(a.GetPtr(offset - HeaderSize))
	start := offset - HeaderSize
	size := h.Size
	if size < HeaderSize || size&7 != 0 || size > a.regionEnd(start/RegionSize)-start {
		panic("arena: invalid payload size")
	}
	return size - HeaderSize
}
