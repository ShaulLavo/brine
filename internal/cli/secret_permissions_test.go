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

func TestSecretPermissionsCLIExactScopeAndAuditOnlyApp(t *testing.T) {
	for _, action := range []string{"allow", "revoke"} {
		t.Run(action, func(t *testing.T) { checkSecretPermissionCLI(t, action) })
	}
}

func checkSecretPermissionCLI(t *testing.T, action string) {
	t.Helper()
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	calls := 0
	deps.PermissionClient = permissionCallFunc(func(_ context.Context, _ transport.Target, _ string, _ string, request enroll.PermissionsRequest) (enroll.PermissionsResponse, error) {
		calls++
		raw, _ := json.Marshal(request)
		if request.AppLabel != "audit-only" || !strings.Contains(string(raw), `"kind":"`+action+`_secret","secret":{"app":"waffle-relay","reference":"relay-signing"}`) {
			t.Fatal("wrong secret scope", string(raw))
		}
		return enroll.PermissionsResponse{Plan: &enroll.PermissionsPlan{Changes: request.Changes}}, nil
	})
	args := []string{"permissions", "plan", "--target", "fixture", "--admin", "operator@fixture", "--app", "audit-only", "--secret-app", "waffle-relay", "--" + action + "-secret-ref", "relay-signing", "--json", "--no-input"}
	if err := Execute(deps, args); err != nil || calls != 1 || !json.Valid(out.Bytes()) {
		t.Fatal("scoped secret plan failed", err, calls, out.String())
	}
}

func TestSecretPermissionsCLIRejectsAmbiguousScopeBeforeTransport(t *testing.T) {
	for _, flags := range [][]string{{"--allow-secret-ref", "ref"}, {"--secret-app", "app"}, {"--secret-app", "app", "--secret-app", "other", "--allow-secret-ref", "ref"}, {"--secret-app", "../app", "--allow-secret-ref", "ref"}, {"--secret-app", "app", "--allow-secret-ref", "*"}, {"--secret-app", "app", "--allow-secret-ref", "ref", "--allow-secret-ref", "ref"}, {"--secret-app", "app", "--allow-secret-ref", "ref", "--revoke-secret-ref", "ref"}} {
		checkInvalidSecretPermissionCLI(t, flags)
	}
}

func checkInvalidSecretPermissionCLI(t *testing.T, flags []string) {
	t.Helper()
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
		t.Fatal("invalid scope reached target loader")
		return transport.Target{}, nil
	}
	args := append([]string{"permissions", "plan", "--target", "fixture", "--admin", "operator@fixture", "--json", "--no-input"}, flags...)
	if err := Execute(deps, args); err == nil || !json.Valid(out.Bytes()) {
		t.Fatal("invalid secret scope accepted", flags, err)
	}
}
