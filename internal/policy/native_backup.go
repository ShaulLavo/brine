package policy

import (
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
)

func (p Policy) CheckNativeBackup(desired Desired) error {
	if p.config == nil || ValidateManagedDesired(desired) != nil || p.config.SpacetimeDB == nil || *p.config.SpacetimeDB != *desired.SpacetimePolicy {
		return refuse("policy.native_backup_changed", "spacetimedb", "native backup review settings must match the active policy")
	}
	destination, ok := p.BackupDestination(desired.SpacetimeDB.BackupDestination)
	if !ok || destination != desired.BackupDestinations[0] {
		return refuse("policy.native_backup_changed", "backup_destinations", "native backup destination must match the active policy")
	}
	var retention []data.RetentionEvidence
	if evidence, ok := p.BackupRetention(destination.Reference); ok {
		retention = []data.RetentionEvidence{evidence}
	}
	if !slices.Equal(retention, desired.BackupRetention) {
		return refuse("policy.native_backup_changed", "backup_retention", "native backup retention must match the active policy")
	}
	return nil
}
