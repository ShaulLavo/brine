//go:build linux

package host

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestNativeBackupPolicyPlanningReturnsTypedConflict(t *testing.T) {
	f := freshNativeMaterialFixture(t)
	o := nativeBackupPolicyOperations{service: f.service, jobs: jobs.Service{Store: f.service.Store, Requester: f.service.Requester}}
	_, err := o.PlanPolicy(t.Context(), dispatch.NativeBackupPolicyPlanArgs{DeploymentPlanID: f.plan.Hash})
	if err == nil || result.Classify(err).Code() != result.Conflict {
		t.Fatalf("missing retention evidence returned %s, want conflict: %v", result.Classify(err).Code(), err)
	}
}
