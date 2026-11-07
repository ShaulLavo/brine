//go:build !linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/target"
)

func openState(context.Context, string, stateMode, *admission.Exemption) (*gatedStore, error) {
	return nil, admission.ErrUnavailable
}
func readControlInventory(context.Context, string) (target.ControlInventory, error) {
	return target.ControlInventory{}, admission.ErrUnavailable
}
func ControlMigrate(context.Context, admission.Exemption) error { return admission.ErrUnavailable }
func UDPGrantHeld(context.Context, string) (bool, error)        { return false, admission.ErrUnavailable }
