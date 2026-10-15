package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestMaterialActivationCommandsSelectExactDeliveryAndPlan(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	now := time.Now().UTC()
	role := func(ref string) data.EngineSecretVersion {
		return data.EngineSecretVersion{Reference: ref, Version: 2, SHA256: strings.Repeat("a", 64)}
	}
	secrets := data.EngineSecrets{Publisher: role("publisher"), SigningPrivate: role("private"), SigningPublic: role("public"), AuthPolicy: role("auth")}
	secrets.Publisher.ExpiresAt = now.Add(time.Hour)
	p := nativematerial.ActivationPlan{ID: strings.Repeat("a", 32), App: "game", Requester: "fixture", DeliveryOperationID: "delivery", Head: data.EngineHeadRef{BindingID: data.ReplicaBindingID(strings.Repeat("b", 32)), EpochID: data.ReplicaEpochID(strings.Repeat("c", 32)), Version: 3, RuntimeSHA256: strings.Repeat("d", 64)}, DesiredRevision: 2, PolicyHash: "sha256:" + strings.Repeat("e", 64), Secrets: secrets, RestartRequired: true, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	receipt := nativematerial.ActivationReceipt{PlanID: p.ID, BindingID: p.Head.BindingID, EpochID: p.Head.EpochID, RuntimeVersion: 4, RuntimeSHA256: strings.Repeat("f", 64)}
	calls := 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls++
		switch request.Op {
		case "native_material_plan":
			var args dispatch.NativeMaterialPlanArgs
			if json.Unmarshal(request.Args, &args) != nil || args.App != "game" || args.DeliveryOperationID != "delivery" {
				t.Fatal("wrong delivery selection")
			}
			return result.Success("brine host native_material_plan", p), nil
		case "native_material_activate":
			var args dispatch.NativeMaterialActivateArgs
			if json.Unmarshal(request.Args, &args) != nil || args.PlanID != p.ID {
				t.Fatal("wrong reviewed plan")
			}
			return result.Success("brine host native_material_activate", receipt), nil
		default:
			t.Fatal("unexpected request", request.Op)
			return result.Envelope{}, nil
		}
	})
	if err := Execute(deps, []string{"data", "material", "plan", "game", "--delivery", "delivery", "--target", "fixture", "--json"}); err != nil {
		t.Fatal(err)
	}
	if err := Execute(deps, []string{"data", "material", "activate", p.ID, "--target", "fixture", "--json"}); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
}

func TestEngineStopCommandReviewsBothComponents(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	called := false
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		called = true
		var args dispatch.EngineStopArgs
		if request.Op != "engine_stop" || json.Unmarshal(request.Args, &args) != nil || args.App != "game" {
			t.Fatal("engine selection lost")
		}
		return result.Success("brine host engine_stop", apps.ConfigPlan{PlanID: "sha256:" + strings.Repeat("a", 64), Kind: plan.Update, Lifecycle: plan.StopApp, ManagedLifecycle: &plan.ManagedLifecyclePlan{Engine: true, Frontend: true, Ingress: true}, Conflicts: []plan.Diagnostic{}}), nil
	})
	if err := Execute(deps, []string{"stop", "game", "--engine", "--target", "fixture", "--json"}); err != nil || !called || !strings.Contains(out.String(), `"engine":true`) {
		t.Fatal("full stop was not reviewable", err, out.String())
	}
}
