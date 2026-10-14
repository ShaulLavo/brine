package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
	"strings"
	"testing"
	"time"
)

func TestNativeLiveRestoreCLIWaitsForExactStoppedReceipt(t *testing.T) {
	for _, resolve := range []bool{false, true} {
		checkNativeLiveRestoreCLI(t, resolve, false)
		checkNativeLiveRestoreCLI(t, resolve, true)
	}
}

func checkNativeLiveRestoreCLI(t *testing.T, resolve, foreign bool) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	plan := "sha256:" + strings.Repeat("a", 64)
	kind := ops.NativeLiveRestore
	original := ""
	args := []string{"restore", "native-live", "apply", "game", "--plan", plan, "--target", "fixture", "--idempotency-key", strings.Repeat("b", 32), "--json"}
	if resolve {
		kind = ops.NativeLiveRestoreResolve
		original = "original"
		args = []string{"restore", "native-live", "resolve", original, "--review", plan, "--target", "fixture", "--idempotency-key", strings.Repeat("b", 32), "--json"}
	}
	receipt := liverestore.Receipt{Format: "brine-native-live-restore-receipt-v1", OperationID: "restore-operation", PlanID: plan, OriginalOperationID: original, BindingID: data.ReplicaBindingID(strings.Repeat("1", 32)), SelectedPointID: strings.Repeat("c", 32), EpochID: data.ReplicaEpochID(strings.Repeat("2", 32)), CompletionProofSHA256: strings.Repeat("d", 64), Outcome: "restored_stopped"}
	if foreign {
		receipt.PlanID = "sha256:" + strings.Repeat("e", 64)
	}
	calls := 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls++
		if request.Op == "operation" {
			raw, _ := json.Marshal(receipt)
			return result.Success("brine host operation", jobs.Status{Operation: ops.Operation{ID: receipt.OperationID, Kind: kind, App: "game", SecretRef: plan, RecoveryOf: original, State: ops.Succeeded}, Events: []ops.Event{}, Outcome: &ops.TaskOutcome{Receipt: raw}}), nil
		}
		expected := "native_live_restore"
		if resolve {
			expected = "native_live_restore_resolve"
		}
		if request.Op != expected {
			t.Fatal("wrong effect", request.Op)
		}
		if _, err := dispatch.EncodeRequest(request); err != nil {
			t.Fatal("invalid CLI request", err)
		}
		return result.Success("brine host "+expected, jobs.Accepted{Status: "accepted", OperationID: receipt.OperationID}), nil
	})
	err := Execute(deps, args)
	if foreign {
		if err == nil {
			t.Fatal("foreign receipt accepted")
		}
		return
	}
	if err != nil || calls != 2 || !strings.Contains(out.String(), "restored_stopped") {
		t.Fatal("restore did not wait for real tagged outcome", err, calls, out.String())
	}
}

func nativeLiveRestorePlanFixture(t *testing.T) liverestore.Plan {
	t.Helper()
	id, hash, selectedID := strings.Repeat("a", 32), strings.Repeat("b", 64), strings.Repeat("c", 32)
	requester, err := datainit.ParseRequester("deploy:" + hash)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	facts := liverestore.Facts{App: "game", IncarnationID: data.AppIncarnationID(id), DatabaseID: data.DatabaseID(id), Head: data.EngineHeadRef{BindingID: data.ReplicaBindingID(id), EpochID: data.ReplicaEpochID(id), Version: 3, RuntimeSHA256: hash}, Generation: 3, GenerationID: id, PolicySHA256: hash, TargetSHA256: hash, DesiredSHA256: hash}
	safety := liverestore.Safety{PointID: id, PointSHA256: hash, FenceID: data.FenceID(id), FenceOwnerOpID: "prepared-operation", SourceSealSHA256: hash, SourceWitnessSHA256: hash, StopProofSHA256: hash, CheckpointSHA256: hash, CommittedOffset: 21, CommittedOffsetKnown: true, PreparedAt: at.Add(-time.Minute), ExpiresAt: at.Add(10 * time.Minute)}
	selected := liverestore.SelectedPoint{IncarnationID: facts.IncarnationID, DatabaseID: facts.DatabaseID, BindingID: facts.Head.BindingID, PointID: selectedID, PointSHA256: hash, EpochID: facts.Head.EpochID, GenerationID: id, CaptureID: selectedID, RepositoryID: hash, RepositorySnapshot: hash, ManifestSHA256: hash, RestoreEvidenceSHA256: hash, CheckpointSHA256: hash, StateWitnessSHA256: hash, NativeDatabase: hash, NativePublisher: hash, ProgramWireHash: hash, ArtifactWasmSHA256: hash, SchemaSHA256: hash, MaterialSHA256: hash, DependenciesSHA256: hash, CommittedOffset: 8, CapturedAt: at.Add(-time.Hour), RetainUntil: at.Add(30 * 24 * time.Hour)}
	spec := liverestore.PlanSpec{Requester: requester, Facts: facts, Safety: safety, Selected: selected, Recovery: liverestore.RecoveryRelease{ReleaseID: "recovery-release-operation", CodeSHA256: hash, CompatibilitySHA256: hash, ProgramWireHash: hash, SchemaSHA256: hash}, IssuedAt: at, ExpiresAt: at.Add(5 * time.Minute)}
	planned, err := liverestore.NewPlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	return planned
}
func TestNativeLiveRestoreJSONPlanExposesTheIDAcceptedByApply(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	planned := nativeLiveRestorePlanFixture(t)
	calls := []string{}
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls = append(calls, request.Op)
		if _, err := dispatch.EncodeRequest(request); err != nil {
			t.Fatal("invalid CLI request", err)
		}
		switch request.Op {
		case "native_live_restore_plan":
			return result.Success("brine host native_live_restore_plan", planned), nil
		case "native_live_restore":
			apply, err := liverestore.DecodeApplyRequest(request.Args)
			if err != nil || apply.PlanID != planned.ID() {
				t.Fatal("JSON plan ID did not reach apply unchanged", err, apply.PlanID)
			}
			return result.Success("brine host native_live_restore", jobs.Accepted{Status: "accepted", OperationID: "restore-operation"}), nil
		default:
			t.Fatal("unexpected operation", request.Op)
		}
		return result.Envelope{}, nil
	})
	spec := planned.Spec()
	args := []string{"restore", "native-live", "plan", "game", "--database", "main", "--point", spec.Selected.PointID, "--prepared-point", spec.Safety.PointID, "--recovery-release", spec.Recovery.ReleaseID, "--target", "fixture", "--json"}
	if err := Execute(deps, args); err != nil {
		t.Fatal(err)
	}
	var response struct {
		OK   bool `json:"ok"`
		Data struct {
			PlanID string          `json:"plan_id"`
			Plan   json.RawMessage `json:"plan"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || !response.OK || response.Data.PlanID != planned.ID() {
		t.Fatal("plan response omitted usable ID", err, out.String())
	}
	decoded, err := liverestore.DecodePlan(response.Data.Plan)
	if err != nil || decoded.ID() != planned.ID() || !bytes.Equal(response.Data.Plan, planned.Bytes()) {
		t.Fatal("envelope changed canonical plan", err)
	}
	out.Reset()
	if err := Execute(deps, []string{"restore", "native-live", "apply", "game", "--plan", response.Data.PlanID, "--target", "fixture", "--idempotency-key", strings.Repeat("d", 32), "--no-wait", "--json"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "native_live_restore_plan" || calls[1] != "native_live_restore" {
		t.Fatal("public workflow repeated effects", calls)
	}
}
