//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

type nativeOnlineCopy struct {
	original     *os.Root
	parent       *os.Root
	root         *os.Root
	path         string
	before       engineOnlineCensus
	members      []engineTreeMember
	copied       engineTreeSeal
	sourcePermit store.EnginePermit
	identity     engineTreeMember
	created      bool
}

func (c *nativeOnlineCopy) close() error {
	if c == nil {
		return nil
	}
	var err error
	if c.created {
		current, identityErr := nativeRestoreDirectoryIdentity(c.parent, "source")
		if identityErr != nil || !sameNativeRestoreDirectory(current, c.identity) {
			return errEngineSource
		}
	}
	if c.root != nil {
		err = c.root.Close()
		c.root = nil
	}
	if c.original != nil {
		err = errors.Join(err, c.original.Close())
		c.original = nil
	}
	if c.created {
		err = errors.Join(err, c.parent.RemoveAll("source"))
		c.created = false
	}
	return err
}
func captureNativeOnlineCopy(ctx context.Context, permit store.EnginePermit, parent *os.Root, path string) (*nativeOnlineCopy, error) {
	original, err := openEngineSource(permit.Reserved.Database)
	if err != nil {
		return nil, err
	}
	copy := &nativeOnlineCopy{original: original, parent: parent, path: filepath.Join(path, "source"), sourcePermit: permit}
	complete := false
	defer func() {
		if !complete {
			_ = copy.close()
		}
	}()
	copy.before, err = scanEngineOnlineMetadata(ctx, original, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil || permit.Allocation == nil || copy.before.root.Device != permit.Allocation.DirectoryDevice || copy.before.root.Inode != permit.Allocation.DirectoryInode {
		return nil, errEngineSource
	}
	if err := parent.Mkdir("source", 0700); err != nil {
		return nil, err
	}
	copy.root, err = parent.OpenRoot("source")
	if err != nil {
		return nil, err
	}
	copy.identity, err = nativeRestoreDirectoryIdentity(copy.root, ".")
	if err != nil {
		return nil, err
	}
	copy.created = true
	for _, member := range copy.before.members {
		if member.Mode&unix.S_IFMT != unix.S_IFREG || !nativeCheckpointMember(member.Name) && !nativeCaptureHistoryMember(member.Name) {
			continue
		}
		if nativeOnlineOffsetCacheMember(member, copy.before) {
			continue
		}
		if err := copy.copyMember(ctx, member); err != nil {
			return nil, err
		}
	}
	if err := copy.unchanged(ctx); err != nil {
		return nil, err
	}
	copy.copied, err = sealEngineRoot(ctx, copy.root, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil {
		return nil, err
	}
	complete = true
	return copy, nil
}
func nativeCaptureHistoryMember(name string) bool {
	return strings.HasPrefix(name, "replicas/") && (strings.Contains(name, "/clog/") || strings.Contains(name, "/snapshots/"))
}
func (c *nativeOnlineCopy) copyMember(ctx context.Context, member engineTreeMember) error {
	if member.Size <= 0 || member.Size > 32<<20 {
		return errEngineSource
	}
	if err := nativeBundleDirectories(c.root, filepath.Dir(member.Name)); err != nil {
		return err
	}
	file, err := engineOpenMember(c.original, member.Name, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	copied, err := copyEngineCapturedPrefix(ctx, file, member, member.Size, c.root, member.Name)
	if err != nil {
		return err
	}
	recorded := member
	recorded.SHA256 = copied.SHA256
	c.members = append(c.members, recorded)
	return nil
}
func (c *nativeOnlineCopy) unchanged(ctx context.Context) error {
	if c == nil || c.original == nil || c.root == nil {
		return errEngineSource
	}
	fresh, err := openEngineSource(c.sourcePermit.Reserved.Database)
	if err != nil {
		return err
	}
	defer func() { _ = fresh.Close() }()
	freshIdentity, err := nativeRestoreDirectoryIdentity(fresh, ".")
	if err != nil || !sameNativeRestoreDirectory(freshIdentity, c.before.root) {
		return errEngineSource
	}
	after, err := scanEngineOnlineMetadata(ctx, c.original, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil || !nativeOnlineCensusMatches(c.before, after) {
		return errEngineSource
	}
	for _, member := range c.members {
		file, err := engineOpenMember(c.original, member.Name, unix.O_RDONLY, 0)
		if err != nil {
			return err
		}
		err = checkEngineCapturedPrefix(ctx, c.original, file, member, member.Size, member.SHA256)
		_ = file.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func nativeOnlineCensusMatches(before, after engineOnlineCensus) bool {
	if !sameNativeRestoreDirectory(before.root, after.root) || len(before.members) != len(after.members) {
		return false
	}
	for i, original := range before.members {
		if !nativeOnlineMemberMatches(original, after.members[i], before) {
			return false
		}
	}
	return true
}

func nativeOnlineMemberMatches(original, actual engineTreeMember, census engineOnlineCensus) bool {
	if nativeOnlineOffsetCacheMember(original, census) {
		return actual == original
	}
	if nativeCaptureHistoryMember(original.Name) && original.Mode&unix.S_IFMT == unix.S_IFREG {
		return enginePrefixMetadata(actual, original, original.Size)
	}
	return actual == original
}

func nativeOnlineCaptureTask(service Service, runtime nativeBackupRuntime) jobs.TaskHandler {
	return func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		return runNativeOnlineCapture(ctx, service, runtime, op)
	}
}
func runNativeOnlineCapture(ctx context.Context, service Service, runtime nativeBackupRuntime, op ops.Operation) (receipt json.RawMessage, taskErr error) {
	claim, err := service.Store.BeginNativeOnlineCapture(ctx, op.ID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = claim.Close() }()
	permit, err := claim.Permit(ctx)
	if err != nil {
		return nil, err
	}
	material, err := claim.Material(ctx)
	pol, policyErr := service.Policy.Load(ctx)
	if err != nil || policyErr != nil || pol.Hash() != material.CredentialRecord.PolicyHash {
		return nil, result.New(result.PolicyRefused, nil)
	}
	containers, err := listEngineContainers(ctx, runtime.session)
	if err != nil {
		return nil, err
	}
	live, err := nativeCaptureLiveHandle(ctx, runtime.session, containers, permit, runtime.stateRoot)
	if err != nil {
		return nil, err
	}
	defer live.close()
	keys, err := readEngineMaterial(ctx, runtime.stateRoot, permit)
	if err != nil {
		return nil, err
	}
	defer keys.clear()
	before, err := observeOwnedNative(ctx, live, permit, keys)
	if err != nil {
		return nil, err
	}
	liveSource, err := openEngineSource(permit.Reserved.Database)
	if err != nil {
		return nil, err
	}
	artifact, err := readNativeProgramArtifact(ctx, liveSource, permit)
	_ = liveSource.Close()
	if err != nil {
		return nil, err
	}
	preparation, err := runtime.state.ReadNativeOnlinePreparation(ctx, claim)
	if err != nil {
		return nil, err
	}
	module, err := runtime.extractCurrentModule(ctx, preparation, permit, artifact)
	if err != nil {
		return nil, err
	}
	defer func() { taskErr = errors.Join(taskErr, module.close()) }()
	confirmed, err := observeNativeConfirmed(ctx, permit, keys, module)
	if err != nil || live.alive() != nil {
		return nil, errEngineSource
	}
	parent, path, err := newNativePreparedRestoreParent(ctx, permit)
	if err != nil {
		return nil, err
	}
	retainWorkspace := false
	defer func() {
		if retainWorkspace {
			_ = parent.Close()
			return
		}
		taskErr = errors.Join(taskErr, discardNativeWorkspace(parent, path))
	}()
	copied, err := captureNativeOnlineCopy(ctx, permit, parent, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = copied.close() }()
	checkpoint, err := buildNativeOnlineCheckpoint(ctx, runtime, permit, copied, claim.PointID())
	if err != nil {
		return nil, err
	}
	inventory, err := nativeOwnedInventory(ctx, copied.root, copied.copied, string(permit.Runtime.BindingID), string(permit.Runtime.EpochID), claim.PointID())
	if err != nil {
		return nil, err
	}
	selection, err := nativecapture.Select(inventory, nativecapture.DefaultLimits())
	if err != nil {
		return nil, err
	}
	source, err := nativecapture.InitializedSource(checkpoint, selection, nativecapture.DefaultLimits())
	if err != nil {
		return nil, err
	}
	witness, disposal, err := readNativeOnlineCopy(ctx, runtime, copied, path, checkpoint, selection)
	if err != nil || copied.unchanged(ctx) != nil || live.alive() != nil {
		return nil, errEngineSource
	}
	final, err := spacetimedb.RecoveryPairsFromPassive(witness, module.candidate.candidate.Schema())
	if err != nil || spacetimedb.CheckConfirmedLowerBound(confirmed, final) != nil {
		return nil, errEngineSource
	}
	after, err := observeOwnedNative(ctx, live, permit, keys)
	if err != nil || !nativeObservationMatches(before.permit.Runtime, after.metadata) {
		return nil, errEngineSource
	}
	ctx, cancel, err := nativeTransportContext(ctx, material.Scope)
	if err != nil {
		return nil, err
	}
	defer cancel()
	client, repository, err := runtime.repositoryMaterial(ctx, material, claim, "")
	if err != nil {
		return nil, err
	}
	bundle, err := materializeNativeBundle(ctx, copied.root, source, parent, path, claim.PointID(), runtime.stateRoot, permit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = bundle.Close() }()
	if err := claim.UploadIntent(ctx, source, bundle.Manifest(), store.NativeUploadRetention{Parent: parent, Workspace: path, SourceSealSHA256: engineSourceSealDigest(copied.copied), SourceEvidence: witness, ConfirmedBound: confirmed, FinalPairs: final, Artifact: artifact.Bytes(), Descriptor: module.descriptor}); err != nil {
		return nil, err
	}
	retainWorkspace = true
	transport, err := client.Backup(ctx, repository, path, bundle)
	if err != nil {
		return nil, err
	}
	extracted, err := client.RestoreExact(ctx, repository, path, parent, "download", transport, bundle.Manifest())
	if err != nil {
		return nil, err
	}
	defer func() { _ = extracted.Root().Close() }()
	restored, remoteDisposal, err := runtime.verifyExtracted(ctx, extracted, parent, path, checkpoint, selection, witness, module.candidate.candidate.Schema(), confirmed, final)
	if err != nil {
		return nil, result.New(result.NativeCaptureVerifyRefused, err)
	}
	if err = checkNativeCapturePostVerification(ctx, copied, live, module.candidate); err != nil {
		return nil, err
	}
	_ = disposal
	point, err := claim.Complete(ctx, extracted, store.NativePreparedObservation{Parent: parent, Source: source, Manifest: bundle.Manifest(), Transport: transport, SourceEvidence: witness, RestoredEvidence: restored, ConfirmedBound: confirmed, FinalPairs: final, Artifact: artifact.Bytes(), Descriptor: module.descriptor, ReaderNonce: remoteDisposal.nonce, ReaderViewSHA256: remoteDisposal.viewHash, ReaderImageID: remoteDisposal.imageID, ReaderDisposedAt: remoteDisposal.at})
	if err != nil {
		return nil, result.New(result.NativeCaptureCommitRefused, err)
	}
	retainWorkspace = false
	backup, err := service.Store.ReadNativeBackupPolicy(ctx, point.BindingID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(nativecapture.TaskReceipt{Kind: string(ops.NativeCapture), OperationID: op.ID, BindingID: string(point.BindingID), EpochID: string(point.EpochID), PointID: point.PointID, PointSHA256: point.PointSHA256, Outcome: "verified", Engine: "spacetimedb", ModuleSHA256: string(permit.Runtime.Module.Wasm), SchemaSHA256: permit.Runtime.Module.Schema.SHA256, CaptureIntervalSeconds: backup.Backup.CaptureIntervalSeconds, Loss: "unknown"})
}
func nativeCaptureLiveHandle(ctx context.Context, session localexec.Session, containers []engineContainer, permit store.EnginePermit, stateRoot string) (*engineNativeHandle, error) {
	var selected *engineContainer
	for i := range containers {
		if containers[i].Name != engineContainerName(permit.Runtime.BindingID) {
			continue
		}
		if selected != nil {
			return nil, errEngineSource
		}
		selected = &containers[i]
	}
	if selected == nil {
		return nil, errEngineSource
	}
	return bindEngineNative(ctx, session, *selected, permit, engineMaterialPaths(stateRoot, permit.Runtime))
}
func buildNativeOnlineCheckpoint(ctx context.Context, runtime nativeBackupRuntime, permit store.EnginePermit, copied *nativeOnlineCopy, pointID string) (nativecapture.Checkpoint, error) {
	replica, err := nativeSoleReplica(copied.copied)
	if err != nil {
		return nativecapture.Checkpoint{}, err
	}
	generation, err := data.NewID()
	if err != nil {
		return nativecapture.Checkpoint{}, err
	}
	var members []nativecapture.CheckpointMember
	config := ""
	for _, member := range copied.members {
		if nativeCheckpointMember(member.Name) {
			if member.Size <= 0 || member.Size > 32<<20 {
				return nativecapture.Checkpoint{}, errEngineSource
			}
			members = append(members, nativecapture.CheckpointMember{Path: member.Name, Bytes: uint64(member.Size), SHA256: member.SHA256})
			if member.Name == "config.toml" {
				config = member.SHA256
			}
		}
	}
	recovery, key, err := backupSigningRecovery(ctx, runtime.stateRoot, permit)
	if err != nil {
		return nativecapture.Checkpoint{}, err
	}
	members = append(members, key)
	r := permit.Runtime
	return nativecapture.NewCheckpoint(nativecapture.CheckpointRecord{Format: nativecapture.Format, Scope: nativecapture.CheckpointScope{BindingID: string(r.BindingID), EpochID: string(r.EpochID), ReplicaID: replica}, GenerationID: generation, DatabaseIdentity: string(r.Database), PublisherIdentity: string(r.Publisher), Names: r.Names, RuntimeModuleHash: string(r.Module.Program), ArtifactSHA256: string(r.Module.Wasm), SchemaVersion: r.Module.Schema.Canonicalizer, SchemaSHA256: r.Module.Schema.SHA256, ConfigurationSHA256: config, SigningRecovery: recovery, Members: members}, nativecapture.DefaultLimits())
}
func readNativeOnlineCopy(ctx context.Context, runtime nativeBackupRuntime, copied *nativeOnlineCopy, path string, checkpoint nativecapture.Checkpoint, selection nativecapture.Selection) (nativeevidence.Evidence, nativeDisposal, error) {
	nonce, err := data.NewID()
	if err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, err
	}
	view, err := materializeNativeSelection(ctx, copied.root, path, selection, nonce)
	if err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, err
	}
	defer func() { _ = view.close() }()
	pin, err := readNativeReaderPin(ctx)
	if err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, err
	}
	evidence, disposal, err := readNativeView(ctx, runtime.session, pin, view, checkpoint)
	if err != nil || copied.unchanged(ctx) != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	if err := view.close(); err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, err
	}
	return evidence, disposal, nil
}

func checkNativeCapturePostVerification(ctx context.Context, copied *nativeOnlineCopy, live *engineNativeHandle, candidate extractedPublicationCandidate) error {
	if err := copied.unchanged(ctx); err != nil {
		return result.New(result.NativeCaptureSourceRefused, err)
	}
	if err := live.alive(); err != nil {
		return result.New(result.NativeCaptureLiveRefused, err)
	}
	if err := candidate.unchanged(ctx); err != nil {
		return result.New(result.NativeCaptureCandidateRefused, err)
	}
	return nil
}
