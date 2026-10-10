package apply

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/policy"
)

func stateless(d policy.Desired) bool { return d.Stateless() }
func (x *execution) checkCompatibility(ctx context.Context) error {
	if stateless(x.previousDesired) && stateless(x.desired) {
		x.compatibilityBasis = "stateless_compatible"
		return nil
	}
	if x.executor.Compatibility == nil {
		return errors.New("data compatibility unknown")
	}
	safe, err := x.executor.Compatibility.Safe(ctx, x.previousDesired, x.desired)
	if err != nil {
		return err
	}
	if !safe {
		return errors.New("data compatibility unknown")
	}
	x.compatibilityBasis = "compatibility_verified"
	return nil
}
