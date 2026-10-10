package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestBackupCredentialsClientPrivateStdin(t *testing.T) {
	for _, mode := range []string{"", "--json", "--jsonl"} {
		t.Run(mode, func(t *testing.T) {
			var out, stderr bytes.Buffer
			deps := testDependencies(t, &out, &stderr)
			deps.Stdin = strings.NewReader(`{"access_key_id":"PLANTED_KEY","secret_access_key":"PLANTED_SECRET","session_token":"PLANTED_TOKEN"}`)
			receipt := backupCredentialReceipt(t)
			id := receipt.PlanID
			deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
			calls := 0
			deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, r dispatch.Request) (result.Envelope, error) {
				calls++
				if r.Op != "backup_credentials_set" {
					t.Fatal(r.Op)
				}
				var f map[string]json.RawMessage
				if json.Unmarshal(r.Args, &f) != nil || string(f["app"]) != `"hello"` || string(f["database"]) != `"audit"` || !strings.Contains(string(f["packet"]), "PLANTED_TOKEN") {
					t.Fatal("private stdin packet missing")
				}
				return result.Success("brine host "+r.Op, receipt), nil
			})
			args := []string{"backup", "credentials", "set", "hello", "--plan-id", id, "--target", "fixture", "--database", "audit"}
			if mode != "" {
				args = append(args, mode)
			}
			if err := Execute(deps, args); err != nil || calls != 1 {
				t.Fatal(err, calls)
			}
			if strings.Contains(out.String()+stderr.String(), "PLANTED") {
				t.Fatal("credential leaked")
			}
		})
	}
}
func backupCredentialReceipt(t *testing.T) backupcredentials.Receipt {
	t.Helper()
	p := backupcredentials.Plan{Requester: "fixture", Kind: backupcredentials.Kind, Version: 1,
		Scope: backupcredentials.Scope{TargetHash: "sha256:" + strings.Repeat("a", 64), App: "hello", CredentialRef: "hello", Destination: "archive", Binding: strings.Repeat("b", 32), Epoch: strings.Repeat("c", 32), PolicyHash: "sha256:" + strings.Repeat("d", 64)}}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	p.ID = "sha256:" + hex.EncodeToString(hash[:])
	if !p.Valid() {
		t.Fatal("invalid fixture plan")
	}
	return backupcredentials.Receipt{Requester: p.Requester, PlanID: p.ID, Scope: p.Scope, Version: p.Version, File: "s3/hello/v1.env", ReceivedAt: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}
}

func TestBackupCredentialClientRefusesMalformedReceipt(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.Stdin = strings.NewReader(`{"access_key_id":"PLANTED_KEY","secret_access_key":"PLANTED_SECRET"}`)
	r := backupCredentialReceipt(t)
	r.Version = 0
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
		return transport.Target{Name: "fixture"}, nil
	}
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
		return result.Success("brine host "+req.Op, r), nil
	})
	err := Execute(deps, []string{"backup", "credentials", "set", "hello", "--plan-id", r.PlanID, "--target", "fixture", "--json"})
	if err == nil || result.Classify(err).Code() != result.TransportInvalidResponse {
		t.Fatal("accepted invalid credential receipt", err)
	}
}

func TestBackupCredentialRefusesTerminalStdin(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.Stdin = strings.NewReader(`{"access_key_id":"PLANTED_KEY","secret_access_key":"PLANTED_SECRET"}`)
	withTestTerminal(t, &deps)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
		t.Fatal("interactive secret input reached target IO")
		return transport.Target{}, nil
	}
	id := "sha256:" + strings.Repeat("a", 64)
	err := Execute(deps, []string{"backup", "credentials", "set", "hello", "--plan-id", id, "--target", "fixture", "--json"})
	if err == nil || result.Classify(err).Code() != result.InvalidUsage {
		t.Fatal("accepted terminal secret input", err)
	}
	if strings.Contains(out.String()+stderr.String(), "PLANTED") {
		t.Fatal("interactive credential leaked")
	}
}

