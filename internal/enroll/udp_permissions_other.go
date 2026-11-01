//go:build !linux

package enroll

import "context"

func UDPGrantHeld(context.Context, string) (bool, error) { return false, errPermissionsRefused }
