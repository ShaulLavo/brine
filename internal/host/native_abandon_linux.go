//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func nativeAbandonTask(service Service, runtime nativeBackupRuntime) jobs.TaskHandler {
	return func(ctx context.Context, op ops.Operation) (receipt json.RawMessage, taskErr error) {
		scoped, err := nativeAbandonTaskService(ctx, service, op)
		if err != nil || runtime.state != scoped.Store {
			return nil, result.New(result.PolicyRefused, err)
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		lock, err := scoped.Store.AcquireHostLock(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { taskErr = errors.Join(taskErr, lock.Release()) }()
		abandonment, err := scoped.Store.OpenNativeAbandonment(ctx, op.ID)
		if err != nil {
			return nil, err
		}
		defer func() { taskErr = errors.Join(taskErr, abandonment.Close()) }()
		expected, err := abandonment.Expected(ctx)
		if err != nil {
			return nil, err
		}
		if err := checkNativeAbandonPolicy(ctx, scoped, expected.Point.PolicySHA256); err != nil {
			return nil, err
		}
		observed, err := observeNativeAbandonment(ctx, scoped, runtime, abandonment)
		if err != nil {
			return nil, err
		}
		completed, err := scoped.Store.CompleteNativeAbandonment(ctx, abandonment, observed)
		if err != nil {
			return nil, err
		}
		return json.Marshal(completed)
	}
}

func observeNativeAbandonment(ctx context.Context, service Service, runtime nativeBackupRuntime, abandonment *store.NativeAbandonment) (store.NativeAbandonObservation, error) {
	expected, err := abandonment.Expected(ctx)
	if err != nil {
		return store.NativeAbandonObservation{}, err
	}
	point, stop, app, err := runtime.state.ReadNativePreparedStopExpectation(ctx, expected.Point.PointID)
	if err != nil || point.PointSHA256 != expected.Point.PointSHA256 || app != expected.App {
		return store.NativeAbandonObservation{}, errEngineSource
	}
	record, err := runtime.state.ReadNativePreparedRoutes(ctx, point.PointID)
	if err != nil {
		return store.NativeAbandonObservation{}, err
	}
	routes, current, previous, err := runtime.preparedObservedRouteStates(ctx, service, record)
	if err != nil {
		return store.NativeAbandonObservation{}, err
	}
	stop, err = runtime.inspectPreparedStop(ctx, app, expected.Permit, stop, routes, current, previous)
	if err != nil {
		return store.NativeAbandonObservation{}, err
	}
	if err := verifyNativeAbandonCode(ctx, runtime, app, expected); err != nil {
		return store.NativeAbandonObservation{}, err
	}
	seal, err := inspectNativeAbandonSource(ctx, expected)
	if err != nil {
		return store.NativeAbandonObservation{}, err
	}
	routeBytes, err := json.Marshal(record)
	if err != nil {
		return store.NativeAbandonObservation{}, errEngineSource
	}
	if _, err := runtime.inspectPreparedStop(ctx, app, expected.Permit, stop, routes, current, previous); err != nil {
		return store.NativeAbandonObservation{}, err
	}
	finalExpected, err := abandonment.Expected(ctx)
	if err != nil {
		return store.NativeAbandonObservation{}, err
	}
	if err := checkNativeAbandonPolicy(ctx, service, finalExpected.Point.PolicySHA256); err != nil {
		return store.NativeAbandonObservation{}, err
	}
	routeHash := sha256.Sum256(routeBytes)
	return store.NativeAbandonObservation{Stop: stop, SourceSHA256: point.SourceSealSHA256, CodeSHA256: expected.CodeSHA256, RoutesSHA256: hex.EncodeToString(routeHash[:]), Directory: data.EngineDirectoryIdentity{Device: seal.root.Device, Inode: seal.root.Inode}, CheckpointSHA: expected.Point.CheckpointSHA256, ObservedAt: time.Now().UTC()}, nil
}

func inspectNativeAbandonSource(ctx context.Context, expected store.NativeAbandonExpected) (engineTreeSeal, error) {
	root, err := openEngineSource(expected.Permit.Reserved.Database)
	if err != nil {
		return engineTreeSeal{}, err
	}
	defer func() { _ = root.Close() }()
	sealed, err := sealOwnedEngineSource(ctx, root, engineCopyLimits{Bytes: expected.Permit.Runtime.CopyBytes, Files: expected.Permit.Runtime.CopyFiles, Depth: 16})
	checkpoint := expected.Checkpoint
	initialized := checkpoint.SHA256() != ""
	allocation := expected.Permit.Allocation
	if err != nil || !initialized || allocation == nil || !engineSourceSealMatches(sealed, expected.Point.SourceSealSHA256) || sealed.root.Device != allocation.DirectoryDevice || sealed.root.Inode != allocation.DirectoryInode || checkpoint.SHA256() != expected.Point.CheckpointSHA256 || !nativeCheckpointMatchesSeal(checkpoint.Record().Members, sealed) {
		return engineTreeSeal{}, errEngineSource
	}
	return sealed, nil
}

func verifyNativeAbandonCode(ctx context.Context, runtime nativeBackupRuntime, app string, expected store.NativeAbandonExpected) error {
	if verifyEngineService(ctx, runtime.home, expected.Permit) != nil {
		return errEngineSource
	}
	unit, err := systemd.ParseUnit(engineContainerName(expected.Permit.Runtime.BindingID) + ".service")
	if err != nil || verifyEngineLoadedService(ctx, runtime.session, runtime.home, unit) != nil {
		return errEngineSource
	}
	material, err := readEngineMaterial(ctx, runtime.stateRoot, expected.Permit)
	if err != nil {
		return err
	}
	material.clear()
	front := expected.Release.Managed.Frontend
	if front == nil {
		return nil
	}
	if quadlet.VerifyCurrent(ctx, runtime.home, front.Unit.Name, front.Unit.Hash) != nil || verifyManagedFrontendLoaded(ctx, runtime.session, runtime.home, app) != nil {
		return errEngineSource
	}
	planned, desired, err := runtime.state.LoadPlan(ctx, expected.Release.PlanID)
	if err != nil {
		return errEngineSource
	}
	managed := managedRuntime{state: runtime.state, observer: managedEngineObserver{session: runtime.session}, home: runtime.home}
	if err := managed.stoppedFrontendImage(ctx, planned, desired); err != nil {
		return err
	}
	client := podman.New(runtime.session)
	for _, secret := range front.Secrets {
		parsed, err := podman.ParseSecretName(secret.VersionName)
		if err != nil {
			return errEngineSource
		}
		present, err := client.SecretExists(ctx, parsed)
		if err != nil || !present {
			return errEngineSource
		}
	}
	name, err := managedFrontendContainerName(app)
	if err != nil {
		return err
	}
	state, err := client.ContainerState(ctx, name)
	if !managedFrontendStopped(state, err) {
		return errEngineSource
	}
	return nil
}

func checkNativeAbandonPolicy(ctx context.Context, service Service, expected string) error {
	current, err := service.Policy.Load(ctx)
	if err != nil || current.Hash() != "sha256:"+expected {
		return result.New(result.PolicyRefused, err)
	}
	return nil
}
