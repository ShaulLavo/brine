//go:build linux

package host

import (
	"context"
	"reflect"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

// absentRemovalAllocation is not a preparation expectation. The protected
// candidate proves allocation identity and no writer/initialization history;
// the retained source separately proves a complete empty directory census.
func (h archiveHost) absentRemovalAllocation(ctx context.Context, p archive.Plan, d archive.SQLiteDatabase) (*data.AllocationReceipt, error) {
	if d.Written || d.Allocation == nil {
		return nil, archive.ErrRefused
	}
	for _, ref := range p.Removal.Points {
		if ref.DatabaseID == d.Binding.DatabaseID {
			return nil, archive.ErrRefused
		}
	}
	o, claimed, err := h.service.Store.ReadArchiveOperation(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	current, _, err := h.archiveCandidate(ctx, p.Candidate.App, absentRemovalOwner(o, claimed))
	if err != nil || current.Incarnation != p.Candidate.Incarnation {
		return nil, archive.ErrRefused
	}
	for _, variant := range current.Databases {
		candidate, ok := variant.SQLite()
		if !ok || candidate.Binding.DatabaseID != d.Binding.DatabaseID {
			continue
		}
		if candidate.Written || candidate.Binding != d.Binding || !reflect.DeepEqual(candidate.Allocation, d.Allocation) {
			return nil, archive.ErrRefused
		}
		allocation := *candidate.Allocation
		return &allocation, nil
	}
	return nil, archive.ErrRefused
}
func absentRemovalOwner(o archive.Operation, claimed bool) string {
	if claimed {
		return o.ID
	}
	return ""
}

func qualifyAbsentRemovalSource(source *sqliteSource, allocation *data.AllocationReceipt) error {
	if allocation == nil || !source.matchesAllocation(*allocation) || source.emptyCensus() != nil {
		return errSQLiteSource
	}
	source.absentAllocation = allocation
	return nil
}
