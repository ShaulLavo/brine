package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/cdnoperator"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestCDNCLIStatusNeedsNoProviderCredential(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	calls := 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls++
		var input cdnoperator.StatusRequest
		if request.Op != "cdn_status" || json.Unmarshal(request.Args, &input) != nil || input.App != "hello" {
			t.Fatal("incorrect typed status request")
		}
		return result.Success("brine host cdn_status", cdnoperator.Status{App: "hello"}), nil
	})
	if err := Execute(deps, []string{"cdn", "status", "--app", "hello", "--target", "fixture", "--profiles", "/absent/profile.json", "--json", "--no-input"}); err != nil || calls != 1 {
		t.Fatal(err, calls, stderr.String())
	}
	var envelope result.Envelope
	if json.Unmarshal(out.Bytes(), &envelope) != nil || !envelope.OK || envelope.Command != "brine cdn status" {
		t.Fatal("missing native machine status", out.String())
	}
}

func TestCDNCLIRejectsInvalidRequestsBeforeTargetOrProviderAccess(t *testing.T) {
	for _, args := range [][]string{
		{"cdn", "plan", "hello", "--binding", "public", "--profile", "edge", "--sample", "/objects/x.bin", "--mode", "invalid", "--target", "fixture"},
		{"cdn", "apply", "--plan-id", "invalid", "--idempotency-key", "key", "--target", "fixture"},
		{"cdn", "resume", "--operation", "invalid/id", "--target", "fixture"},
		{"cdn", "status", "--app", "Invalid", "--target", "fixture"},
		{"cdn", "status", "--app", "hello"},
	} {
		var out, stderr bytes.Buffer
		deps := testDependencies(t, &out, &stderr)
		deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
			t.Fatal("invalid request reached target access")
			return transport.Target{}, nil
		}
		if err := Execute(deps, append(args, "--json", "--no-input")); err == nil || !json.Valid(out.Bytes()) {
			t.Fatal("invalid CDN request accepted or lost machine error", args, out.String())
		}
	}
}

func TestCDNCLIRegistersNativeCommands(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	if err := Execute(deps, []string{"cdn", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plan", "apply", "resume", "status"} {
		if !strings.Contains(out.String(), name) {
			t.Fatal("missing native CDN command", name)
		}
	}
}
