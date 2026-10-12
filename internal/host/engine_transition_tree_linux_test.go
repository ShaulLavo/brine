//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

func engineTreeTransitionFixture(t *testing.T) *engineTransitionTree {
	t.Helper()
	binding, _ := engineSourceFixture(t)
	id := strings.Repeat("f", 32)
	stage := filepath.Join(string(binding.Root), "transitions", id, "staged")
	if err := os.MkdirAll(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "state"), []byte("verified-target"), 0600); err != nil {
		t.Fatal(err)
	}
	oldEpoch := data.ReplicaEpochID(strings.Repeat("d", 32))
	newEpoch := data.ReplicaEpochID(strings.Repeat("e", 32))
	return finishEngineTreeTransitionFixture(t, binding, id, oldEpoch, newEpoch)
}

func TestEngineTransitionRejectsLifetimeFromAnotherStateRoot(t *testing.T) {
	tree := engineTreeTransitionFixture(t)
	other := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := data.PrepareEngineLocks(ctx, other, tree.binding.BindingID); err != nil {
		t.Fatal(err)
	}
	foreign, err := data.AcquireEngineLifetimeLock(ctx, other, tree.binding.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Release() }()
	tree.lifetime = foreign
	if tree.active() {
		t.Fatal("foreign lifetime namespace admitted transition while original lock remained held")
	}
	if _, err = tree.inspect(ctx); err == nil {
		t.Fatal("foreign lifetime namespace admitted source inspection")
	}
}

