//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/enroll"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativeBackupRuntime struct {
	state     *store.Store
	stateRoot string
	home      string
	session   localexec.Session
	routes    *caddy.Manager
}

func (r nativeBackupRuntime) repository(ctx context.Context, admission *store.NativeRepositoryRead) (*restic.Client, restic.Repository, error) {
	material, err := admission.Material(ctx)
	if err != nil {
		return nil, restic.Repository{}, errEngineSource
	}
	return r.repositoryMaterial(ctx, material, admission, "")
}

func (r nativeBackupRuntime) repositoryMaterial(ctx context.Context, material store.NativeRepositoryMaterial, admission restic.RepositoryAdmission, grant string) (*restic.Client, restic.Repository, error) {
	files := backupcredentials.Files{Root: filepath.Join(r.stateRoot, "credentials")}
	packet, err := nativeRepositoryCredentialPacket(files, material)
	if err != nil {
		return nil, restic.Repository{}, errEngineSource
	}
	defer packet.Clear()
	root, err := engineOpenRoot(r.stateRoot)
	if err != nil {
		return nil, restic.Repository{}, errEngineSource
	}
	defer func() { _ = root.Close() }()
	password, err := readEnginePrivateFile(ctx, root, filepath.Join("native-recovery", material.Scope.BindingID, material.RecoveryReference, "v"+strconv.FormatUint(material.Scope.RecoveryVersion, 10)+".secret"), material.RecoverySHA256, 4096)
	if err != nil {
		return nil, restic.Repository{}, errEngineSource
	}
	defer clear(password)
	repository, err := restic.NewS3Repository(material.Scope, material.Location, password, packet, grant)
	if err != nil {
		return nil, restic.Repository{}, errEngineSource
	}
	asset, err := enroll.ResticAsset(runtime.GOARCH)
	if err != nil {
		return nil, restic.Repository{}, errEngineSource
	}
	client, err := restic.NewClient(asset.BinarySHA256, restic.DefaultLimits(), admission)
	return client, repository, err
}

func nativeRepositoryCredentialPacket(files backupcredentials.Files, material store.NativeRepositoryMaterial) (backupcredentials.Packet, error) {
	ref := material.CredentialReference
	if material.ArchiveCredential != nil {
		expected := *material.ArchiveCredential
		ref = expected.Plan.Scope.FileReference()
		actual, err := files.ArchiveReceipt(ref, material.Scope.CredentialVersion)
		if err != nil || !reflect.DeepEqual(actual, expected) || actual.Plan.Scope.Binding != material.Scope.BindingID || actual.Plan.Scope.Epoch != material.Scope.EpochID || actual.Plan.Scope.CredentialRef != material.CredentialReference || actual.Admit(time.Now().UTC(), time.Minute) != nil {
			return backupcredentials.Packet{}, errEngineSource
		}
	} else {
		receipt, err := files.Receipt(ref, material.Scope.CredentialVersion)
		if err != nil || !nativeCredentialReceiptMatches(material, receipt) {
			return backupcredentials.Packet{}, errEngineSource
		}
	}
	return files.Read(ref, material.Scope.CredentialVersion)
}

func (r nativeBackupRuntime) restorePreparedSource(ctx context.Context, stopped *store.NativeStoppedPreparation, original engineSealedCheckpoint) (*nativeRestoredSource, error) {
	if original.state != r.state || original.stopped != stopped || original.unchanged(ctx) != nil {
		return nil, errEngineSource
	}
	bundle, err := r.state.ReadNativePreparedBundle(ctx, stopped)
	checkpoint, _, initialized := bundle.Source().Initialized()
	if err != nil || !initialized || checkpoint.SHA256() != original.checkpoint.SHA256() || bundle.Point().SourceSealSHA256 != original.original.hash {
		return nil, errEngineSource
	}
	restored, err := r.restorePrepared(ctx, stopped, bundle)
	if err != nil {
		return nil, err
	}
	restored.original = &original
	if restored.unchanged(ctx) != nil {
		_ = restored.close()
		return nil, errEngineSource
	}
	return restored, nil
}

