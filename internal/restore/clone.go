package restore

import "slices"

func cloneSQLiteSource(s SQLiteSource) SQLiteSource {
	if s.Snapshot != nil {
		copy := *s.Snapshot
		s.Snapshot = &copy
	}
	if s.LTX != nil {
		copy := *s.LTX
		copy.Barrier = cloneBarrier(copy.Barrier)
		s.LTX = &copy
	}
	return s
}

func cloneBarrier(b *BarrierReceipt) *BarrierReceipt {
	if b == nil {
		return nil
	}
	copy := *b
	return &copy
}

func cloneSentinel(s *Sentinel) *Sentinel {
	if s == nil {
		return nil
	}
	copy := *s
	return &copy
}

func cloneCoverage(c *CoverageEvidence) *CoverageEvidence {
	if c == nil {
		return nil
	}
	copy := *c
	copy.Source = cloneSQLiteSource(copy.Source)
	return &copy
}

func cloneSQLiteRequest(r SQLiteRequest) SQLiteRequest {
	r.Source = cloneSQLiteSource(r.Source)
	r.AcceptedSchemas = slices.Clone(r.AcceptedSchemas)
	r.Invariants = slices.Clone(r.Invariants)
	r.Sentinel = cloneSentinel(r.Sentinel)
	r.Coverage = cloneCoverage(r.Coverage)
	return r
}

func cloneSQLiteReceipt(r SQLiteReceipt) SQLiteReceipt {
	r.Source = cloneSQLiteSource(r.Source)
	r.Sentinel = cloneSentinel(r.Sentinel)
	r.Barrier = cloneBarrier(r.Barrier)
	r.Coverage = cloneCoverage(r.Coverage)
	return r
}

func (r SQLiteReceipt) Clone() SQLiteReceipt { return cloneSQLiteReceipt(r) }
