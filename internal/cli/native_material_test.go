package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestNativeMaterialCommandUsesPrivateStdinAndReportedKey(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.Stdin = strings.NewReader(`{"publisher":"PLANTED_TOKEN","signing_private":"PLANTED_PRIVATE","signing_public":"PLANTED_PUBLIC","auth_policy":{"mode":"supplied_key_jwt","require_client_token":true},"publisher_expires_at":"2030-01-01T00:00:00Z"}`)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	planID, key := "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 32)
	version := func(ref string) data.EngineSecretVersion {
		return data.EngineSecretVersion{Reference: ref, Version: 1, SHA256: strings.Repeat("a", 64)}
	}
	secrets := data.EngineSecrets{Publisher: version("publisher"), SigningPrivate: version("private"), SigningPublic: version("public"), AuthPolicy: version("auth")}
	secrets.Publisher.ExpiresAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	receipt := nativematerial.Receipt{OperationID: "operation-native-material", App: "game", PreparationPlanID: planID, BindingID: data.ReplicaBindingID(strings.Repeat("b", 32)), EpochID: data.ReplicaEpochID(strings.Repeat("c", 32)), Outcome: "configured_unpublished", Secrets: secrets}
	calls := 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls++
		if request.Op != "native_material_set" {
			t.Fatal("wrong operation")
		}
		var wire struct {
			App    string          `json:"app"`
			Plan   string          `json:"preparation_plan_id"`
			Key    string          `json:"idempotency_key"`
			Packet json.RawMessage `json:"packet"`
		}
		if json.Unmarshal(request.Args, &wire) != nil || wire.App != "game" || wire.Plan != planID || wire.Key != key || !bytes.Contains(wire.Packet, []byte("PLANTED_PRIVATE")) {
			t.Fatal("private reference-scoped request changed")
		}
		return result.Success("brine host native_material_set", receipt), nil
	})
	if err := Execute(deps, []string{"data", "material", "set", "game", "--preparation-plan", planID, "--idempotency-key", key, "--target", "fixture", "--json"}); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	if strings.Contains(out.String()+diagnostics.String(), "PLANTED") {
		t.Fatal("private role leaked into output")
	}
}
func TestNativeMaterialCommandRejectsOpenPacketBeforeTargetIO(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.Stdin = strings.NewReader(`{"runtime":{},"publisher":"PLANTED"}`)
	called := false
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { called = true; return transport.Target{}, nil }
	err := Execute(deps, []string{"data", "material", "set", "game", "--preparation-plan", "sha256:" + strings.Repeat("a", 64), "--target", "fixture"})
	if err == nil || called || strings.Contains(out.String()+diagnostics.String(), "PLANTED") {
		t.Fatal("open runtime packet reached target or output")
	}
}