func (r nativeBackupRuntime) restorePreparedEmpty(ctx context.Context, original engineEmptySource) (*nativeRestoredSource, error) {
	if original.state != r.state || original.unchanged(ctx) != nil {
		return nil, errEngineSource
	}
	bundle, err := r.state.ReadNativePreparedBundle(ctx, original.stopped)
	if err != nil || bundle.Source().Kind() != "unpublished" || bundle.Source().SHA256() != original.source.SHA256() || bundle.Point().SourceSealSHA256 != original.original.hash {
		return nil, errEngineSource
	}
	restored, err := r.restorePrepared(ctx, original.stopped, bundle)
	if err != nil {
		return nil, err
	}
	restored.empty = &original
	owner, err := r.state.ReadNativeInitialOwner(ctx, original.stopped)
	if err != nil || owner.SourceSHA256 != original.source.SHA256() {
		_ = restored.close()
		return nil, errEngineSource
	}
	restored.initialPublisher, restored.publisherProofSHA = owner.Publisher, owner.PublisherProofSHA256
	if restored.unchanged(ctx) != nil {
		_ = restored.close()
		return nil, errEngineSource
	}
	return restored, nil
}

func (r nativeBackupRuntime) restorePrepared(ctx context.Context, stopped *store.NativeStoppedPreparation, bundle store.NativePreparedBundle) (*nativeRestoredSource, error) {
	permit, err := r.state.ReadNativeStoppedPermit(ctx, stopped)
	if err != nil {
		return nil, errEngineSource
	}
	preparation, err := r.state.ReadNativeStoppedDescriptorPreparation(ctx, stopped)
	if err != nil {
		return nil, errEngineSource
	}
	admission, err := r.state.NativePreparedRepository(ctx, stopped)
	if err != nil {
		return nil, errEngineSource
	}
	metadata, err := admission.Material(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := nativeTransportContext(ctx, metadata.Scope)
	if err != nil {
		return nil, err
	}
	defer cancel()
	client, repository, err := r.repository(ctx, admission)
	if err != nil {
		return nil, err
	}
	parent, path, err := newNativePreparedRestoreParent(ctx, permit)
	if err != nil {
		return nil, err
	}
	complete := false
	var extracted restic.AuthenticatedRestore
	defer func() {
		if !complete {
			if extracted.Root() != nil {
				_ = extracted.Root().Close()
			}
			_ = parent.Close()
		}
	}()
	extracted, err = client.RestoreExact(ctx, repository, path, parent, "download", bundle.Transport(), bundle.Manifest())
	if err != nil {
		return nil, err
	}
	seal, err := sealEngineRoot(ctx, extracted.Root(), engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil {
		return nil, errEngineSource
	}
	checkpoint, selection, initialized := bundle.Source().Initialized()
	var evidence nativeevidence.Evidence
	if initialized {
		evidence, _, err = r.verifyExtracted(ctx, extracted, parent, path, checkpoint, selection, bundle.SourceEvidence(), bundle.Schema(), bundle.ConfirmedBound(), bundle.FinalPairs())
		if err != nil {
			return nil, err
		}
	}
	point := bundle.Point()
	restored := &nativeRestoredSource{state: r.state, preparation: preparation, permit: permit, root: extracted.Root(), path: filepath.Join(path, "download"), seal: seal, limits: engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16}, pointID: point.PointID, pointSHA256: point.PointSHA256, source: bundle.Source(), checkpoint: checkpoint, checkpointSHA256: point.CheckpointSHA256, manifest: bundle.Manifest(), transport: bundle.Transport(), evidence: evidence, extracted: extracted, parent: parent, ownsParent: true}
	if !extracted.Matches(ctx, parent, "download", bundle.Transport(), bundle.Manifest()) {
		return nil, errEngineSource
	}
	complete = true
	return restored, nil
}

func newNativePreparedRestoreParent(ctx context.Context, permit store.EnginePermit) (*os.Root, string, error) {
	return newNativeRestoreParent(ctx, string(permit.Reserved.Database.Root))
}

func newNativeRestoreParent(ctx context.Context, rootPath string) (*os.Root, string, error) {
	base, err := engineOpenRoot(rootPath)
	if err != nil {
		return nil, "", errEngineSource
	}
	defer func() { _ = base.Close() }()
	if err = engineMakePrivateDirectoriesForRestore(base); err != nil {
		return nil, "", err
	}
	nonce, err := data.NewID()
	if err != nil {
		return nil, "", err
	}
	name := filepath.Join("native-restores", nonce)
	if err := base.Mkdir(name, 0700); err != nil {
		return nil, "", errEngineSource
	}
	if err := syncEngineDirectory(base, "native-restores"); err != nil {
		return nil, "", errEngineSource
	}
	parent, err := base.OpenRoot(name)
	if err != nil || ctx.Err() != nil {
		if parent != nil {
			_ = parent.Close()
		}
		return nil, "", errEngineSource
	}
	return parent, filepath.Join(rootPath, name), nil
}

func engineMakePrivateDirectoriesForRestore(base *os.Root) error {
	if err := engineMakePrivateDirectories(base, "native-restores"); err != nil {
		return err
	}
	return syncEngineDirectory(base, ".")
}

func (r nativeBackupRuntime) verifyExtracted(ctx context.Context, extracted restic.AuthenticatedRestore, parent *os.Root, path string, checkpoint nativecapture.Checkpoint, selection nativecapture.Selection, source nativeevidence.Evidence, schema spacetimedb.CanonicalSchema, confirmed spacetimedb.ConfirmedPairsRecord, final spacetimedb.RecoveredPairs) (nativeevidence.Evidence, nativeDisposal, error) {
	if !extracted.Matches(ctx, parent, "download", extracted.Point(), extracted.Manifest()) {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	nonce, err := data.NewID()
	if err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, err
	}
	view, err := materializeNativeSelection(ctx, extracted.Root(), path, selection, nonce)
	if err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, err
	}
	defer func() { _ = view.close() }()
	pin, err := readNativeReaderPin(ctx)
	if err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, err
	}
	evidence, disposal, err := readNativeView(ctx, r.session, pin, view, checkpoint)
	pairs, pairErr := spacetimedb.RecoveryPairsFromPassive(evidence, schema)
	if err != nil || pairErr != nil || !source.SamePersistedState(evidence) || spacetimedb.CheckConfirmedLowerBound(confirmed, pairs) != nil || spacetimedb.CheckRecoveredPairsEqual(final, pairs) != nil || disposal.nonce != view.nonce || disposal.imageID != pin.ImageID || disposal.at.Before(extracted.VerifiedAt()) || !extracted.Matches(ctx, parent, "download", extracted.Point(), extracted.Manifest()) {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	if err = view.close(); err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, err
	}
	return evidence, disposal, nil
}

