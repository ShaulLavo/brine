package archive

import (
	"path"
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// Admits checks the structural direct-continuation evidence. Only the store can
// admit the genuine consumer against protected preparation and current scope.
func (c PreparedCoverage) Admits(p Plan, database SQLiteDatabase, coverage SQLiteCoverage, now time.Time) bool {
	if p.Removal == nil || !p.Valid() || !removalTime(c.ClaimedAt) || !removalTime(now) || c.ClaimedAt.Before(p.CreatedAt) || !c.ClaimedAt.Before(p.Removal.ExpiresAt) || !c.ClaimedAt.Before(c.Ref.ExpiresAt) || now.Before(c.ClaimedAt) || now.Sub(c.ClaimedAt) > MaxEvidenceAge || now.Sub(c.ClaimedAt) > time.Duration(p.Removal.Bounds.RecoveryWindowSeconds)*time.Second {
		return false
	}
	if coverage.Prepared == nil || *coverage.Prepared != c || !c.matchesPlan(p, database) || coverage.DatabaseID != c.Ref.DatabaseID || !coverage.WritersQuiescedAt.Equal(c.Ref.Cutoff) || coverage.Empty != nil || coverage.Point == nil || coverage.Receipt == nil {
		return false
	}
	point, receipt := coverage.Point, coverage.Receipt
	if point.Validate() != nil || point.ID != c.Ref.SourcePointID || point.Schema != coverage.Observation || point.Schema.DatabaseID != c.Ref.DatabaseID || point.BindingID != database.Binding.ReplicaBindingID || point.EpochID != database.Epochs[0].EpochID || !removalTime(point.RecordedAt) || point.RecordedAt.Before(c.Ref.Cutoff) || point.RecordedAt.After(c.ClaimedAt) || !removalTime(point.Schema.ObservedAt) || point.Schema.ObservedAt.After(c.ClaimedAt) {
		return false
	}
	if !receipt.Valid() || !nativeArchiveOperationID.MatchString(receipt.OperationID) || receipt.DatabasePath != "" || receipt.Sentinel != nil || !removalTime(receipt.ObservedAt) || receipt.ObservedAt.Before(point.RecordedAt) || receipt.ObservedAt.After(c.ClaimedAt) || c.ClaimedAt.Sub(c.Ref.Cutoff) > time.Duration(p.Removal.Bounds.MaxBackupAgeSeconds)*time.Second || c.ClaimedAt.Sub(receipt.ObservedAt) > time.Duration(p.Removal.Bounds.MaxRestoreTestAgeSeconds)*time.Second || receipt.Coverage == nil || !receipt.Coverage.CoveredThrough.Equal(c.Ref.Cutoff) || receipt.LossWindow.State != restore.LossBounded {
		return false
	}
	expected := restore.SQLiteSchemaObservation{State: restore.SchemaState(point.Schema.State), Marker: point.Schema.Marker, CatalogSHA256: point.Schema.CatalogSHA256}
	source := (sqliteprep.RemoteSource{Point: *point}).RestoreSource()
	if receipt.Schema != expected || !reflect.DeepEqual(receipt.Source, source) || !reflect.DeepEqual(receipt.Coverage.Source, source) || len(database.Invariants) > 0 && receipt.InvariantCheck != "passed" {
		return false
	}
	if point.Snapshot != nil {
		return point.Snapshot.ObjectKey == path.Join(database.Epochs[0].RemotePrefix, "restore-points", point.ID, "snapshot.sqlite")
	}
	return !point.LTX.Barrier.ObservedAt.Before(c.Ref.Cutoff)
}

func (c PreparedCoverage) matchesPlan(p Plan, database SQLiteDatabase) bool {
	if !c.Ref.valid() || database.Binding.DatabaseID != c.Ref.DatabaseID || len(database.Epochs) != 1 {
		return false
	}
	if !preparedDatabaseMatches(p, database) {
		return false
	}
	for _, ref := range p.Removal.Points {
		if ref.DatabaseID == c.Ref.DatabaseID {
			return ref == c.Ref
		}
	}
	return false
}

func preparedDatabaseMatches(p Plan, database SQLiteDatabase) bool {
	for _, variant := range p.Candidate.Databases {
		candidate, ok := variant.SQLite()
		if ok && candidate.Binding.DatabaseID == database.Binding.DatabaseID {
			return reflect.DeepEqual(candidate, database)
		}
	}
	return false
}
