package apply

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
)

// This frozen typed schema deliberately stops compiling when Desired gains a
// field. P04 must explicitly classify persistent data before widening it.
type statelessDesiredV1 struct {
	SchemaVersion int                  `json:"schema_version"`
	Name          spec.Name            `json:"name"`
	Image         spec.ImageReference  `json:"image"`
	ContainerPort spec.Port            `json:"container_port"`
	Domains       []spec.Domain        `json:"domains"`
	Health        policy.Health        `json:"health"`
	Resources     policy.Resources     `json:"resources"`
	Environment   []policy.Environment `json:"environment"`
	Secrets       []policy.Secret      `json:"secrets"`
	PolicyVersion string               `json:"policy_version"`
	PolicyHash    string               `json:"policy_hash"`
	AppPorts      policy.PortRange     `json:"app_ports"`
	// Capacity admission does not introduce a data mount or schema change.
	MinimumFreeDiskBytes uint64 `json:"minimum_free_disk_bytes"`
}

var _ = statelessDesiredV1(policy.Desired{})

func stateless(d policy.Desired) bool {
	return statelessDesiredV1(d).SchemaVersion == 1
}
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
