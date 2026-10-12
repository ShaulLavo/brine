//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

// prepareNativeRestoreTarget keeps the authenticated download intact and builds
// the native-only staged tree under the original transition parent. Any partial
// copy remains owned by the pending intent; this function never overwrites it.
func prepareNativeRestoreTarget(ctx context.Context, stage *nativeRestoreStage, verified nativeVerifiedRestore, extracted restic.AuthenticatedRestore, home string) error {
	if stage == nil || verified.source == nil || verified.source.root != extracted.Root() || stage.unchanged(ctx) != nil || verified.source.unchanged(ctx) != nil {
		return errEngineSource
	}
	facts, err := stage.state.ReadNativeRestoreTargetFacts(ctx, stage.claim)
	if err != nil {
		return err
	}
	if err = prepareNativeRestoreMaterial(ctx, stage, verified.source, facts.Runtime); err != nil {
		return err
	}
	unit, err := readNativeRestoreUnit(ctx, home, facts.Runtime)
	if err != nil {
		return err
	}
	if err = stage.root.Mkdir(stage.stagedName, 0700); err != nil {
		return errEngineSource
	}
	staged, err := stage.root.OpenRoot(stage.stagedName)
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = staged.Close() }()
	if err = copyNativeRestoreTarget(ctx, verified.source, staged); err != nil {
		return err
	}
	if err = syncNativeRestoreTarget(ctx, staged, verified.source.limits); err != nil {
		return err
	}
	if err = syncEngineDirectory(stage.root, "."); err != nil {
		return err
	}
	observed, err := observeNativeRestoreTarget(ctx, stage, staged, facts.Runtime, unit, verified.source.limits)
	if err != nil || stage.unchanged(ctx) != nil || verified.source.unchanged(ctx) != nil {
		return errEngineSource
	}
	observed.Download = extracted.Root()
	return stage.state.RecordNativeRestoreTarget(ctx, stage.claim, extracted, observed)
}

func copyNativeRestoreTarget(ctx context.Context, source *nativeRestoredSource, target *os.Root) error {
	if source == nil || source.unchanged(ctx) != nil {
		return errEngineSource
	}
	members := source.checkpoint.Record().Members
	for _, member := range members {
		if member.Path == "recovery/signing.key" {
			continue
		}
		if err := copyNativeRestoreMember(ctx, source, target, member.Path, member.Bytes, member.SHA256); err != nil {
			return err
		}
	}
	_, selection, initialized := source.source.Initialized()
	if !initialized {
		return errEngineSource
	}
	for _, member := range selection.Members() {
		if err := copyNativeRestoreMember(ctx, source, target, member.Path, member.Bytes, member.SHA256); err != nil {
			return err
		}
	}
	return source.unchanged(ctx)
}

func copyNativeRestoreMember(ctx context.Context, source *nativeRestoredSource, target *os.Root, name string, size uint64, hash string) error {
	if size > uint64(source.limits.Bytes) || engineMakePrivateDirectories(target, filepath.Dir(name)) != nil {
		return errEngineSource
	}
	file, err := engineOpenMember(source.root, name, unix.O_RDONLY, 0)
	if err != nil {
		return errEngineSource
	}
	member, err := engineSourceMember(file, name)
	closeErr := file.Close()
	if err != nil || closeErr != nil || member.Size < 0 || uint64(member.Size) != size {
		return errEngineSource
	}
	member.SHA256 = hash
	return copyEngineFile(ctx, source.root, target, member)
}

func prepareNativeRestoreMaterial(ctx context.Context, stage *nativeRestoreStage, source *nativeRestoredSource, runtime data.EngineRuntime) error {
	recovery := source.checkpoint.Record().SigningRecovery
	if recovery.Reference != runtime.Secrets.SigningPrivate.Reference || recovery.Version != runtime.Secrets.SigningPrivate.Version || recovery.SHA256 != runtime.Secrets.SigningPrivate.SHA256 {
		return errEngineSource
	}
	root, err := engineOpenRoot(source.preparation.StateRoot())
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = root.Close() }()
	versions := []data.EngineSecretVersion{runtime.Secrets.Publisher, runtime.Secrets.SigningPublic, runtime.Secrets.AuthPolicy}
	roles := []string{"publisher", "signing-public", "auth-policy"}
	values := make([][]byte, len(versions))
	defer func() {
		for _, value := range values {
			clear(value)
		}
	}()
	for i, version := range versions {
		name := filepath.Join("engine-secrets", string(runtime.BindingID), roles[i], "v"+strconv.FormatUint(version.Version, 10)+".secret")
		values[i], err = readEnginePrivateFile(ctx, root, name, version.SHA256, 32<<10)
		if err != nil {
			return err
		}
	}
	private, err := readNativeRestoreSigningPrivate(ctx, root, source.root, runtime, recovery.Kind, recovery.Member)
	if err != nil {
		return err
	}
	defer clear(private)
	material, err := NewEngineSecretMaterial(values[0], private, values[1], values[2])
	if err != nil {
		return err
	}
	defer material.Clear()
	if err = WriteEngineMaterial(ctx, source.preparation, runtime, material); err != nil {
		return err
	}
	permit := source.permit
	permit.Runtime = runtime.Clone()
	readback, err := readEngineMaterial(ctx, source.preparation.StateRoot(), permit)
	if err == nil {
		readback.clear()
	}
	return err
}

