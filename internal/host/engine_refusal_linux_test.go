//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

func engineRefusalFixture(t *testing.T) (*EngineStartupRuntime, data.ReplicaBindingID) {
	t.Helper()
	stateRoot := t.TempDir()
	state, err := store.Open(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	permit, _ := engineStartupFixture(t)
	binding := permit.Reserved.Database
	epoch := data.ReplicaEpochID(strings.Repeat("d", 32))
	destination := data.Destination{Reference: "fixture", Endpoint: "https://storage.example.invalid", Region: "fixture", Bucket: "fixture", BasePrefix: "fixture", CredentialRef: "fixture"}
	prefix, err := data.RemotePrefix(destination.BasePrefix, binding.IncarnationID, binding.DatabaseID, epoch)
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.ReserveEngine(t.Context(), store.EngineReservation{App: "fixture", PolicyHash: "sha256:" + strings.Repeat("a", 64), Database: binding, Archive: data.ArchiveBinding{BindingID: binding.BindingID, DatabaseID: binding.DatabaseID, Destination: destination, EpochID: epoch, RemotePrefix: prefix}})
	if err != nil {
		t.Fatal(err)
	}
	return &EngineStartupRuntime{state: state, stateRoot: stateRoot}, binding.BindingID
}

func refusalFile(root string, binding data.ReplicaBindingID) string {
	return filepath.Join(root, "engine-refusals", string(binding)+".json")
}

func TestEngineRefusalReceiptIsPrivateBoundedAndAtomic(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil { //nolint:gosec // Private receipt fixture.
		t.Fatal(err)
	}
	binding := data.ReplicaBindingID(strings.Repeat("a", 32))
	first := engineRefusalReceipt{Version: 1, Code: engineRefusalManager, At: time.Now().UTC()}
	if err := writeEngineRefusal(t.Context(), root, binding, first); err != nil {
		t.Fatal(err)
	}
	name := refusalFile(root, binding)
	held, err := os.Open(name) //nolint:gosec // Known fixture binding receipt under the private test root.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	before, err := held.Stat()
	if err != nil || before.Mode().Perm() != 0600 || before.Size() > 4096 {
		t.Fatal("receipt permissions or quota", err)
	}
	second := engineRefusalReceipt{Version: 1, Code: engineRefusalMaterial, At: time.Now().UTC()}
	if err = writeEngineRefusal(t.Context(), root, binding, second); err != nil {
		t.Fatal(err)
	}
	current, err := os.Stat(name)
	if err != nil || os.SameFile(before, current) {
		t.Fatal("replacement was not atomic", err)
	}
	var old engineRefusalReceipt
	if err = json.NewDecoder(held).Decode(&old); err != nil || old.Code != first.Code {
		t.Fatal("replacement changed held prior receipt", err)
	}
	raw, err := os.ReadFile(name) //nolint:gosec // Owned receipt path selected by this fixture.
	if err != nil || len(raw) > 4096 {
		t.Fatal(err)
	}
	var next engineRefusalReceipt
	if err = json.Unmarshal(raw, &next); err != nil || next.Code != second.Code {
		t.Fatal("new receipt unavailable", err)
	}
}

func TestEngineRefusalReceiptRejectsUnsafeTargetsAndCodes(t *testing.T) {
	for _, attack := range []string{"symlink-root", "symlink-file", "public-file", "hardlink", "oversize", "fifo", "directory", "unknown-code"} {
		t.Run(attack, func(t *testing.T) { testEngineRefusalAttack(t, attack) })
	}
}

func testEngineRefusalAttack(t *testing.T, attack string) {
	t.Helper()
	root := t.TempDir()
	parent := filepath.Join(root, "engine-refusals")
	if err := os.Chmod(root, 0700); err != nil { //nolint:gosec // Private receipt fixture.
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	binding := data.ReplicaBindingID(strings.Repeat("a", 32))
	name := refusalFile(root, binding)
	other := filepath.Join(t.TempDir(), "retained")
	if err := os.WriteFile(other, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	receipt := engineRefusalReceipt{Version: 1, Code: engineRefusalUnit, At: time.Now().UTC()}
	switch attack {
	case "symlink-root":
		if err := os.Remove(parent); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Dir(other), parent); err != nil {
			t.Fatal(err)
		}
	case "symlink-file":
		if err := os.Symlink(other, name); err != nil {
			t.Fatal(err)
		}
	case "public-file":
		if err := os.WriteFile(name, []byte("unsafe"), 0644); err != nil { //nolint:gosec // Deliberately unsafe receipt fixture.
			t.Fatal(err)
		}
	case "hardlink":
		if err := os.Link(other, name); err != nil {
			t.Fatal(err)
		}
	case "oversize":
		if err := os.WriteFile(name, bytes.Repeat([]byte("x"), 4097), 0600); err != nil {
			t.Fatal(err)
		}
	case "fifo":
		if err := unix.Mkfifo(name, 0600); err != nil {
			t.Fatal(err)
		}
	case "directory":
		if err := os.Mkdir(name, 0700); err != nil {
			t.Fatal(err)
		}
	case "unknown-code":
		receipt.Code = engineRefusalCode("secret-value /private/path")
	}
	if writeEngineRefusal(context.Background(), root, binding, receipt) == nil {
		t.Fatal("unsafe target or arbitrary code accepted")
	}
	raw, err := os.ReadFile(other) //nolint:gosec // Parent-created retained marker under this test's temporary root.
	if err != nil || string(raw) != "retained" {
		t.Fatal("receipt touched unrelated data", err)
	}
}

func TestEngineRefusalCanceledWriteHasNoEffect(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	receipt := engineRefusalReceipt{Version: 1, Code: engineRefusalUnit, At: time.Now().UTC()}
	if writeEngineRefusal(ctx, root, data.ReplicaBindingID(strings.Repeat("a", 32)), receipt) == nil {
		t.Fatal("canceled diagnostic accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "engine-refusals")); !os.IsNotExist(err) {
		t.Fatal("canceled diagnostic changed state", err)
	}
}

func TestEngineRefusalCodePreservesFailureAndScrubsDetails(t *testing.T) {
	cause := errors.New("private token and /private/path")
	refused := engineRefusal(engineRefusalMaterial, cause)
	if !errors.Is(refused, cause) || strings.Contains(refused.Error(), "private") {
		t.Fatal("failure was replaced or private detail rendered")
	}
	for _, kind := range []localexec.ErrorKind{localexec.Failed, localexec.Timeout, localexec.UnknownOutcome} {
		if !enginePrivateRunCode(&localexec.Error{Kind: kind}).valid() {
			t.Fatal("subprocess classification escaped closed codes")
		}
	}
}

func TestEngineRefusalRecordingPreservesFailureAndLastRefusal(t *testing.T) {
	runtime, binding := engineRefusalFixture(t)
	err := runtime.EngineExec(t.Context(), string(binding))
	if !errors.Is(err, errEngineSource) {
		t.Fatal("missing runtime failure was hidden", err)
	}
	name := refusalFile(runtime.stateRoot, binding)
	before, err := os.ReadFile(name) //nolint:gosec // Known fixture binding's fixed receipt path.
	if err != nil {
		t.Fatal(err)
	}
	var receipt engineRefusalReceipt
	if err = json.Unmarshal(before, &receipt); err != nil || receipt.Code != engineRefusalPermitRead {
		t.Fatal("actual failing proof was not recorded", err)
	}
	runtime.recordEngineRefusal(t.Context(), binding, nil)
	current, err := os.ReadFile(name) //nolint:gosec // Known fixture binding's fixed receipt path.
	if err != nil || !bytes.Equal(before, current) {
		t.Fatal("later success cleared prior refusal", err)
	}
	unknown := data.ReplicaBindingID(strings.Repeat("f", 32))
	runtime.recordEngineRefusal(t.Context(), unknown, engineRefusal(engineRefusalUnit, errEngineSource))
	if _, err = os.Stat(refusalFile(runtime.stateRoot, unknown)); !os.IsNotExist(err) {
		t.Fatal("unknown binding gained a receipt", err)
	}
	if err = os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Dir(name)); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(t.TempDir(), filepath.Dir(name)); err != nil {
		t.Fatal(err)
	}
	if err = runtime.EngineExec(t.Context(), string(binding)); !errors.Is(err, errEngineSource) {
		t.Fatal("diagnostic failure changed original refusal", err)
	}
}
