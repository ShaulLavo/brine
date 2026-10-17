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

func TestPermissionsPrivilegedRequestClosureBeforeRootHelper(t *testing.T) {
	base := `{"action":"show","identity_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","host_key":"fixture-host"}`
	plan := strings.Replace(base, `"action":"show"`, `"action":"plan","changes":[{"kind":"allow_domain","domain":"demo.example"}]`, 1)
	registry := strings.Replace(plan, `{"kind":"allow_domain","domain":"demo.example"}`, `{"kind":"allow_registry","registry":{"host":"registry.example","repository_prefixes":["demo"]}}`, 1)
	malformed := []string{
		base + ` {}`,
		strings.Replace(base, `"action":"show"`, `"action":"show","action":"show"`, 1),
		strings.Replace(base, `"action":`, `"ACTION":`, 1),
		strings.Replace(base, `"host_key":"fixture-host"`, `"host_key":null`, 1),
		strings.Replace(base, `"identity_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="`, `"identity_key":null`, 1),
		strings.TrimSuffix(base, "}") + `,"changes":null}`,
		strings.TrimSuffix(base, "}") + `,"plan_id":null}`,
		strings.TrimSuffix(base, "}") + `,"app_label":null}`,
		strings.TrimSuffix(base, "}") + `,"idempotency_key":null}`,
		strings.Replace(plan, `"kind":"allow_domain"`, `"kind":"allow_domain","kind":"allow_domain"`, 1),
		strings.Replace(plan, `"kind":`, `"KIND":`, 1),
		strings.Replace(plan, `"domain":"demo.example"`, `"domain":null`, 1),
		strings.Replace(plan, `"domain":"demo.example"`, `"domain":"demo.example","registry":null`, 1),
		strings.Replace(plan, `"changes":[{"kind":"allow_domain","domain":"demo.example"}]`, `"changes":null`, 1),
		strings.Replace(registry, `"host":"registry.example"`, `"host":"registry.example","host":"registry.example"`, 1),
		strings.Replace(registry, `"host":`, `"HOST":`, 1),
		strings.Replace(registry, `"repository_prefixes":["demo"]`, `"repository_prefixes":null`, 1),
		strings.Replace(registry, `"repository_prefixes":["demo"]`, `"repository_prefixes":[null]`, 1),
		strings.Replace(registry, `"registry":{`, `"registry":null,"extra":{`, 1),
	}
	for i, raw := range malformed {
		rejectPermissionRootEntry(t, i, raw)
	}
	calls := 0
	_, err := executeHostPermissions(t.Context(), strings.NewReader(registry), func(_ context.Context, r enroll.PermissionsRequest) (enroll.PermissionsResponse, error) {
		calls++
		return enroll.PermissionsResponse{}, nil
	})
	if err != nil || calls != 1 {
		t.Fatal("valid request did not reach helper", err, calls)
	}
}

func rejectPermissionRootEntry(t *testing.T, index int, raw string) {
	t.Helper()
	calls := 0
	_, err := executeHostPermissions(t.Context(), strings.NewReader(raw), func(context.Context, enroll.PermissionsRequest) (enroll.PermissionsResponse, error) {
		calls++
		return enroll.PermissionsResponse{}, nil
	})
	if err == nil || calls != 0 {
		t.Fatalf("malformed case %d reached root helper: calls=%d error=%v", index, calls, err)
	}
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
