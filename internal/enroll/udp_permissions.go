package enroll

import (
	"context"

	operatorpolicy "github.com/ShaulLavo/brine/internal/policy"
)

func (s permissionService) checkUDPRevocations(ctx context.Context, changes []operatorpolicy.PermissionAction) error {
	for _, change := range changes {
		if change.Kind != operatorpolicy.RevokeUDPGrant {
			continue
		}
		if s.udpHeld == nil {
			return errPermissionsRefused
		}
		held, err := s.udpHeld(ctx, change.UDPApp)
		if err != nil || held {
			return errPermissionsRefused
		}
	}
	return nil
}