func (s *nativeRestoredSource) close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.root != nil {
		err = s.root.Close()
		s.root = nil
	}
	if s.parent != nil && s.ownsParent {
		err = errors.Join(err, s.parent.Close())
		s.parent = nil
	}
	return err
}

func (r nativeBackupRuntime) restoreVerifiedPoint(ctx context.Context, pointID string, stage *nativeRestoreStage) (nativeVerifiedRestore, error) {
	if stage == nil || stage.state != r.state || pointID != stage.pointID || stage.unchanged(ctx) != nil || stage.downloadAbsent(ctx) != nil {
		return nativeVerifiedRestore{}, errEngineSource
	}
	bundle, err := r.state.ReadNativeLiveRestoreBundle(ctx, stage.claim)
	if err != nil || bundle.Point.PointID != pointID {
		return nativeVerifiedRestore{}, errEngineSource
	}
	admission, err := r.state.NativeLiveRestoreRepository(ctx, stage.claim)
	if err != nil {
		return nativeVerifiedRestore{}, errEngineSource
	}
	metadata, err := admission.Material(ctx)
	if err != nil {
		return nativeVerifiedRestore{}, err
	}
	ctx, cancel, err := nativeTransportContext(ctx, metadata.Scope)
	if err != nil {
		return nativeVerifiedRestore{}, err
	}
	defer cancel()
	client, repository, err := r.repository(ctx, admission)
	if err != nil {
		return nativeVerifiedRestore{}, err
	}
	parentPath := filepath.Dir(stage.path)
	extracted, err := client.RestoreExact(ctx, repository, parentPath, stage.root, stage.downloadName, bundle.Transport, bundle.Manifest)
	if err != nil {
		return nativeVerifiedRestore{}, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = extracted.Root().Close()
		}
	}()
	evidence, disposal, err := r.verifyExtracted(ctx, extracted, stage.root, parentPath, bundle.Checkpoint, bundle.Selection, bundle.SourceEvidence, bundle.Schema, bundle.ConfirmedBound, bundle.FinalPairs)
	if err != nil || stage.unchanged(ctx) != nil {
		return nativeVerifiedRestore{}, errEngineSource
	}
	limits := engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16}
	seal, err := sealEngineRoot(ctx, extracted.Root(), limits)
	pairs, pairErr := spacetimedb.RecoveryPairsFromPassive(evidence, bundle.Schema)
	if err != nil || pairErr != nil {
		return nativeVerifiedRestore{}, errEngineSource
	}
	preparation, err := r.state.ReadNativeStoppedDescriptorPreparation(ctx, stage.source.stopped)
	if err != nil {
		return nativeVerifiedRestore{}, errEngineSource
	}
	permit, err := r.state.ReadNativeStoppedPermit(ctx, stage.source.stopped)
	if err != nil {
		return nativeVerifiedRestore{}, errEngineSource
	}
	restored := &nativeRestoredSource{state: r.state, preparation: preparation, permit: permit, root: extracted.Root(), path: stage.path, seal: seal, limits: limits, pointID: pointID, pointSHA256: bundle.Point.PointSHA256, source: bundle.Source, checkpoint: bundle.Checkpoint, checkpointSHA256: bundle.Point.CheckpointSHA256, manifest: bundle.Manifest, transport: bundle.Transport, evidence: evidence, original: &stage.source, extracted: extracted, parent: stage.root}
	if restored.unchanged(ctx) != nil {
		return nativeVerifiedRestore{}, errEngineSource
	}
	tree := data.EngineTreeSeal{Root: engineTransitionDirectory(stage.rootIdentity), Parent: engineTransitionDirectory(stage.parentIdentity), Directory: engineTransitionDirectory(seal.root), ContentsSHA256: engineTransitionSealDigest(seal)}
	_, err = r.state.RecordNativeVerifiedRestore(ctx, stage.claim, extracted, store.NativeRestoreObservation{Parent: stage.root, TargetTree: tree, SelectedPointID: pointID, SelectedPointSHA256: bundle.Point.PointSHA256, SourceSHA256: bundle.Source.SHA256(), CheckpointSHA256: bundle.Checkpoint.SHA256(), ManifestSHA256: bundle.Manifest.SHA256(), TransportSHA256: bundle.Point.TransportSHA256, SourceNativeSHA256: bundle.SourceEvidence.SHA256(), Evidence: evidence, RecoveredPairsSHA256: pairs.SHA256(), ReaderNonce: disposal.nonce, ReaderViewSHA256: disposal.viewHash, ReaderImageID: disposal.imageID, ReaderDisposedAt: disposal.at})
	if err != nil {
		return nativeVerifiedRestore{}, err
	}
	complete = true
	return nativeVerifiedRestore{source: restored, evidence: evidence, disposal: disposal}, nil
}

func nativeCredentialReceiptMatches(material store.NativeRepositoryMaterial, receipt backupcredentials.Receipt) bool {
	v := material.CredentialRecord
	if !receipt.Valid() || v.ID != material.CredentialReceiptID || v.ID != credentialRecordID("receipt", receipt.PlanID) || v.Kind != "receipt" || v.PlanID != receipt.PlanID || v.PlanHash != receipt.PlanID || v.Requester != receipt.Requester || v.App != receipt.Scope.App || v.CredentialRef != receipt.Scope.CredentialRef || v.TargetHash != receipt.Scope.TargetHash || v.PolicyHash != receipt.Scope.PolicyHash || string(v.BindingID) != receipt.Scope.Binding || string(v.EpochID) != receipt.Scope.Epoch || string(v.Destination) != receipt.Scope.Destination || v.Version != receipt.Version || !v.ReceivedAt.Equal(receipt.ReceivedAt) || (v.ExpiresAt == nil) != (receipt.ExpiresAt == nil) {
		return false
	}
	return v.ExpiresAt == nil || v.ExpiresAt.Equal(*receipt.ExpiresAt)
}
