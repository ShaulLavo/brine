package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
)

type refusingPolicyPlanner struct{ refusal error }

func (p refusingPolicyPlanner) Plan(context.Context, spec.App) (Planned, error) {
	return Planned{}, fmt.Errorf("private-context: %w", p.refusal)
}
func (p refusingPolicyPlanner) PlanDataPreparation(ctx context.Context, a spec.App) (Planned, error) {
	return p.Plan(ctx, a)
}

func TestConnectedPolicyRefusalIsTypedAndPrivate(t *testing.T) {
	for _, op := range []string{"plan", "data_prepare_plan"} {
		for _, tc := range []struct {
			diagnostic string
			code       result.Code
		}{
			{"policy.registry_denied", result.PolicyRegistryDenied},
			{"policy.domain_denied", result.PolicyRefused},
		} {
			t.Run(op+"/"+tc.diagnostic, func(t *testing.T) {
				args, _ := json.Marshal(PlanArgs{Spec: "schema_version=1\nname=\"fixture\"\nimage=\"ghcr.io/example/fixture@sha256:" + strings.Repeat("a", 64) + "\"\ncontainer_port=8080\ndomains=[\"fixture.example.test\"]\n"})
				wire, err := EncodeRequest(Request{SchemaVersion: 1, Op: op, RequestID: "refused", Args: args})
				if err != nil {
					t.Fatal(err)
				}
				server := NewServer("test", nil)
				server.authorize = func(context.Context, Class) error { return nil }
				server.Planner = refusingPolicyPlanner{&policy.Refusal{Code: tc.diagnostic, Field: "private-field", Message: "private-value"}}
				response, err := server.Handle(t.Context(), strings.NewReader(string(wire)))
				if err == nil || result.ExitCode(err) != 4 || response.Error == nil || response.Error.Code != tc.code {
					t.Fatalf("response=%+v error=%v", response, err)
				}
				raw, _ := json.Marshal(response)
				for _, private := range []string{"private-context", "private-field", "private-value"} {
					if strings.Contains(string(raw), private) {
						t.Fatalf("leaked %s", private)
					}
				}
				if _, err := DecodeResponse(raw, op); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
