package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/planfile"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

const offlineExamples = "../../examples/offline/"

type forbiddenOfflineRunner struct{}

func (forbiddenOfflineRunner) Run(context.Context, string, ...string) (string, error) {
	panic("offline command invoked exec adapter")
}

func offlineExecute(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LookPath = func(string) (string, error) { panic("offline command looked up host tool") }
	deps.DoctorRunner = forbiddenOfflineRunner{}
	deps.RunTUI = func(context.Context, io.Reader, io.Writer) error { panic("offline command invoked TUI") }
	err := Execute(deps, args)
	return out.String(), diagnostics.String(), result.ExitCode(err)
}

func offlineArgs(snapshot string) []string {
	return []string{"plan", offlineExamples + "brine.toml", "--offline", "--snapshot", offlineExamples + snapshot + ".json", "--policy", offlineExamples + "policy.toml"}
}

func offlineState(t *testing.T) string {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(offlineExamples + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	app, err := spec.Parse(read("brine.toml"))
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(read("policy.toml"))
	if err != nil {
		t.Fatal(err)
	}
	desired, err := policy.Normalize(app, pol)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(read("host-with-app.json"))
	if err != nil {
		t.Fatal(err)
	}
	current := (*snapshot.Apps.Value)[0]
	state := plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{{App: "hello", ID: "release-0001", Desired: desired, Image: *current.Image.Value, HostPort: *current.AllocatedHostPort.Value, Secrets: []plan.SecretBinding{}, Units: *current.QuadletUnits.Value, CaddyFile: snapshot.CaddyConfig.Value.Files[0]}}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(offlineExamples+"brine-state.json", append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOfflineCommandsEndToEnd(t *testing.T) {
	t.Setenv("PATH", "")
	invalid := filepath.Join(t.TempDir(), "invalid.toml")
	if err := os.WriteFile(invalid, []byte("schema_version = 1\nunknown = 'planted-private-value'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	refused := filepath.Join(t.TempDir(), "refused.toml")
	b, err := os.ReadFile(offlineExamples + "brine.toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(refused, bytes.ReplaceAll(b, []byte("ghcr.io"), []byte("registry.example.net")), 0600); err != nil {
		t.Fatal(err)
	}
	state := offlineState(t)
	tests := []struct {
		name     string
		args     []string
		exit     int
		contains string
	}{
		{"validate-ok", []string{"validate", offlineExamples + "brine.toml", "--policy", offlineExamples + "policy.toml"}, 0, "hello"},
		{"validate-invalid", []string{"validate", invalid, "--policy", offlineExamples + "policy.toml"}, 2, ""},
		{"validate-refused", []string{"validate", refused, "--policy", offlineExamples + "policy.toml"}, 4, ""},
		{"plan-create", offlineArgs("fresh-host"), 0, "create"},
		{"plan-no-op", append(offlineArgs("host-with-app"), "--state", state), 0, "no-op"},
		{"plan-conflict", offlineArgs("unenrolled-host"), 0, "conflict"},
		{"plan-state-required", offlineArgs("host-with-app"), 0, "conflict"},
		{"plan-connected-refused", []string{"plan", offlineExamples + "brine.toml", "--snapshot", offlineExamples + "fresh-host.json", "--policy", offlineExamples + "policy.toml"}, 2, ""},
	}
	for _, tt := range tests {
		for _, mode := range []string{"", "--json", "--jsonl"} {
			t.Run(tt.name+"/"+mode, func(t *testing.T) {
				args := append([]string{}, tt.args...)
				if mode != "" {
					args = append(args, mode)
				}
				out, diagnostics, exit := offlineExecute(t, args...)
				if exit != tt.exit {
					t.Fatalf("exit %d want %d, out %s, diagnostic %s", exit, tt.exit, out, diagnostics)
				}
				if strings.Contains(out+diagnostics, "planted-private-value") || strings.Contains(out+diagnostics, "fixture-setting-value") || strings.Contains(out+diagnostics, "\x1b") {
					t.Fatal("unsafe output")
				}
				if exit == 0 && (!strings.Contains(out, tt.contains) || diagnostics != "") {
					t.Fatalf("out %q diagnostics %q", out, diagnostics)
				}
				if mode == "" {
					if exit == 0 && tt.args[0] == "plan" && !strings.Contains(out, "OFFLINE PLAN — NOT APPLYABLE") {
						t.Fatal("missing offline warning")
					}
					return
				}
				var envelope result.Envelope
				dec := json.NewDecoder(strings.NewReader(out))
				if err := dec.Decode(&envelope); err != nil {
					t.Fatal(err)
				}
				if err := dec.Decode(new(any)); err != io.EOF {
					t.Fatal("extra machine output")
				}
				if envelope.OK != (exit == 0) {
					t.Fatal("wrong ok status")
				}
				golden := filepath.Join("testdata", tt.name+".json")
				if os.Getenv("UPDATE_GOLDEN") == "1" && mode == "--json" {
					if err := os.WriteFile(golden, []byte(out), 0644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				if out != string(want) {
					t.Fatalf("golden mismatch\ngot %s\nwant %s", out, want)
				}
			})
		}
	}
}

func TestOfflinePlanFileDeterministicAndPrivate(t *testing.T) {
	t.Setenv("PATH", "")
	dir := t.TempDir()
	var previous string
	for i := 0; i < 2; i++ {
		out, diagnostics, exit := offlineExecute(t, append(offlineArgs("fresh-host"), "--out", dir, "--json")...)
		if exit != 0 || diagnostics != "" {
			t.Fatalf("%d %s %s", exit, out, diagnostics)
		}
		if i > 0 && out != previous {
			t.Fatal("same inputs changed response")
		}
		previous = out
		var envelope struct {
			Data struct {
				Hash string `json:"hash"`
			}
		}
		if err := json.Unmarshal([]byte(out), &envelope); err != nil {
			t.Fatal(err)
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 {
			t.Fatalf("unexpected writes %v", files)
		}
		path := filepath.Join(dir, files[0].Name())
		document, err := planfile.Read(path)
		if err != nil {
			t.Fatal(err)
		}
		if document.Hash() != envelope.Data.Hash || document.Applyable() || files[0].Name() != strings.TrimPrefix(envelope.Data.Hash, "sha256:")+".plan.json" {
			t.Fatal("wrong offline file binding")
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatal("file is not private")
		}
	}
}

func TestOfflineOutPresentationsAndNoOverwrite(t *testing.T) {
	t.Setenv("PATH", "")
	for _, mode := range []string{"", "--json", "--jsonl"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			args := append(offlineArgs("fresh-host"), "--out", dir)
			if mode != "" {
				args = append(args, mode)
			}
			out, diagnostics, exit := offlineExecute(t, args...)
			if exit != 0 || diagnostics != "" {
				t.Fatalf("exit %d %s %s", exit, out, diagnostics)
			}
			files, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 {
				t.Fatal(files)
			}
			path := filepath.Join(dir, files[0].Name())
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(before, []byte("fixture-setting-value")) {
				t.Fatal("file lost verification input")
			}
			if strings.Contains(out, "fixture-setting-value") || strings.Contains(out, `"inputs"`) || strings.Contains(out, `"metadata"`) {
				t.Fatal("offline envelope leaked into presentation")
			}
			if mode == "" {
				if !strings.Contains(out, "Plan file:") || !strings.Contains(out, path) || !strings.Contains(out, "Plan hash: sha256:") {
					t.Fatal(out)
				}
			} else {
				want, err := os.ReadFile("testdata/plan-create.json")
				if err != nil {
					t.Fatal(err)
				}
				if out != string(want) {
					t.Fatal("--out changed machine projection")
				}
			}
			_, _, exit = offlineExecute(t, args...)
			if exit != 0 {
				t.Fatal("retry refused")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("retry rewrote evidence metadata")
			}
			corrupt := bytes.ReplaceAll(before, []byte("fixture-setting-value"), []byte("tampered-input-value"))
			if err := os.WriteFile(path, corrupt, 0600); err != nil {
				t.Fatal(err)
			}
			_, _, exit = offlineExecute(t, args...)
			if exit != 5 {
				t.Fatalf("tamper exit %d", exit)
			}
			after, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(corrupt, after) {
				t.Fatal("corrupt file was overwritten")
			}
		})
	}
}

func TestOfflineInvalidInputsNeverWrite(t *testing.T) {
	t.Setenv("PATH", "")
	dir := t.TempDir()
	invalidJSON := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalidJSON, []byte(`{"private":"planted-private-value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	invalidPolicy := filepath.Join(t.TempDir(), "invalid-policy.toml")
	if err := os.WriteFile(invalidPolicy, []byte(`private = "planted-private-value"`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"", "--json", "--jsonl"} {
		for _, name := range []string{"name", "domain", "host-injection", "unpinned", "path-mount", "hook", "expansion", "unknown"} {
			args := offlineArgs("fresh-host")
			args[1] = "../spec/testdata/" + name + ".toml"
			args = append(args, "--out", dir)
			if mode != "" {
				args = append(args, mode)
			}
			out, diagnostics, exit := offlineExecute(t, args...)
			if exit != 2 {
				t.Fatalf("%s %d %s %s", name, exit, out, diagnostics)
			}
		}
		for _, tt := range []struct {
			flag, value string
			exit        int
		}{{"--snapshot", invalidJSON, 2}, {"--state", invalidJSON, 2}, {"--policy", invalidPolicy, 4}, {"--state", "", 2}, {"--snapshot", "", 2}, {"--policy", "", 2}} {
			args := append(offlineArgs("fresh-host"), tt.flag, tt.value, "--out", dir)
			if mode != "" {
				args = append(args, mode)
			}
			out, diagnostics, exit := offlineExecute(t, args...)
			if exit != tt.exit {
				t.Fatalf("%s %d %s %s", tt.flag, exit, out, diagnostics)
			}
			if strings.Contains(out+diagnostics, "planted-private-value") {
				t.Fatal("unsafe diagnostic")
			}
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("invalid inputs wrote files")
	}
}

func TestOfflineArgumentAndWriteFailures(t *testing.T) {
	t.Setenv("PATH", "")
	for _, args := range [][]string{{"validate"}, {"validate", offlineExamples + "brine.toml"}, {"validate", offlineExamples + "brine.toml", "extra", "--policy", offlineExamples + "policy.toml"}, append(offlineArgs("fresh-host"), "--out", ""), append(offlineArgs("fresh-host"), "--offline=false")} {
		_, _, exit := offlineExecute(t, append(args, "--json")...)
		if exit != 2 {
			t.Fatalf("args %v exit %d", args, exit)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	_, _, exit := offlineExecute(t, append(offlineArgs("fresh-host"), "--out", missing, "--json")...)
	if exit != 1 {
		t.Fatal(exit)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("command created output directory")
	}
}

func TestOfflineFixtureStateMatchesGeneratedState(t *testing.T) {
	generated, err := os.ReadFile(offlineState(t))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(offlineExamples + "brine-state.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(generated), bytes.TrimSpace(fixture)) {
		t.Fatal("example committed state is stale")
	}
	out, diagnostics, exit := offlineExecute(t, append(offlineArgs("host-with-app"), "--state", offlineExamples+"brine-state.json", "--json")...)
	if exit != 0 || diagnostics != "" || !strings.Contains(out, `"kind":"no-op"`) {
		t.Fatalf("%d %s %s", exit, out, diagnostics)
	}
}

func TestOfflineStateSchemaRefusals(t *testing.T) {
	t.Setenv("PATH", "")
	valid, err := os.ReadFile(offlineState(t))
	if err != nil {
		t.Fatal(err)
	}
	tests := [][]byte{[]byte("null"), []byte("{}"), append(append([]byte{}, valid...), []byte(" {}")...), bytes.Replace(valid, []byte(`"releases":[`), []byte(`"Releases":[`), 1), bytes.Replace(valid, []byte(`"generation":4`), []byte(`"generation":4,"generation":4`), 1), bytes.Replace(valid, []byte(`"environment":[`), []byte(`"environment":null,"ignored":[`), 1)}
	for i, data := range tests {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		out, diagnostics, exit := offlineExecute(t, append(offlineArgs("host-with-app"), "--state", path, "--json")...)
		if exit != 2 {
			t.Fatalf("case %d exit %d %s %s", i, exit, out, diagnostics)
		}
	}
}

func TestOfflineReadOnlyAndCancellation(t *testing.T) {
	entries, err := os.ReadDir(offlineExamples)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, entry := range entries {
		b, err := os.ReadFile(offlineExamples + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		before[entry.Name()] = b
	}
	for _, args := range [][]string{{"validate", offlineExamples + "brine.toml", "--policy", offlineExamples + "policy.toml"}, offlineArgs("fresh-host"), append(offlineArgs("host-with-app"), "--state", offlineExamples+"brine-state.json")} {
		_, _, exit := offlineExecute(t, args...)
		if exit != 0 {
			t.Fatal(exit)
		}
	}
	after, err := os.ReadDir(offlineExamples)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatal("read-only commands created files")
	}
	for _, entry := range after {
		b, err := os.ReadFile(offlineExamples + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, before[entry.Name()]) {
			t.Fatal("read-only command changed input")
		}
	}
	dir := t.TempDir()
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deps.Context = ctx
	err = Execute(deps, append(offlineArgs("fresh-host"), "--out", dir, "--json"))
	if result.ExitCode(err) != 130 {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("cancelled command wrote files")
	}
}
