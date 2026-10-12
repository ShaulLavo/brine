//go:build linux

package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/enroll"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"runtime"
	"strconv"
)

// executeNativeLiveRestore consumes the actual stopped source and retained20
// extraction reservation. No path or serialized observation can invoke effects.
func (r nativeBackupRuntime) executeNativeLiveRestore(ctx context.Context, stage *nativeRestoreStage, preparation *store.EnginePreparation, lifetime *data.EngineLock, routes *caddy.Manager) (liverestore.Event, error) {
	if stage == nil || stage.state != r.state || !preparation.Active(stage.claim.Binding().BindingID) || !lifetime.HoldsLifetime(r.stateRoot, stage.claim.Binding().BindingID) {
		return liverestore.Event{}, errEngineSource
	}
	verified, err := r.restoreVerifiedPoint(ctx, stage.pointID, stage)
	if err != nil {
		return liverestore.Event{}, err
	}
	defer func() { _ = verified.source.close() }()
	if _, err = r.state.RecordNativeLiveRestoreTransitionIntent(ctx, stage.claim); err != nil {
		return liverestore.Event{}, err
	}
	if err = prepareNativeRestoreTarget(ctx, stage, verified, verified.source.extracted, r.home); err != nil {
		return liverestore.Event{}, err
	}
	incoming, err := r.state.ClaimNativeLiveRestoreIncomingRepository(ctx, stage.claim)
	if err != nil {
		return liverestore.Event{}, err
	}
	initialized, err := r.initializeNativeRestoreRepository(ctx, incoming, filepath.Dir(stage.path))
	if err != nil {
		return liverestore.Event{}, err
	}
	capability, err := r.state.ClaimNativeLiveRestoreTransition(ctx, stage.claim, incoming, initialized)
	if err != nil {
		return liverestore.Event{}, err
	}
	return r.finishNativeLiveRestore(ctx, capability, preparation, lifetime, stage.claim.Binding(), routes)
}

func (r nativeBackupRuntime) initializeNativeRestoreRepository(ctx context.Context, incoming *store.NativeIncomingRepository, workspace string) (restic.InitializedRepository, error) {
	metadata, err := incoming.Metadata(ctx)
	if err != nil {
		return restic.InitializedRepository{}, err
	}
	material := metadata.Material
	files := backupcredentials.Files{Root: filepath.Join(r.stateRoot, "credentials")}
	receipt, err := files.Receipt(material.CredentialReference, material.Scope.CredentialVersion)
	credentialMaterial := material
	credentialMaterial.Scope.EpochID = string(metadata.CredentialEpochID)
	if err != nil || !nativeCredentialReceiptMatches(credentialMaterial, receipt) {
		return restic.InitializedRepository{}, errEngineSource
	}
	packet, err := files.Read(material.CredentialReference, material.Scope.CredentialVersion)
	if err != nil {
		return restic.InitializedRepository{}, errEngineSource
	}
	defer packet.Clear()
	root, err := engineOpenRoot(r.stateRoot)
	if err != nil {
		return restic.InitializedRepository{}, errEngineSource
	}
	defer func() { _ = root.Close() }()
	password, err := readEnginePrivateFile(ctx, root, filepath.Join("native-recovery", material.Scope.BindingID, material.RecoveryReference, "v"+strconv.FormatUint(material.Scope.RecoveryVersion, 10)+".secret"), material.RecoverySHA256, 4096)
	if err != nil {
		return restic.InitializedRepository{}, errEngineSource
	}
	defer clear(password)
	repository, err := restic.NewS3Repository(material.Scope, material.Location, password, packet, metadata.CreationGrantSHA256)
	if err != nil {
		return restic.InitializedRepository{}, errEngineSource
	}
	asset, err := enroll.ResticAsset(runtime.GOARCH)
	if err != nil {
		return restic.InitializedRepository{}, err
	}
	client, err := restic.NewClient(asset.BinarySHA256, restic.DefaultLimits(), incoming)
	if err != nil {
		return restic.InitializedRepository{}, err
	}
	if err = r.state.BeginNativeIncomingInitialization(ctx, incoming); err != nil {
		return restic.InitializedRepository{}, err
	}
	initialized, err := client.Init(ctx, repository, workspace)
	if err != nil {
		return restic.InitializedRepository{}, err
	}
	if _, err = r.state.RecordNativeIncomingInitialized(ctx, incoming, initialized); err != nil {
		return restic.InitializedRepository{}, err
	}
	return initialized, nil
}

