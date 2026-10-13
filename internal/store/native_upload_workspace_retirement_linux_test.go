//go:build linux

package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestIssuedUploadWorkspaceRetiresBothRetainedJournals(t *testing.T) {
	ctx := context.Background()
	root := nativeForeignRoot(t)
	s, prep, life, op := nativePreparationFixtureAtRoot(t, root)
	if _, err := s.RecordNativeStoppedPreparation(ctx, prep, life, nativeAbsentStopFixture()); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTask(ctx, op.ID, ops.RecoveryRequired, ops.TaskOutcome{Error: result.InternalError}); err != nil {
		t.Fatal(err)
	}
	if err := prep.preparation.Close(); err != nil {
		t.Fatal(err)
	}
	i, err := s.OpenNativeBackupInspection(ctx, op.ID, op.Requester)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = i.Close() }()
	permit, err := s.ReadEnginePermit(ctx, prep.intent.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "native-restores", strings.Repeat("e", 32))
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	id, err := i.RecordWorkspace(ctx, parent, path)
	if err != nil {
		t.Fatal(err)
	}
	source, manifest := retainedUploadWorkspaceBundleFixture(t, permit, prep.PointID(), parent, path)
	owned, err := i.Workspaces(ctx)
	if err != nil || len(owned) != 1 {
		t.Fatal(err)
	}
	sourceRaw, _ := json.Marshal(source)
	descriptor := nativeUploadDescriptor{Format: "brine-native-upload-descriptor-v1", OperationID: op.ID, PointID: prep.PointID(), Workspace: filepath.Join("native-restores", id), Directory: owned[0].Directory, SourceSealSHA256: strings.Repeat("a", 64), Source: sourceRaw, Manifest: manifest.Bytes(), SourceEvidence: json.RawMessage("null"), ConfirmedBound: json.RawMessage("null"), FinalPairs: json.RawMessage("null"), ArtifactSHA256: nativePointHash(nil), DescriptorSHA256: nativePointHash(nil), Scope: restic.Scope{BindingID: string(permit.Runtime.BindingID), EpochID: string(permit.Runtime.EpochID)}, IssuedAt: time.Now().UTC()}
	raw, _ := json.Marshal(descriptor)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertNativeUploadDescriptor(ctx, tx, op.ID, prep.PointID(), raw, nil, nil); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	retained, err := i.RetainedUploadWorkspace(ctx)
	if err != nil || retained != path || filepath.Base(retained) != owned[0].ID {
		t.Fatal("issued upload did not select its tracked workspace", err)
	}
	if err := i.DisposeWorkspace(ctx, id); err == nil {
		t.Fatal("live upload workspace declared disposed")
	}
	if err := i.BeginUploadRetirement(ctx, parent); err != nil {
		t.Fatal("upload retirement", err)
	}
	if err := i.BeginWorkspaceRetirement(ctx, id, parent); err != nil {
		t.Fatal("workspace retirement", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := i.DisposeWorkspace(ctx, id); err != nil {
		t.Fatal("removed upload workspace did not settle tracker", err)
	}
	if issued, err := i.UploadRetirementIssued(ctx); err != nil || !issued {
		t.Fatal("upload retirement journal was lost", err)
	}
	if issued, err := i.WorkspaceRetirementIssued(ctx, id); err != nil || !issued {
		t.Fatal("workspace retirement journal was lost", err)
	}
	owned, err = i.Workspaces(ctx)
	if err != nil || len(owned) != 0 {
		t.Fatal("workspace remains pending after both retirements", err)
	}
	original, err := s.GetOperation(ctx, op.ID)
	if err != nil || original.State != ops.RecoveryRequired {
		t.Fatal("retirement changed original failed operation", err)
	}
}

func retainedUploadWorkspaceBundleFixture(t *testing.T, permit EnginePermit, point string, parent *os.Root, path string) (nativecapture.Source, restic.Manifest) {
	t.Helper()
	config := []byte("[module-http]\nenabled = false\n")
	r := permit.Runtime
	source, err := nativecapture.UnpublishedSource(nativecapture.EmptyRecord{Format: nativecapture.Format, Scope: nativecapture.EmptyScope{BindingID: string(r.BindingID), EpochID: string(r.EpochID)}, GenerationID: strings.Repeat("f", 32), ConfigurationSHA256: nativePointHash(config), PublisherReference: r.Secrets.Publisher.Reference, SigningRecovery: nativecapture.SigningRecovery{Kind: "external_immutable", Reference: r.Secrets.SigningPrivate.Reference, Version: r.Secrets.SigningPrivate.Version, SHA256: r.Secrets.SigningPrivate.SHA256}, Members: []nativecapture.CheckpointMember{{Path: "config.toml", Bytes: uint64(len(config)), SHA256: nativePointHash(config)}}}, nativecapture.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Mkdir("bundle", 0700); err != nil {
		t.Fatal(err)
	}
	if err := parent.WriteFile("bundle/config.toml", config, 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(source)
	if err := parent.WriteFile("bundle/native-source.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	bundle, err := restic.Seal(context.Background(), filepath.Join(path, "bundle"), path, restic.Identity{BindingID: string(r.BindingID), EpochID: string(r.EpochID), CaptureID: point, ModuleSHA256: source.SHA256(), CheckpointSHA256: source.SHA256()}, restic.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	manifest := bundle.Manifest()
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	return source, manifest
}
