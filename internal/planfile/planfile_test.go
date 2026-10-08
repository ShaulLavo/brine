package planfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func fixture(t *testing.T) plan.Input {
	t.Helper()
	read := func(path string) []byte {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	snapshot, err := target.Decode(read("../target/testdata/ready-arm64.json"))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := policy.Parse(bytes.ReplaceAll(read("../policy/testdata/operator.toml"), []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(bytes.ReplaceAll(read("../spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello")))
	if err != nil {
		t.Fatal(err)
	}
	desired, err := policy.Normalize(app, rules)
	if err != nil {
		t.Fatal(err)
	}
	return plan.Input{Desired: desired, Snapshot: snapshot, Image: target.Image{Digest: strings.Split(string(desired.Image), "@")[1], Platform: target.Platform{OS: "linux", Arch: snapshot.Arch}}, State: plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{}}}
}
func offline(t *testing.T) Offline {
	t.Helper()
	p, err := New(fixture(t), Metadata{CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), ToolVersion: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestRoundTrip(t *testing.T) {
	p := offline(t)
	dir := t.TempDir()
	path, err := Write(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != strings.TrimPrefix(p.Hash(), "sha256:")+".plan.json" {
		t.Fatal(path)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v %v", info, err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, _ := json.Marshal(p)
	gotBytes, _ := json.Marshal(got)
	if !bytes.Equal(wantBytes, gotBytes) || got.Applyable() || got.Reason() != "offline" {
		t.Fatalf("round trip mismatch")
	}
	if got.Plan().Hash != p.Hash() || got.Metadata() != p.Metadata() {
		t.Fatal("metadata or hash mismatch")
	}
	again, err := Write(dir, got)
	if err != nil || again != path {
		t.Fatalf("idempotent write: %v", err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(info, after) {
		t.Fatal("identical rewrite replaced original")
	}
}
func TestTamper(t *testing.T) {
	p := offline(t)
	path, err := Write(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	b = bytes.Replace(b, []byte(`"app":"hello"`), []byte(`"app":"jello"`), 1)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = Read(path)
	var integrity *IntegrityError
	if !errors.As(err, &integrity) {
		t.Fatalf("want integrity error, got %v", err)
	}
}
func TestWrongSchema(t *testing.T) {
	b, _ := json.Marshal(offline(t))
	b = bytes.Replace(b, []byte(`"schema_version":1`), []byte(`"schema_version":99`), 1)
	_, err := Decode(b)
	var version *SchemaError
	if !errors.As(err, &version) {
		t.Fatalf("want schema error, got %v", err)
	}
}
func TestOverwriteConflict(t *testing.T) {
	p := offline(t)
	dir := t.TempDir()
	path, err := Write(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	changed, err := New(fixture(t), Metadata{CreatedAt: p.Metadata().CreatedAt.Add(time.Second), ToolVersion: "0.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Hash() != p.Hash() {
		t.Fatal("creation metadata entered hash")
	}
	_, err = Write(dir, changed)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(original, after) {
		t.Fatal("conflict overwrote original")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("temporary file leaked")
	}
}
func TestStrictDecode(t *testing.T) {
	b, _ := json.Marshal(offline(t))
	tests := map[string][]byte{
		"applyable":         bytes.Replace(b, []byte(`"applyable":false`), []byte(`"applyable":true`), 1),
		"missing applyable": bytes.Replace(b, []byte(`"applyable":false,`), nil, 1),
		"null applyable":    bytes.Replace(b, []byte(`"applyable":false`), []byte(`"applyable":null`), 1),
		"reason":            bytes.Replace(b, []byte(`"reason":"offline"`), []byte(`"reason":"connected"`), 1),
		"unknown":           append([]byte(`{"unknown":1,`), b[1:]...),
		"duplicate":         append([]byte(`{"applyable":true,`), b[1:]...),
		"trailing":          append(bytes.Clone(b), []byte(` {}`)...),
		"secret value":      bytes.Replace(b, []byte(`"secrets":[]`), []byte(`"secrets":[{"environment":"TOKEN","reference":"hello-token","version_name":"brine-hello-hello-token-v1","id":"opaque","value":"planted-secret-do-not-export"}]`), 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(raw)
			if err == nil {
				t.Fatal("accepted invalid envelope")
			}
			if strings.Contains(err.Error(), "planted-secret-do-not-export") {
				t.Fatal("secret leaked in error")
			}
		})
	}
	if bytes.Contains(b, []byte("planted-secret-do-not-export")) {
		t.Fatal("secret value serialized")
	}
}
func TestZeroValue(t *testing.T) {
	if _, err := Write(t.TempDir(), Offline{}); err == nil {
		t.Fatal("wrote zero value")
	}
}

var errInjected = errors.New("injected write failure")

type failingFile struct {
	*os.File
	stage string
	final string
	t     *testing.T
}

func (f *failingFile) Write(b []byte) (int, error) {
	if _, err := os.Stat(f.final); !os.IsNotExist(err) {
		f.t.Fatalf("final file visible before publication: %v", err)
	}
	if f.stage == "write" {
		n, err := f.File.Write(b[:len(b)/2])
		if err != nil {
			return n, err
		}
		return n, errInjected
	}
	if f.stage == "short write" {
		return f.File.Write(b[:len(b)/2])
	}
	return f.File.Write(b)
}
func (f *failingFile) Sync() error {
	if f.stage == "sync" {
		return errInjected
	}
	return f.File.Sync()
}
func (f *failingFile) Close() error {
	err := f.File.Close()
	if f.stage == "close" {
		return errInjected
	}
	return err
}
func TestAtomicFailure(t *testing.T) {
	for _, stage := range []string{"create", "write", "short write", "sync", "close", "rename"} {
		t.Run(stage, func(t *testing.T) {
			p := offline(t)
			dir := t.TempDir()
			final := filepath.Join(dir, p.Filename())
			ops := writeOps{createTemp: func(dir, pattern string) (temporaryFile, error) {
				if stage == "create" {
					return nil, errInjected
				}
				f, err := os.CreateTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				return &failingFile{File: f, stage: stage, final: final, t: t}, nil
			}, publish: func(oldPath, newPath string) error {
				if stage == "rename" {
					return errInjected
				}
				return publish(oldPath, newPath)
			}}
			if _, err := writeWith(dir, p, ops); err == nil {
				t.Fatal("injected failure succeeded")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("partial file left behind: %v", entries)
			}
			if _, err := Write(dir, p); err != nil {
				t.Fatalf("retry failed: %v", err)
			}
		})
	}
}
func TestConcurrentPublication(t *testing.T) {
	p := offline(t)
	dir := t.TempDir()
	results := make(chan error, 12)
	for range cap(results) {
		go func() { _, err := Write(dir, p); results <- err }()
	}
	for range cap(results) {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("publication left %d entries", len(entries))
	}
	if _, err := Read(filepath.Join(dir, p.Filename())); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentConflict(t *testing.T) {
	first := offline(t)
	second, err := New(fixture(t), Metadata{CreatedAt: first.Metadata().CreatedAt.Add(time.Second), ToolVersion: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	results := make(chan error, 2)
	for _, p := range []Offline{first, second} {
		go func() { _, err := Write(dir, p); results <- err }()
	}
	success, conflicts := 0, 0
	for range 2 {
		err := <-results
		var conflict *ConflictError
		switch {
		case err == nil:
			success++
		case errors.As(err, &conflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("got %d successes and %d conflicts", success, conflicts)
	}
	if _, err := Read(filepath.Join(dir, first.Filename())); err != nil {
		t.Fatal(err)
	}
}
func TestVerificationInputs(t *testing.T) {
	p := offline(t)
	raw, _ := json.Marshal(p)
	for _, field := range []string{"hash", "snapshot_identity", "snapshot_generation", "inputs"} {
		t.Run(field, func(t *testing.T) {
			var document map[string]json.RawMessage
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "hash":
				document[field] = json.RawMessage(`"sha256:` + strings.Repeat("f", 64) + `"`)
			case "snapshot_identity":
				document[field] = json.RawMessage(`{}`)
			case "snapshot_generation":
				document[field] = json.RawMessage(`{"status":"known","value":999}`)
			case "inputs":
				var input map[string]json.RawMessage
				_ = json.Unmarshal(document[field], &input)
				var state map[string]json.RawMessage
				_ = json.Unmarshal(input["brine_state"], &state)
				state["generation"] = json.RawMessage(`999`)
				input["brine_state"], _ = json.Marshal(state)
				document[field], _ = json.Marshal(input)
			}
			b, _ := json.Marshal(document)
			_, err := Decode(b)
			var integrity *IntegrityError
			if !errors.As(err, &integrity) {
				t.Fatalf("want integrity error, got %v", err)
			}
		})
	}
	t.Run("filename", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "wrong.plan.json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Read(path)
		var integrity *IntegrityError
		if !errors.As(err, &integrity) {
			t.Fatalf("want integrity error, got %v", err)
		}
	})
	t.Run("immutable", func(t *testing.T) {
		copy := p.Plan()
		copy.Hash = "changed"
		copy.Changes[0].Allocation.App = "changed"
		after, _ := json.Marshal(p)
		if !bytes.Equal(raw, after) {
			t.Fatal("caller mutation changed offline evidence")
		}
	})
	t.Run("metadata excluded", func(t *testing.T) {
		var document map[string]json.RawMessage
		_ = json.Unmarshal(raw, &document)
		document["metadata"] = json.RawMessage(`{"created_at":"2026-01-03T00:00:00Z","tool_version":"0.1.2"}`)
		b, _ := json.Marshal(document)
		got, err := Decode(b)
		if err != nil || got.Hash() != p.Hash() {
			t.Fatalf("metadata changed fingerprint: %v", err)
		}
	})
}
func TestInvalidBoundaries(t *testing.T) {
	if _, err := New(fixture(t), Metadata{}); err == nil {
		t.Fatal("accepted missing creation metadata")
	}
	if _, err := Decode(bytes.Repeat([]byte(" "), MaxFileBytes+1)); err == nil {
		t.Fatal("accepted oversized file")
	}
	p := offline(t)
	dir := t.TempDir()
	path := filepath.Join(dir, p.Filename())
	outside := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	_, err := Write(dir, p)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("accepted symlink: %v", err)
	}
	after, _ := os.ReadFile(outside)
	if string(after) != "untouched" {
		t.Fatal("overwrote symlink destination")
	}
}
