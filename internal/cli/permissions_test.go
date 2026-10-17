package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/enroll"
	"github.com/ShaulLavo/brine/internal/transport"
)

type permissionCallFunc func(context.Context, transport.Target, string, string, enroll.PermissionsRequest) (enroll.PermissionsResponse, error)

func (f permissionCallFunc) Call(ctx context.Context, t transport.Target, dir, admin string, r enroll.PermissionsRequest) (enroll.PermissionsResponse, error) {
	return f(ctx, t, dir, admin, r)
}

func TestPermissionsCLIProducesReviewableMachinePlanWithoutEnrollment(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	calls := 0
	deps.PermissionClient = permissionCallFunc(func(_ context.Context, _ transport.Target, _ string, admin string, r enroll.PermissionsRequest) (enroll.PermissionsResponse, error) {
		calls++
		if admin != "operator@fixture" || r.Action != "plan" || r.AppLabel != "demo" || len(r.Changes) != 2 {
			t.Fatal("wrong typed permission request", r)
		}
		return enroll.PermissionsResponse{Plan: &enroll.PermissionsPlan{ID: strings.Repeat("a", 64), Changes: r.Changes}}, nil
	})
	args := []string{"permissions", "plan", "--target", "fixture", "--admin", "operator@fixture", "--app", "demo", "--allow-registry", "registry.example/owned/demo", "--allow-domain", "demo.example", "--json", "--no-input"}
	if err := Execute(deps, args); err != nil || calls != 1 {
		t.Fatal(err, calls, stderr.String())
	}
	if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), "allow_registry") || !strings.Contains(out.String(), "demo.example") {
		t.Fatal("missing reviewable machine plan", out.String())
	}
}

func TestPermissionsCLIRefusesUneditableCapabilitiesAndMissingAdmin(t *testing.T) {
	for _, args := range [][]string{{"permissions", "show", "--target", "fixture", "--json"}, {"permissions", "plan", "--target", "fixture", "--admin", "fixture", "--resources.memory_mb", "9999", "--json"}, {"permissions", "plan", "--target", "fixture", "--admin", "fixture", "--allow-registry", "registry.example", "--json"}} {
		var out, stderr bytes.Buffer
		deps := testDependencies(t, &out, &stderr)
		if err := Execute(deps, args); err == nil {
			t.Fatal("invalid authority accepted", args)
		}
		if !json.Valid(out.Bytes()) {
			t.Fatal("missing failure envelope", out.String())
		}
	}
}
