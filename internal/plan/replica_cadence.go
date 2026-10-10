package plan

import (
	"reflect"
	"slices"

	"github.com/ShaulLavo/brine/internal/policy"
)

// CadenceOnly distinguishes a replica-only desired change from any app change.
func CadenceOnly(next, previous policy.Desired) bool {
	if next.Backup == nil || previous.Backup == nil || len(next.Databases) == 0 || len(next.Databases) != len(previous.Databases) {
		return false
	}
	normalized := next
	// Snapshot cadence is operator-policy input. Fresh normalization and frozen
	// credential evidence still admit the new policy; its identity is metadata,
	// not a reason to restart an otherwise unchanged application.
	normalized.PolicyHash = previous.PolicyHash
	normalized.PolicyVersion = previous.PolicyVersion
	normalized.Databases = slices.Clone(next.Databases)
	backup := *next.Backup
	normalized.Backup = &backup
	changed := backup.SnapshotInterval != previous.Backup.SnapshotInterval
	backup.SnapshotInterval = previous.Backup.SnapshotInterval
	for i := range normalized.Databases {
		changed = changed || normalized.Databases[i].SyncInterval != previous.Databases[i].SyncInterval
		normalized.Databases[i].SyncInterval = previous.Databases[i].SyncInterval
	}
	return changed && reflect.DeepEqual(normalized, previous)
}