func (r nativeBackupRuntime) finishNativeLiveRestore(ctx context.Context, capability *store.NativeRestoreTransition, preparation *store.EnginePreparation, lifetime *data.EngineLock, binding data.SpacetimeDBBinding, routes *caddy.Manager) (liverestore.Event, error) {
	view, err := r.state.ReadNativeRestoreTransition(ctx, capability)
	if err != nil {
		return liverestore.Event{}, err
	}
	tree := &engineTransitionTree{preparation: preparation, lifetime: lifetime, binding: binding, input: view.Input, limits: engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16}}
	// Fresh absence precedes every move, including resuming a recorded move.
	if _, err = r.observeNativeRestoreTransition(ctx, view, tree, routes); err != nil {
		return liverestore.Event{}, err
	}
	if err = relocateNativeRestoreTree(ctx, r.state, capability, tree); err != nil {
		return liverestore.Event{}, err
	}
	view, err = r.state.ReadNativeRestoreTransition(ctx, capability)
	if err != nil {
		return liverestore.Event{}, err
	}
	observed, err := r.observeNativeRestoreTransition(ctx, view, tree, routes)
	if err != nil {
		return liverestore.Event{}, err
	}
	if view.Cursor.Stage == data.EngineTransitionActivationIntent {
		if _, err = r.state.ActivateNativeLiveRestore(ctx, capability, observed); err != nil {
			return liverestore.Event{}, err
		}
		view, err = r.state.ReadNativeRestoreTransition(ctx, capability)
		if err != nil {
			return liverestore.Event{}, err
		}
	}
	if view.Cursor.Stage == data.EngineTransitionActivated {
		observed, err = r.observeNativeRestoreTransition(ctx, view, tree, routes)
		if err != nil {
			return liverestore.Event{}, err
		}
		if _, err = r.state.BeginNativeLiveRestoreCompletion(ctx, capability, observed); err != nil {
			return liverestore.Event{}, err
		}
	}
	observed, err = r.observeNativeRestoreTransition(ctx, view, tree, routes)
	if err != nil {
		return liverestore.Event{}, err
	}
	return r.state.CompleteNativeLiveRestore(ctx, capability, observed)
}

func (r nativeBackupRuntime) observeNativeRestoreTransition(ctx context.Context, view store.NativeRestoreTransitionView, tree *engineTransitionTree, routes *caddy.Manager) (store.NativeRestoreTransitionObservation, error) {
	var zero store.NativeRestoreTransitionObservation
	if tree == nil || !tree.active() || view.Input.SHA256() != tree.input.SHA256() || routes == nil {
		return zero, errEngineSource
	}
	recovery, err := r.state.ReadNativeRestoreRecoveryCode(ctx, view.Input.Common().Scope.PlanID)
	if err != nil {
		return zero, err
	}
	if err = r.nativeRestoreQuiescent(ctx, recovery.App, view.Runtime, tree); err != nil {
		return zero, err
	}
	if err = verifyNativeRestoreRecoveryUnits(ctx, r.home, recovery); err != nil {
		return zero, err
	}
	route, err := r.state.ReadManagedRouteState(ctx, recovery.App)
	if err != nil || route.Present || route.BindingID != view.Runtime.BindingID {
		return zero, errEngineSource
	}
	loaded, err := routes.Observe()
	if err != nil || loaded.Generation != route.Generation || loaded.Files[recovery.App+".caddy"] != "" || routes.CheckDrift(loaded) != nil {
		return zero, errEngineSource
	}
	site, err := caddy.CommittedManagedSite(recovery.Desired, *recovery.Plan.Managed)
	if err != nil {
		return zero, errEngineSource
	}
	if err = routes.InspectLoadedChange(ctx, loaded, caddy.State{Sites: map[string]caddy.Site{recovery.App + ".caddy": site}}); err != nil {
		return zero, errEngineSource
	}
	unit, err := readNativeRestoreUnit(ctx, r.home, view.Runtime)
	if err != nil {
		return zero, err
	}
	material, err := readEngineMaterial(ctx, r.stateRoot, store.EnginePermit{Runtime: view.Runtime})
	if err != nil {
		return zero, err
	}
	material.clear()
	layout, err := tree.inspect(ctx)
	if err != nil || data.ClassifyEngineTransitionLayout(view.Input, layout) == data.EngineLayoutConflict {
		return zero, errEngineSource
	}
	raw, err := json.Marshal(view.Runtime.Secrets)
	if err != nil {
		return zero, err
	}
	if err = r.nativeRestoreQuiescent(ctx, recovery.App, view.Runtime, tree); err != nil {
		return zero, err
	}
	return store.NativeRestoreTransitionObservation{Layout: layout, UnitSHA256: unit, MaterialSHA256: strings.TrimPrefix(managedServiceHash(raw), "sha256:"), RecoveryCodeSHA256: recovery.CodeSHA256, RecoveryCompatibilitySHA256: recovery.CompatibilitySHA256, EngineAbsent: true, ApplicationAbsent: true, IngressClosed: true, PrivateReadersAbsent: true, ObservedAt: time.Now().UTC()}, nil
}

