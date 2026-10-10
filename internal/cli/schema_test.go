package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestSchemaExportAndConnectedPlanDigest(t *testing.T) {
	raw, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n[runtime]\nuid=10001\ngid=10001\n[[databases]]\nname=\"main\"\npersistent_root=\"/srv/brine-data\"\nmount_path=\"/data\"\nfilename=\"app.db\"\nbackup_destination=\"primary\"\nschema=\"schema.sql\"\n")...)
	dir := t.TempDir()
	path := filepath.Join(dir, "brine.toml")
	if err := os.WriteFile(path, raw, 0600); err != nil { // #nosec G703 -- Fixed fixture filename within the test-owned temporary directory.
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schema.sql"), []byte("CREATE TABLE t(x TEXT);"), 0600); err != nil {
		t.Fatal(err)
	}
	var exported, diagnostics bytes.Buffer
	deps := testDependencies(t, &exported, &diagnostics)
	if err := Execute(deps, []string{"data", "schema", path, "--database", "main"}); err != nil {
		t.Fatal(err)
	}
	var initializer data.SchemaInitializer
	if err := json.Unmarshal(exported.Bytes(), &initializer); err != nil {
		t.Fatal(err)
	}
	if err := initializer.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(exported.Bytes())
	digest := "sha256:" + hex.EncodeToString(sum[:])
	for _, mode := range []string{"--json", "--jsonl"} {
		var out bytes.Buffer
		deps := testDependencies(t, &out, &diagnostics)
		if err := Execute(deps, []string{"data", "schema", path, "--database", "main", mode}); err != nil {
			t.Fatal(err)
		}
		var response struct {
			OK   bool `json:"ok"`
			Data struct {
				Bytes    string              `json:"bytes"`
				Artifact data.SchemaArtifact `json:"artifact"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out.Bytes(), &response); err != nil || !response.OK || response.Data.Bytes != exported.String() || response.Data.Artifact.Digest != digest {
			t.Fatalf("artifact machine envelope changed exact bytes: %s %v", out.String(), err)
		}
	}
	for _, operation := range []string{"plan", "data_prepare_plan"} {
		for _, mode := range []string{"", "--json"} {
			var out bytes.Buffer
			deps := testDependencies(t, &out, &diagnostics)
			deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{}, nil }
			deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
				var args dispatch.PlanArgs
				if err := json.Unmarshal(request.Args, &args); err != nil {
					t.Fatal(err)
				}
				if len(args.SchemaArtifacts) != 1 || args.SchemaArtifacts[0].Digest != digest || strings.Contains(string(request.Args), "CREATE TABLE") || strings.Contains(string(request.Args), "statements") {
					t.Fatal("wrong artifact evidence or executable SQL sent to host")
				}
				return result.Success("brine host "+operation, dispatch.Planned{PlanID: "sha256:" + strings.Repeat("a", 64), Kind: plan.Create, Conflicts: []plan.Diagnostic{}, SchemaArtifacts: args.SchemaArtifacts}), nil
			})
			args := []string{"plan", path, "--target", "fixture"}
			if operation == "data_prepare_plan" {
				args = []string{"data", "prepare", path, "--target", "fixture"}
			}
			if mode != "" {
				args = append(args, mode)
			}
			if err := Execute(deps, args); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), digest) {
				t.Fatalf("derived digest missing from plan: %s", out.String())
			}
		}
	}
}

func TestClientSchemaRefusalShowsLineWithoutSQL(t *testing.T) {
	raw, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n[runtime]\nuid=10001\ngid=10001\n[[databases]]\nname=\"main\"\npersistent_root=\"/srv/brine-data\"\nmount_path=\"/data\"\nfilename=\"app.db\"\nbackup_destination=\"primary\"\nschema=\"schema.sql\"\n")...)
	dir := t.TempDir()
	filename := filepath.Join(dir, "brine.toml")
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	} // #nosec G703 -- Fixed fixture filename in a test-owned directory.
	if err := os.WriteFile(filepath.Join(dir, "schema.sql"), []byte("-- header\n\nINSERT INTO planted_private_sql VALUES(1);"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"data", "schema", filename, "--database", "main"}, {"plan", filename, "--target", "fixture", "--json"}, {"data", "prepare", filename, "--target", "fixture"}} {
		var out, diagnostics bytes.Buffer
		deps := testDependencies(t, &out, &diagnostics)
		if err := Execute(deps, args); result.ExitCode(err) != 2 {
			t.Fatalf("not refused: %v", err)
		}
		if !strings.Contains(diagnostics.String(), "schema line 3:") || strings.Contains(out.String()+diagnostics.String(), "planted_private_sql") {
			t.Fatalf("unsafe or missing diagnostic: %s %s", out.String(), diagnostics.String())
		}
	}
}
