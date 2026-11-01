package apps

import (
	"context"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
)

func udpRollbackInvolved(in plan.Input, selected plan.Plan) bool {
	if selected.UDP != nil || in.Desired.UDP != nil {
		return true
	}
	for _, r := range in.State.Releases {
		if r.App == string(in.Desired.Name) && r.UDP != nil {
			return true
		}
	}
	if in.State.UDP == nil {
		return false
	}
	for _, r := range in.State.UDP.Reservations {
		if r.App == string(in.Desired.Name) {
			return true
		}
	}
	return false
}
func (s Service) udpRollbackInput(ctx context.Context, in plan.Input, selected plan.Plan) (plan.Input, error) {
	if s.LoadPolicy == nil {
		return in, result.New(result.DependencyMissing, nil)
	}
	pol, err := s.LoadPolicy(ctx)
	if err != nil {
		return in, err
	}
	in.Desired, err = policy.Normalize(in.Desired.App(), pol)
	if err != nil {
		return in, err
	}
	if selected.UDP != nil && selected.UDP.Next != nil {
		endpoint := selected.UDP.Next.Endpoint
		in.UDPExact = &endpoint
	}
	return in, nil
}