func verifyNativeRestoreRecoveryUnits(ctx context.Context, home string, recovery store.NativeRestoreRecoveryCode) error {
	if recovery.Release.Managed == nil || recovery.Plan.Managed == nil {
		return errEngineSource
	}
	if recovery.Release.Managed.Frontend != nil {
		front := recovery.Release.Managed.Frontend
		if err := quadlet.VerifyCurrent(ctx, home, front.Unit.Name, front.Unit.Hash); err != nil {
			return errEngineSource
		}
	}
	return ctx.Err()
}

func (r nativeBackupRuntime) nativeRestoreQuiescent(ctx context.Context, app string, runtime data.EngineRuntime, tree *engineTransitionTree) error {
	host := archiveHost{session: r.session}
	stopped, err := host.serviceStopped(ctx, engineContainerName(runtime.BindingID)+".service", false)
	if err != nil || !stopped {
		return errEngineSource
	}
	// The frontend may be absent only for an engine-only release.
	recovery, err := r.state.ReadNativeRestoreRecoveryCode(ctx, tree.input.Common().Scope.PlanID)
	if err != nil {
		return errEngineSource
	}
	if recovery.Release.Managed.Frontend != nil {
		stopped, err = host.serviceStopped(ctx, app+".service", false)
		if err != nil || !stopped {
			return errEngineSource
		}
	}
	containers, err := listEngineContainers(ctx, r.session)
	if err != nil {
		return errEngineSource
	}
	live, staged, preserved := tree.relativeNames()
	for _, location := range []string{live, staged, preserved} {
		if !engineSourceAbsent(containers, filepath.Join(string(tree.binding.Root), location)) {
			return errEngineSource
		}
	}
	if !nativeRestoreContainersAbsent(containers, runtime.BindingID, app) {
		return errEngineSource
	}
	unit, err := systemd.ParseUnit(engineContainerName(runtime.BindingID) + ".service")
	if err != nil {
		return errEngineSource
	}
	pending, err := systemd.New(r.session).JobPending(ctx, unit)
	if err != nil || pending {
		return errEngineSource
	}
	return ctx.Err()
}

func nativeRestoreContainersAbsent(containers []engineContainer, binding data.ReplicaBindingID, app string) bool {
	for _, container := range containers {
		if container.Config.Labels["BRINE_PRIVATE_VERIFY"] != "" || container.Config.Labels["BRINE_MODULE_STAGE"] != "" {
			return false
		}
		relevant := container.Name == engineContainerName(binding) || container.Config.Labels["BRINE_ENGINE_BINDING"] == string(binding) || container.Name == "systemd-"+app
		if relevant && (*container.State.Running || *container.State.PID != 0) {
			return false
		}
	}
	return true
}
