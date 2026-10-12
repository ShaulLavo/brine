package plan

import (
	"reflect"

	"github.com/ShaulLavo/brine/internal/policy"
)

func ManagedBackupOnly(after, before policy.Desired) bool {
	if after.SpacetimeDB == nil || before.SpacetimeDB == nil {
		return false
	}
	changed := after.SpacetimeDB.RecoveryPasswordRef != before.SpacetimeDB.RecoveryPasswordRef || !reflect.DeepEqual(after.SpacetimePolicy, before.SpacetimePolicy) || !reflect.DeepEqual(after.BackupRetention, before.BackupRetention)
	if !changed {
		return false
	}
	left, right := after.App(), before.App()
	left.SpacetimeDB.RecoveryPasswordRef = right.SpacetimeDB.RecoveryPasswordRef
	return reflect.DeepEqual(left, right)
}
