package cli

import (
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
)

func secretPermissionActions(apps, allow, revoke []string) ([]policy.PermissionAction, error) {
	if len(apps) == 0 && len(allow) == 0 && len(revoke) == 0 {
		return nil, nil
	}
	if len(apps) != 1 || len(allow)+len(revoke) == 0 {
		return nil, result.New(result.InvalidUsage, nil)
	}
	changes := make([]policy.PermissionAction, 0, len(allow)+len(revoke))
	for _, ref := range allow {
		changes = append(changes, policy.PermissionAction{Kind: policy.AllowSecret, Secret: &policy.SecretPermission{App: spec.Name(apps[0]), Reference: spec.SecretReference(ref)}})
	}
	for _, ref := range revoke {
		changes = append(changes, policy.PermissionAction{Kind: policy.RevokeSecret, Secret: &policy.SecretPermission{App: spec.Name(apps[0]), Reference: spec.SecretReference(ref)}})
	}
	return changes, nil
}
