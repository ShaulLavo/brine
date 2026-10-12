package archive

import "github.com/ShaulLavo/brine/internal/data"

func cloneSQLiteCoverage(c SQLiteCoverage) SQLiteCoverage {
	c.Empty = cloneAllocation(c.Empty)
	if c.Point != nil {
		point := *c.Point
		c.Point = &point
		cloneCoveragePoint(c.Point)
	}
	if c.Receipt != nil {
		receipt := c.Receipt.Clone()
		c.Receipt = &receipt
	}
	return c
}

func cloneCoveragePoint(point *data.SQLiteRestorePoint) {
	if point.LTX != nil {
		ltx := *point.LTX
		point.LTX = &ltx
	}
	if point.Snapshot != nil {
		snapshot := *point.Snapshot
		point.Snapshot = &snapshot
	}
}
