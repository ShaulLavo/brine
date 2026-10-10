//go:build linux

package host

import (
	"context"
	"encoding/json"
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

func TestProductionAuthorizationPreservesPolicyLoaderRefusalsThroughDispatch(t *testing.T) {
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	invalid := strings.Replace(string(raw), `persistent_roots = ["/srv/brine/data"]`, `persistent_roots = ["relative-private-root"]`, 1)
	if invalid == string(raw) {
		t.Fatal("fixture replacement missing")
	}
	for _, op := range []string{"plan", "data_prepare_plan"} {
		for _, tc := range []struct {
			name     string
			loader   PolicyLoader
			code     result.Code
			category int
		}{
			{"malformed", parsedRefusalPolicy{[]byte(invalid)}, result.PolicyRefused, 4},
			{"missing", DiskPolicy{Path: "/missing-private-policy"}, result.DependencyMissing, 3},
		} {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				r, _, _, _ := connectedPreparationRig(t)
				r.service.Policy = tc.loader
				// Rebind the method value so the dispatcher uses the changed production service.
				r.server = r.server.WithJobs(nil, r.service.Authorize)
				r.server.Planner = r.service
				authorizationErr := r.service.Authorize(t.Context(), dispatch.Mutating)
				if tc.code == result.PolicyRefused {
					var refusal *policy.Refusal
					if !errors.As(authorizationErr, &refusal) {
						t.Fatal("authorization lost original refusal")
					}
				}
				args, err := json.Marshal(dispatch.PlanArgs{Spec: r.spec})
				if err != nil {
					t.Fatal(err)
				}
				wire, err := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: 1, Op: op, RequestID: "refused", Args: args})
				if err != nil {
					t.Fatal(err)
				}
				response, err := r.server.Handle(t.Context(), strings.NewReader(string(wire)))
				if err == nil || result.ExitCode(err) != tc.category || response.Error == nil || response.Error.Code != tc.code {
					t.Fatalf("response=%+v error=%v", response, err)
				}
				encoded, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				for _, private := range []string{"relative-private-root", "/missing-private-policy"} {
					if strings.Contains(string(encoded), private) {
						t.Fatalf("leaked private diagnostic %q", private)
					}
				}
				if _, err := dispatch.DecodeResponse(encoded, op); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