func TestBackupCredentialInputRefusals(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	for _, input := range []string{"", `{"access_key_id":"PLANTED_KEY","secret_access_key":"bad\nKEY=PLANTED"}`, strings.Repeat("x", backupcredentials.PacketLimit+1)} {
		var out, stderr bytes.Buffer
		deps := testDependencies(t, &out, &stderr)
		deps.Stdin = strings.NewReader(input)
		deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
			t.Fatal("invalid packet did IO")
			return transport.Target{}, nil
		}
		if err := Execute(deps, []string{"backup", "credentials", "set", "hello", "--plan-id", id, "--target", "fixture", "--json"}); err == nil {
			t.Fatal("accepted invalid packet")
		}
		if strings.Contains(out.String()+stderr.String(), "PLANTED") {
			t.Fatal("invalid packet leaked")
		}
	}
}

func TestBackupCredentialDatabasePlanWireAndHumanGuidance(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	r := backupCredentialReceipt(t)
	p := backupcredentials.Plan{Requester: r.Requester, Kind: backupcredentials.Kind, ID: r.PlanID, Scope: r.Scope, Version: r.Version, ExpiresAt: r.ExpiresAt}
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
		var args dispatch.BackupCredentialPlanArgs
		if req.Op != "backup_credentials_plan" || json.Unmarshal(req.Args, &args) != nil || args.App != "hello" || args.Database != "audit" {
			t.Fatal("database selection lost")
		}
		return result.Success("brine host "+req.Op, p), nil
	})
	if err := Execute(deps, []string{"backup", "credentials", "plan", "hello", "--database", "audit", "--target", "fixture"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--database audit") {
		t.Fatal("delivery guidance lost database selection")
	}
}

func TestBackupCredentialDatabaseNameRefusedBeforeIO(t *testing.T) {
	for _, operation := range []string{"plan", "set"} {
		var out, stderr bytes.Buffer
		deps := testDependencies(t, &out, &stderr)
		deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
			t.Fatal("invalid database reached target IO")
			return transport.Target{}, nil
		}
		args := []string{"backup", "credentials", operation, "hello", "--database", "../audit", "--target", "fixture"}
		if operation == "set" {
			args = append(args, "--plan-id", "sha256:"+strings.Repeat("a", 64))
		}
		if err := Execute(deps, args); result.Classify(err).Code() != result.InvalidUsage {
			t.Fatal("invalid database accepted", err)
		}
	}
}

func TestBackupCredentialsActivatedHealthIsPresented(t *testing.T) {
	for _, mode := range []string{"", "--json", "--jsonl"} {
		t.Run(mode, func(t *testing.T) {
			var out, stderr bytes.Buffer
			deps := testDependencies(t, &out, &stderr)
			deps.Stdin = strings.NewReader(`{"access_key_id":"PLANTED_KEY","secret_access_key":"PLANTED_SECRET"}`)
			receipt := backupCredentialReceipt(t)
			receipt.Activated = true
			receipt.ActivationStatus = "verified"
			expiry := receipt.ReceivedAt.Add(24 * time.Hour)
			receipt.ExpiresAt = &expiry
			plan := backupcredentials.Plan{Requester: receipt.Requester, Kind: backupcredentials.Kind, Scope: receipt.Scope, Version: receipt.Version, ExpiresAt: receipt.ExpiresAt}
			canonical, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(canonical)
			receipt.PlanID = "sha256:" + hex.EncodeToString(sum[:])
			health := receipt.Health(receipt.ReceivedAt.Add(37*time.Second), time.Minute)
			receipt.CredentialHealth = &health
			deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
			deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, r dispatch.Request) (result.Envelope, error) {
				return result.Success("brine host "+r.Op, receipt), nil
			})
			args := []string{"backup", "credentials", "set", "hello", "--plan-id", receipt.PlanID, "--target", "fixture"}
			if mode != "" {
				args = append(args, mode)
			}
			if err := Execute(deps, args); err != nil {
				t.Fatal(err)
			}
			text := out.String()
			if strings.Contains(text+stderr.String(), "PLANTED") || !strings.Contains(text, "verified") || !strings.Contains(text, "37") {
				t.Fatal("activation health missing or unsafe", text)
			}
			if mode == "" && (!strings.Contains(text, "not restarted") || !strings.Contains(text, expiry.Format(time.RFC3339))) {
				t.Fatal("app lifecycle distinction missing")
			}
		})
	}
}
