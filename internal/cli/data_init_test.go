package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestDataInitializationClientCarriesOnlyPlanReferences(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	hash := "sha256:" + strings.Repeat("a", 64)
	calls := 0
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, r dispatch.Request) (result.Envelope, error) {
		calls++
		const operationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		if calls == 1 {
			if r.Op != "data_init_apply" || string(r.Args) != `{"app":"hello","plan_id":"`+hash+`"}` {
				t.Fatalf("unexpected request %s %s", r.Op, r.Args)
			}
			return result.Success("brine host data_init_apply", jobs.Accepted{Status: "accepted", OperationID: operationID}), nil
		}
		if r.Op != "operation" {
			t.Fatal("initialization did not poll status")
		}
		receipt, err := json.Marshal(datainit.Operation{ID: strings.Repeat("1", 32), PlanID: hash, Fence: data.FenceID(strings.Repeat("2", 32)), State: "succeeded"})
		if err != nil {
			t.Fatal(err)
		}
		return result.Success("brine host operation", jobs.Status{Operation: ops.Operation{ID: operationID, Kind: ops.DataInitApply, App: "hello", SecretRef: hash, State: ops.Succeeded}, Outcome: &ops.TaskOutcome{Receipt: receipt}}), nil
	})
	if err := Execute(deps, []string{"data", "init", "hello", "--plan-id", hash, "--target", "fixture", "--json"}); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	if !strings.Contains(out.String(), "succeeded") {
		t.Fatal("result not printed")
	}
}

func TestDataInitializationRejectsInvalidUsageBeforeRuntime(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	for _, args := range [][]string{
		{"host", "data-init", "hello", "--plan"},
		{"host", "data-init", "hello", "--plan-id", hash, "--artifact", hash},
		{"data", "init", "hello", "--plan", "--first-release-plan", hash, "--artifact", hash, "--approved"},
		{"data", "init", "hello", "--plan", "--first-release-plan", hash, "--artifact", hash, "--restore-point", hash},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, stderr bytes.Buffer
			deps := testDependencies(t, &out, &stderr)
			err := ExecuteWithRuntime(deps, args, RuntimeLifecycle{Open: func(context.Context, bool) (RuntimeServices, error) {
				t.Fatal("invalid command opened host runtime")
				return RuntimeServices{}, nil
			}})
			if err == nil {
				t.Fatal("invalid initialization accepted")
			}
		})
	}
}

func TestDataInitializationNoWaitReturnsAcceptedJobWithoutPolling(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	hash := "sha256:" + strings.Repeat("a", 64)
	calls := 0
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls++
		if request.Op != "data_init_apply" {
			t.Fatal("no-wait polled or changed the request")
		}
		return result.Success("brine host data_init_apply", jobs.Accepted{Status: "accepted", OperationID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}), nil
	})
	if err := Execute(deps, []string{"data", "init", "hello", "--plan-id", hash, "--target", "fixture", "--no-wait", "--json"}); err != nil || calls != 1 || !strings.Contains(out.String(), "accepted") {
		t.Fatal("no-wait lost acceptance", err, calls, out.String())
	}
}

func TestInitializationPlanSendsExactArtifactAndShowsSQLAndCatalog(t *testing.T) {
	for _, machine := range []bool{false, true} {
		t.Run(fmt.Sprint(machine), func(t *testing.T) {
			var out, stderr bytes.Buffer
			deps := testDependencies(t, &out, &stderr)
			filename := filepath.Join(t.TempDir(), "initializer.json")
			artifact := data.SchemaInitializer{Definition: data.SQLiteSchemaDefinition{Database: "main", Marker: "v1", CatalogSHA256: "688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"}, Statements: []string{"CREATE TABLE t(x TEXT)"}}
			raw, err := json.Marshal(artifact)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filename, raw, 0600); err != nil {
				t.Fatal(err)
			}
			hash := "sha256:" + strings.Repeat("a", 64)
			deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
			deps.OperationClient = callFunc(func(ctx context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
				var args dispatch.DataInitPlanArgs
				if request.Op != "data_init_plan" || json.Unmarshal(request.Args, &args) != nil || !bytes.Equal(args.ArtifactBytes, raw) {
					t.Fatal("exact bytes not transported")
				}
				decoded, err := datainit.DecodeArtifact(args.ArtifactBytes, args.Artifact)
				if err != nil {
					t.Fatal(err)
				}
				catalog, err := data.InitializerCatalog(ctx, decoded)
				if err != nil {
					t.Fatal(err)
				}
				requester, _ := datainit.ParseRequester("deploy:" + strings.Repeat("a", 64))
				return result.Success("brine host data_init_plan", datainit.Plan{Requester: requester, ID: hash, Request: datainit.Request(args), Definition: decoded.Definition, Catalog: catalog}), nil
			})
			args := []string{"data", "init", "hello", "--plan", "--first-release-plan", hash, "--artifact", filename, "--target", "fixture"}
			if machine {
				args = append(args, "--json")
			}
			if err := Execute(deps, args); err != nil {
				t.Fatal(err)
			}
			if machine {
				if !strings.Contains(out.String(), `"artifact_bytes"`) || !strings.Contains(out.String(), `"catalog"`) {
					t.Fatal("machine review omitted bytes/catalog")
				}
			} else if !strings.Contains(out.String(), "CREATE TABLE t(x TEXT);") || !strings.Contains(out.String(), "Resulting catalog:") || !strings.Contains(out.String(), `"TableName": "t"`) {
				t.Fatal("human review omitted SQL/catalog", out.String())
			}
		})
	}
}

func TestInitializationArtifactReaderIsBoundedRegularFile(t *testing.T) {
	root := t.TempDir()
	oversized := filepath.Join(root, "oversized")
	if err := os.WriteFile(oversized, make([]byte, datainit.ArtifactLimit+1), 0600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(oversized, linked); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{root, oversized, linked, filepath.Join(root, "missing")} {
		if _, err := readInitializerArtifact(filename); err == nil {
			t.Fatal("invalid artifact file accepted")
		}
	}
}
