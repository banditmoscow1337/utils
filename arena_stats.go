package utils

import "sync/atomic"

// Stats is a diagnostic snapshot, not a synchronization or admission-control
// mechanism. Concurrent allocations can change counters between reads. At a
// quiescent point after Trim, ClaimedBytes/ActiveClaims exclude all TLAB reserves.
// Byte counts include block headers and class rounding, not just user payloads.
type Stats struct {
	MappingBytes        uint64
	MetadataBytes       uint64
	HighWaterMark       uint64
	ClaimedBytes        int64 // live blocks plus unconsumed TLAB reservations
	ActiveClaims        int64 // includes conservative TLAB claims before Trim
	FreeListBytes       uint64
	ReusableRegionBytes uint64
	ReusableRegions     uint64
	TailBytes           uint64 // unreserved tails in initialized nonempty regions
	StrandedBytes       uint64 // refunded TLAB tails/exact blocks, until region reset
	VirginBytes         uint64 // data in regions not yet initialized
	RegisteredTLABs     uint64
	FreeBlocksByClass   [SizeClassCount]uint64
	// These cumulative counters belong to this Arena handle, not the file format.
	RegionReuses, PurgeCalls, PurgedBytes, PurgeErrors, OOMs uint64
}

// Stats takes the slow reservation mutex and short region/free-list leases.
// It walks free chains while holding their locks; keep it off query paths.
// Physical RSS must be measured separately.
func (a *Arena) Stats() Stats {
	a.reserveMu.Lock()
	defer a.reserveMu.Unlock()
	s := Stats{MappingBytes: a.size, MetadataBytes: a.dataStart}
	if a.meta == nil {
		return s
	}
	s.HighWaterMark = atomic.LoadUint64(&a.meta.bumpOffset)
	for idx := a.dataStart / RegionSize; idx < a.meta.nextRegion; idx++ {
		r := &a.regions[idx]
		r.enter()
		claimed := int64(r.liveBytes.Load())
		s.ClaimedBytes += claimed
		s.ActiveClaims += int64(r.activeCount.Load())
		free := uint64(0)
		for class := range r.freeHeads {
			head := lockHead64(&r.freeHeads[class])
			n:=uint64(0)
			for off:=head;off!=0; { n++;off=(*BlockHeader)(a.ptr(off-HeaderSize)).NextFree }
			s.FreeBlocksByClass[class] += n
			free += n * sizeClasses[class]
			unlockHead64(&r.freeHeads[class], head)
		}
		s.FreeListBytes += free
		if r.flags&regionReusable != 0 {
			s.ReusableRegions++
			s.ReusableRegionBytes += a.regionEnd(idx) - a.regionStart(idx)
		} else {
			s.TailBytes += a.regionEnd(idx) - r.cursor
			used := int64(r.cursor - a.regionStart(idx))
			if lost := used - claimed - int64(free); lost > 0 {
				s.StrandedBytes += uint64(lost)
			}
		}
		// A concurrent final Free waits for reserveMu before recycling; avoid
		// recursively acquiring that mutex through leaveRegion here.
		r.users.Add(-1)
	}
	if start := max(a.meta.nextRegion*RegionSize, a.dataStart); start < a.size {
		s.VirginBytes = a.size - start
	}
	for t := a.tlabHead.Load(); t != nil; t = t.next {
		s.RegisteredTLABs++
	}
	s.RegionReuses = a.regionReuses.Load()
	s.PurgeCalls, s.PurgedBytes = a.purgeCalls.Load(), a.purgedBytes.Load()
	s.PurgeErrors, s.OOMs = a.purgeErrors.Load(), a.ooms.Load()
	return s
}