func readNativeRestoreUnit(ctx context.Context, home string, runtime data.EngineRuntime) (string, error) {
	units, err := openManagedUnits(home)
	if err != nil {
		return "", err
	}
	defer func() { _ = units.Close() }()
	raw, present, err := units.read(ctx, filepath.Join(managedServiceDirectory, "brine-engine-"+string(runtime.BindingID)+".service"))
	expected, renderErr := EngineServiceBytes(runtime)
	if err != nil || renderErr != nil || !present || managedServiceHash(raw) != runtime.UnitSHA256 || managedServiceHash(expected) != runtime.UnitSHA256 {
		return "", errEngineSource
	}
	return runtime.UnitSHA256, nil
}

func observeNativeRestoreTarget(ctx context.Context, stage *nativeRestoreStage, staged *os.Root, runtime data.EngineRuntime, unit string, limits engineCopyLimits) (store.NativeRestoreTargetObservation, error) {
	permit, err := stage.state.ReadNativeStoppedPermit(ctx, stage.source.stopped)
	if err != nil || permit.Allocation == nil {
		return store.NativeRestoreTargetObservation{}, errEngineSource
	}
	tree := engineTransitionTree{limits: limits}
	old, err := tree.inspectLocation(ctx, stage.base, stage.claim.Binding().RelativeDirectory)
	oldSeal, present := old.Tree()
	if err != nil || !present {
		return store.NativeRestoreTargetObservation{}, errEngineSource
	}
	sealed, err := sealOwnedEngineSource(ctx, staged, limits)
	if err != nil {
		return store.NativeRestoreTargetObservation{}, err
	}
	target := data.EngineTreeSeal{Root: engineTransitionDirectory(stage.rootIdentity), Parent: engineTransitionDirectory(stage.parentIdentity), Directory: engineTransitionDirectory(sealed.root), ContentsSHA256: engineTransitionSealDigest(sealed)}
	allocation := *permit.Allocation
	allocation.DirectoryDevice, allocation.DirectoryInode = target.Directory.Device, target.Directory.Inode
	allocation.AllocatedAt = time.Now().UTC()
	raw, _ := json.Marshal(runtime.Secrets)
	sum := sha256.Sum256(raw)
	return store.NativeRestoreTargetObservation{Parent: stage.root, Download: nil, OldTree: oldSeal, TargetTree: target, Allocation: allocation, UnitSHA256: unit, MaterialSHA256: hex.EncodeToString(sum[:]), ObservedAt: time.Now().UTC()}, nil
}

func syncNativeRestoreTarget(ctx context.Context, root *os.Root, limits engineCopyLimits) error {
	seal, err := sealOwnedEngineSource(ctx, root, limits)
	if err != nil {
		return err
	}
	for i := len(seal.members) - 1; i >= 0; i-- {
		member := seal.members[i]
		if member.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		if err = syncEngineDirectory(root, member.Name); err != nil {
			return err
		}
	}
	return syncEngineDirectory(root, ".")
}

func readNativeRestoreSigningPrivate(ctx context.Context, state, download *os.Root, runtime data.EngineRuntime, kind, member string) ([]byte, error) {
	switch kind {
	case "encrypted_member":
		if member != "recovery/signing.key" {
			return nil, errEngineSource
		}
		return readEnginePrivateFile(ctx, download, member, runtime.Secrets.SigningPrivate.SHA256, 32<<10)
	case "external_immutable":
		if member != "" {
			return nil, errEngineSource
		}
		name := filepath.Join("engine-secrets", string(runtime.BindingID), "signing-private", "v"+strconv.FormatUint(runtime.Secrets.SigningPrivate.Version, 10)+".secret")
		return readEnginePrivateFile(ctx, state, name, runtime.Secrets.SigningPrivate.SHA256, 32<<10)
	default:
		return nil, errEngineSource
	}
}
