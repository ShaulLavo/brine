//go:build linux

package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"golang.org/x/sys/unix"
)

func nativeTestPin() nativeReaderPin {
	return nativeReaderPin{Format: "brine-native-reader-pin-v1", SourceHead: strings.Repeat("a", 40), UpstreamCommit: nativeevidence.UpstreamCommit, Architecture: "amd64", Image: nativeReaderRepository + "@sha256:" + strings.Repeat("b", 64), ManifestDigest: "sha256:" + strings.Repeat("c", 64), ImageID: strings.Repeat("d", 64), ExecutableSHA256: strings.Repeat("e", 64)}
}

func TestNativeReaderPinIsClosedAndArchitectureBound(t *testing.T) {
	raw, err := json.Marshal(nativeTestPin())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNativeReaderPin(raw, "amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNativeReaderPin(raw, "arm64"); err == nil {
		t.Fatal("cross-architecture artifact admitted")
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), `"format":`, `"unknown":0,"format":`, 1),
		strings.Replace(string(raw), `"architecture":"amd64"`, `"architecture":null`, 1),
		strings.Replace(string(raw), `"source_head":`, `"source_head":null,"source_head":`, 1),
		strings.Replace(string(raw), nativeReaderRepository, "foreign/image", 1),
	} {
		if _, err := decodeNativeReaderPin([]byte(invalid), "amd64"); err == nil {
			t.Fatal("untrusted artifact pin admitted")
		}
	}
}

func nativeViewFixture(t *testing.T) (*os.Root, nativecapture.Selection, nativecapture.Checkpoint) {
	t.Helper()
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	name := "replicas/0/snapshots/00000000000000000000.snapshot_dir/00000000000000000000.snapshot_bsatn"
	if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	history := []byte("synthetic history, no replay claim")
	if err := root.WriteFile(name, history, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := engineOpenMember(root, name, unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	member, err := engineMember(file, name)
	if err != nil || member.Size != int64(len(history)) {
		t.Fatal(err)
	}
	member.SHA256, err = hashEngineFile(context.Background(), file, member)
	if err != nil {
		t.Fatal(err)
	}
	scope := nativecapture.Scope{BindingID: strings.Repeat("a", 32), EpochID: strings.Repeat("b", 32), CaptureID: strings.Repeat("c", 32), ReplicaID: 0}
	recorded := nativecapture.Member{Path: name, Bytes: uint64(len(history)), ExpandedBytes: uint64(len(history)), SHA256: member.SHA256, Device: member.Device, Inode: member.Inode, MountID: member.MountID, UID: member.UID, Mode: 0600, Links: uint64(member.Links)}
	inventory, err := nativecapture.NewInventory(scope, []nativecapture.Member{recorded}, nativecapture.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	selection, err := nativecapture.Select(inventory, nativecapture.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := nativecapture.NewCheckpoint(nativecapture.CheckpointRecord{Format: nativecapture.Format, Scope: nativecapture.CheckpointScope{BindingID: scope.BindingID, EpochID: scope.EpochID, ReplicaID: 0}, GenerationID: strings.Repeat("d", 32), DatabaseIdentity: strings.Repeat("1", 64), PublisherIdentity: strings.Repeat("2", 64), Names: []string{"fixture"}, RuntimeModuleHash: strings.Repeat("3", 64), ArtifactSHA256: strings.Repeat("4", 64), SchemaVersion: spacetimedb.CanonicalizerVersion, SchemaSHA256: strings.Repeat("5", 64), ConfigurationSHA256: strings.Repeat("6", 64), SigningRecovery: nativecapture.SigningRecovery{Kind: "external_immutable", Reference: "fixture", Version: 1, SHA256: strings.Repeat("7", 64)}, Members: []nativecapture.CheckpointMember{{Path: "config.toml", Bytes: 1, SHA256: strings.Repeat("6", 64)}}}, nativecapture.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return root, selection, checkpoint
}

func TestNativeViewClosePreservesReplacedDirectory(t *testing.T) {
	root, selection, _ := nativeViewFixture(t)
	staging, nonce := t.TempDir(), strings.Repeat("f", 32)
	view, err := materializeNativeSelection(context.Background(), root, staging, selection, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if err := view.parent.Rename(nonce, "retained"); err != nil {
		t.Fatal(err)
	}
	if err := view.parent.Mkdir(nonce, 0700); err != nil {
		t.Fatal(err)
	}
	if err := view.parent.WriteFile(nonce+"/marker", []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if view.close() == nil {
		t.Fatal("replaced directory was admitted for cleanup")
	}
	if _, err := view.parent.Stat(nonce + "/marker"); err != nil {
		t.Fatal("replacement data removed", err)
	}
	if err := view.parent.RemoveAll(nonce); err != nil {
		t.Fatal(err)
	}
	if err := view.parent.Rename("retained", nonce); err != nil {
		t.Fatal(err)
	}
	if err := view.close(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeViewOwnsSelectedOnlyAndZeroSnapshotContext(t *testing.T) {
	root, selection, checkpoint := nativeViewFixture(t)
	if err := root.WriteFile("config.toml", []byte("private configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	view, err := materializeNativeSelection(context.Background(), root, staging, selection, strings.Repeat("d", 32))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = view.close() }()
	if _, err := view.root.Stat("config.toml"); !os.IsNotExist(err) {
		t.Fatal("configuration entered reader view")
	}
	if view.unchanged(context.Background()) != nil {
		t.Fatal("private view not sealed")
	}
	if _, _, err := readNativeView(context.Background(), localexec.Session{}, nativeTestPin(), view, checkpoint); err == nil {
		t.Fatal("uninstalled or mismatched protected reader pin admitted")
	}
	input, err := nativeReaderInput(checkpoint, selection)
	if err != nil || !strings.Contains(string(input), `"offset":0`) || !strings.Contains(string(input), `"replica_id":0`) {
		t.Fatal("zero snapshot context omitted", err)
	}
	args, err := nativeReaderArguments(nativeTestPin(), view)
	if err != nil || strings.Contains(strings.Join(args, "\n"), "TOKIO_WORKER_THREADS") || !strings.Contains(strings.Join(args, "\n"), "/copy/replica:rw") || !strings.Contains(strings.Join(args, "\n"), "--read-only-tmpfs=false") || !strings.Contains(strings.Join(args, "\n"), "--passwd=false") {
		t.Fatal("invalid fixed passive argv", err)
	}
	view.state = nativeViewReading
	if err := view.close(); err == nil {
		t.Fatal("unknown reader presence allowed stage deletion")
	}
	view.state = nativeViewDisposed
	if _, err := materializeNativeSelection(context.Background(), root, staging, selection, strings.Repeat("d", 32)); err == nil {
		t.Fatal("existing view replaced")
	}
	if view.unchanged(context.Background()) != nil {
		t.Fatal("collision removed original view")
	}
}