func finishEngineTreeTransitionFixture(t *testing.T, binding data.SpacetimeDBBinding, id string, oldEpoch, newEpoch data.ReplicaEpochID) *engineTransitionTree {
	t.Helper()
	// A real store preparation is required; create its separate private state here.
	ctx := context.Background()
	stateRoot := t.TempDir()
	state, err := store.Open(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	dest := data.Destination{Reference: "backup", Endpoint: "https://storage.example", Region: "test", Bucket: "fixture", BasePrefix: "native", CredentialRef: "backup"}
	prefix, _ := data.RemotePrefix(dest.BasePrefix, binding.IncarnationID, binding.DatabaseID, oldEpoch)
	_, err = state.ReserveEngine(ctx, store.EngineReservation{App: "fixture", PolicyHash: "sha256:" + strings.Repeat("a", 64), Database: binding, Archive: data.ArchiveBinding{BindingID: binding.BindingID, DatabaseID: binding.DatabaseID, Destination: dest, EpochID: oldEpoch, RemotePrefix: prefix}})
	if err != nil {
		t.Fatal(err)
	}
	if err = data.PrepareEngineLocks(ctx, stateRoot, binding.BindingID); err != nil {
		t.Fatal(err)
	}
	fence, err := state.HoldDataFence(ctx, binding.DatabaseID, "fixture-preparation")
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := state.AcquireEnginePreparation(ctx, binding.BindingID, fence.ID, "fixture-preparation")
	if err != nil {
		t.Fatal(err)
	}
	lifetime, err := data.AcquireEngineLifetimeLock(ctx, stateRoot, binding.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lifetime.Release(); _ = preparation.Close() })
	scope := data.EngineTransitionScope{ID: id, ApplyOperationID: "fixture-apply", PlanID: "sha256:" + strings.Repeat("1", 64), Requester: "deploy:" + strings.Repeat("2", 64), PolicyHash: "sha256:" + strings.Repeat("3", 64), TargetHash: "sha256:" + strings.Repeat("4", 64), DesiredHash: "sha256:" + strings.Repeat("5", 64), IncarnationID: binding.IncarnationID, DatabaseID: binding.DatabaseID, BindingID: binding.BindingID, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	base, _ := data.CommittedEngineBase(data.EngineHeadRef{BindingID: binding.BindingID, EpochID: oldEpoch, Version: 1, RuntimeSHA256: strings.Repeat("6", 64)})
	ref := func(kind data.EngineEvidenceKind, epoch data.ReplicaEpochID, ordinal int) data.EngineEvidenceRef {
		return data.EngineEvidenceRef{Kind: kind, ID: fmt.Sprintf("%032x", ordinal), SHA256: fmt.Sprintf("%064x", ordinal), DatabaseID: binding.DatabaseID, BindingID: binding.BindingID, EpochID: epoch}
	}
	tree := &engineTransitionTree{preparation: preparation, lifetime: lifetime, binding: binding, limits: engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16}}
	root, err := engineOpenRoot(string(binding.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	old, err := tree.inspectLocation(ctx, root, binding.RelativeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	target, err := tree.inspectLocation(ctx, root, filepath.Join("transitions", id, "staged"))
	if err != nil {
		t.Fatal(err)
	}
	oldSeal, _ := old.Tree()
	newSeal, _ := target.Tree()
	common := data.EngineTransitionCommon{Engine: data.EngineSpacetimeDB, Scope: scope, Base: base, NewEpochID: newEpoch, FenceID: fence.ID, FenceOwnerOperationID: "fixture-preparation", PreparedPoint: ref(data.EnginePreparedPointEvidence, oldEpoch, 1), SourceWitness: ref(data.EngineSourceWitnessEvidence, oldEpoch, 2), OldTree: oldSeal, TargetTree: newSeal, PreservedParent: newSeal.Parent, TargetRuntimeSHA256: strings.Repeat("7", 64), TargetVerification: ref(data.EngineTargetEvidence, newEpoch, 3), TargetCheckpoint: ref(data.EngineCheckpointEvidence, newEpoch, 4), TargetMaterial: ref(data.EngineMaterialEvidence, newEpoch, 5), RecoveryRelease: ref(data.EngineRecoveryEvidence, newEpoch, 6), RetainedDependencies: ref(data.EngineDependenciesEvidence, newEpoch, 7)}
	tree.input, err = data.NewEnginePublicationTransition(common, data.EnginePublicationTarget{CandidateSHA256: strings.Repeat("8", 64), Receipt: ref(data.EnginePublicationEvidence, newEpoch, 8)})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func preserveIntentFixture(t *testing.T, tree *engineTransitionTree) data.EngineTransitionCursor {
	t.Helper()
	initial, err := data.PrepareEngineTransition(tree.input)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := tree.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	event, err := data.AdvanceEngineTransition(tree.input, initial.Cursor(), data.EngineTransitionPreserveIntent, observed)
	if err != nil {
		t.Fatal(err)
	}
	return event.Cursor()
}

func TestEngineTransitionRenamePreservesOriginalAndPromotesTarget(t *testing.T) {
	tree := engineTreeTransitionFixture(t)
	ctx := context.Background()
	intent := preserveIntentFixture(t, tree)
	beforeSeal := transitionActualSeal(t, tree, tree.binding.RelativeDirectory)
	if err := tree.move(ctx, intent, enginePreserveOriginal); err != nil {
		transitionFailureDetails(t, tree)
		_, _, original := tree.relativeNames()
		afterSeal := transitionActualSeal(t, tree, original)
		t.Logf("synthetic before-root=%+v after-root=%+v before-members=%+v after-members=%+v", beforeSeal.root, afterSeal.root, beforeSeal.members, afterSeal.members)
		t.Fatal(err)
	}
	observed, err := tree.inspect(ctx)
	if err != nil || data.ClassifyEngineTransitionLayout(tree.input, observed) != data.EngineLayoutPreserved {
		t.Fatal(err)
	}
	preserved, err := data.AdvanceEngineTransition(tree.input, intent, data.EngineTransitionOriginalPreserved, observed)
	if err != nil {
		t.Fatal(err)
	}
	promote, err := data.AdvanceEngineTransition(tree.input, preserved.Cursor(), data.EngineTransitionPromoteIntent, observed)
	if err != nil || tree.move(ctx, promote.Cursor(), enginePromoteTarget) != nil {
		t.Fatal(err)
	}
	live, _, original := tree.relativeNames()
	root, err := os.OpenRoot(string(tree.binding.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if content, err := root.ReadFile(filepath.Join(live, "state")); err != nil || string(content) != "verified-target" {
		t.Fatal("target was not promoted", err)
	}
	if content, err := root.ReadFile(filepath.Join(original, "control-db/state")); err != nil || string(content) != "synthetic-persisted-state" {
		t.Fatal("original data changed", err)
	}
}

func transitionActualSeal(t *testing.T, tree *engineTransitionTree, relative string) engineTreeSeal {
	t.Helper()
	root, err := engineOpenRoot(string(tree.binding.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	leaf, err := root.OpenRoot(relative)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = leaf.Close() }()
	seal, err := sealOwnedEngineSource(context.Background(), leaf, tree.limits)
	if err != nil {
		t.Fatal(err)
	}
	return seal
}

func TestEngineTransitionUnknownRenameSettlesWithoutRepeatingEffect(t *testing.T) {
	tree := engineTreeTransitionFixture(t)
	intent := preserveIntentFixture(t, tree)
	renamed := 0
	rename := func(from int, old string, to int, next string, flags uint) error {
		renamed++
		if err := unix.Renameat2(from, old, to, next, flags); err != nil {
			return err
		}
		return errors.New("injected unknown post-rename failure")
	}
	if err := tree.moveWith(context.Background(), intent, enginePreserveOriginal, rename, func(file *os.File) error { return file.Sync() }); err == nil {
		t.Fatal("unknown effect reported success")
	}
	if err := tree.move(context.Background(), intent, enginePreserveOriginal); err == nil {
		t.Fatal("unknown movement replayed")
	}
	if err := tree.settle(context.Background(), intent, enginePreserveOriginal); err != nil || renamed != 1 {
		transitionFailureDetails(t, tree)
		t.Fatal("known original not adopted by sync only", err, renamed)
	}
}

func transitionFailureDetails(t *testing.T, tree *engineTransitionTree) {
	t.Helper()
	observed, err := tree.inspect(context.Background())
	raw, _ := json.Marshal(observed)
	t.Logf("synthetic layout inspect error=%v observed=%s expected=%+v", err, raw, tree.input.Common())
}

func TestEngineTransitionRefusesOccupiedDestinationAndReleasedGuard(t *testing.T) {
	tree := engineTreeTransitionFixture(t)
	intent := preserveIntentFixture(t, tree)
	_, _, original := tree.relativeNames()
	root, err := os.OpenRoot(string(tree.binding.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err = root.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	if err = tree.move(context.Background(), intent, enginePreserveOriginal); err == nil {
		t.Fatal("empty occupied original destination was replaced")
	}
	if err = root.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err = tree.preparation.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tree.move(context.Background(), intent, enginePreserveOriginal); err == nil {
		t.Fatal("closed launch guard permitted tree movement")
	}
	content, err := root.ReadFile(filepath.Join(tree.binding.RelativeDirectory, "control-db/state"))
	if err != nil || string(content) != "synthetic-persisted-state" {
		t.Fatal("refusal changed original data", err)
	}
}

func TestEngineTransitionPostRenameSyncFailureKeepsOriginalRecoverable(t *testing.T) {
	tree := engineTreeTransitionFixture(t)
	intent := preserveIntentFixture(t, tree)
	syncs := 0
	injected := func(file *os.File) error {
		syncs++
		if syncs == 1 {
			return errors.New("injected parent fsync failure")
		}
		return file.Sync()
	}
	if err := tree.moveWith(context.Background(), intent, enginePreserveOriginal, unix.Renameat2, injected); err == nil {
		t.Fatal("parent fsync failure reported durable success")
	}
	observed, err := tree.inspect(context.Background())
	if err != nil || data.ClassifyEngineTransitionLayout(tree.input, observed) != data.EngineLayoutPreserved {
		t.Fatal("unknown sync lost original or staging", err)
	}
	if err = tree.settle(context.Background(), intent, enginePreserveOriginal); err != nil {
		t.Fatal("observable moved original could not settle", err)
	}
}

func TestEngineTransitionChangedParentAndSymlinkCannotRedirectMovement(t *testing.T) {
	tree := engineTreeTransitionFixture(t)
	intent := preserveIntentFixture(t, tree)
	_, staged, _ := tree.relativeNames()
	root, err := os.OpenRoot(string(tree.binding.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	parent := filepath.Dir(staged)
	if err = root.Rename(parent, parent+"-held"); err != nil {
		t.Fatal(err)
	}
	if err = root.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err = tree.move(context.Background(), intent, enginePreserveOriginal); err == nil {
		t.Fatal("rebound stage parent permitted movement")
	}
	if err = root.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err = root.Symlink(filepath.Base(parent)+"-held", parent); err != nil {
		t.Fatal(err)
	}
	if err = tree.move(context.Background(), intent, enginePreserveOriginal); err == nil {
		t.Fatal("symlink stage parent redirected movement")
	}
	if _, err = root.Stat(tree.binding.RelativeDirectory); err != nil {
		t.Fatal("refusal removed original source", err)
	}
}

func TestEngineTransitionDigestIgnoresDirectoryStorageSizeOnly(t *testing.T) {
	tree := engineTreeTransitionFixture(t)
	before := transitionActualSeal(t, tree, tree.binding.RelativeDirectory)
	expected := engineTransitionSealDigest(before)
	changed := before
	changed.root.Size += 8
	changed.members = append([]engineTreeMember(nil), before.members...)
	changed.members[0].Size += 8
	if engineTransitionSealDigest(changed) != expected {
		t.Fatal("directory storage layout changed content evidence")
	}
	changed.members[1].Size++
	if engineTransitionSealDigest(changed) == expected {
		t.Fatal("file size disappeared from content evidence")
	}
	changed = before
	changed.root.Inode++
	if engineTransitionSealDigest(changed) == expected {
		t.Fatal("directory identity disappeared from content evidence")
	}
}
