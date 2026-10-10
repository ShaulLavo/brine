//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
)

type parsedRefusalPolicy struct{ raw []byte }

func (p parsedRefusalPolicy) Load(context.Context) (policy.Policy, error) {
	return productionPolicy(p.raw)
}

func TestProductionPlanPreservesPolicyLoaderRefusals(t *testing.T) {
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	invalid := []byte(strings.Replace(string(raw), `persistent_roots = ["/srv/brine/data"]`, `persistent_roots = ["relative-private-root"]`, 1))
	if string(invalid) == string(raw) {
		t.Fatal("fixture replacement missing")
	}
	_, err = productionPolicy(invalid)
	var refusal *policy.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("expected parser refusal, got %v", err)
	}
	for _, prepare := range []bool{false, true} {
		t.Run(fmt.Sprint(prepare), func(t *testing.T) {
			r, _, _, _ := connectedPreparationRig(t)
			r.service.Policy = parsedRefusalPolicy{invalid}
			app, err := spec.Parse([]byte(r.spec))
			if err != nil {
				t.Fatal(err)
			}
			var observed dispatch.Planned
			if prepare {
				observed, err = r.service.PlanDataPreparation(t.Context(), app)
			} else {
				observed, err = r.service.Plan(t.Context(), app)
			}
			if err == nil || result.Classify(err).Code() != result.PolicyRefused || result.ExitCode(err) != 4 || observed.PlanID != "" {
				t.Fatalf("plan=%+v error=%v", observed, err)
			}
			if !errors.As(err, &refusal) {
				t.Fatal("lost original refusal")
			}
			r.service.Policy = DiskPolicy{Path: "/missing-private-policy"}
			if prepare {
				_, err = r.service.PlanDataPreparation(t.Context(), app)
			} else {
				_, err = r.service.Plan(t.Context(), app)
			}
			if result.Classify(err).Code() != result.DependencyMissing || result.ExitCode(err) != 3 {
				t.Fatalf("filesystem load failure=%v", err)
			}
		})
	}
}
