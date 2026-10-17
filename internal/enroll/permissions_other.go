//go:build !linux

package enroll

import (
	"context"
	"github.com/ShaulLavo/brine/internal/result"
)

func PermissionsHostOperation(context.Context, PermissionsRequest) (PermissionsResponse, error) {
	return PermissionsResponse{}, result.New(result.PolicyRefused, nil)
}
