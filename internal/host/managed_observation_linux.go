//go:build linux

package host

import (
	"context"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func (o managedEngineObserver) sourceAbsent(ctx context.Context, binding data.SpacetimeDBBinding) error {
	if o.state == nil || o.units == nil || binding.Validate() != nil {
		return errEngineSource
	}
	reserved, err := o.state.ReadReservedEngine(ctx, binding.BindingID)
	if err != nil || reserved.Database != binding {
		return errEngineSource
	}
	unit, err := systemd.ParseUnit(engineContainerName(binding.BindingID) + ".service")
	if err != nil {
		return errEngineSource
	}
	pending, err := o.units.JobPending(ctx, unit)
	if err != nil || pending {
		return errEngineSource
	}
	properties, err := o.units.Show(ctx, unit)
	if err != nil && !managedUnitAbsent(err) {
		return errEngineSource
	}
	if err == nil && (properties.ActiveState != "inactive" && properties.ActiveState != "failed" || properties.SubState != "dead" && properties.SubState != "failed") {
		return errEngineSource
	}
	containers, err := listEngineContainers(ctx, o.session)
	if err != nil || !engineSourceAbsent(containers, binding.SourcePath()) {
		return errEngineSource
	}
	for _, container := range containers {
		if container.Name == engineContainerName(binding.BindingID) || container.Config.Labels["BRINE_ENGINE_BINDING"] == string(binding.BindingID) {
			return errEngineSource
		}
	}
	return ctx.Err()
}

func managedUnitAbsent(err error) bool {
	var execution *localexec.Error
	return errors.As(err, &execution) && execution.Kind == localexec.NotFound
}

func (o managedEngineObserver) current(ctx context.Context, binding data.ReplicaBindingID, expectedRuntime string) (managedCurrentProof, error) {
	if o.state == nil || o.units == nil || !data.ValidID(string(binding)) || !data.ValidCatalogHash(expectedRuntime) {
		return managedCurrentProof{}, errEngineSource
	}
	observe, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	permit, err := o.state.ReadEnginePermit(observe, binding)
	if err != nil || !permit.AllowsVerification(time.Now().UTC()) {
		return managedCurrentProof{}, errEngineSource
	}
	runtimeHash, err := managedRuntimeHash(permit.Runtime)
	if err != nil || runtimeHash != expectedRuntime || verifyEngineService(observe, o.home, permit) != nil {
		return managedCurrentProof{}, errEngineSource
	}
	unit, err := systemd.ParseUnit(engineContainerName(binding) + ".service")
	if err != nil {
		return managedCurrentProof{}, errEngineSource
	}
	pending, err := o.units.JobPending(observe, unit)
	if err != nil || pending {
		return managedCurrentProof{}, errEngineSource
	}
	properties, err := o.units.Show(observe, unit)
	if err != nil || properties.ActiveState != "active" || properties.SubState != "running" {
		return managedCurrentProof{}, errEngineSource
	}
	if verifyEngineLoadedService(observe, o.session, o.home, unit) != nil {
		return managedCurrentProof{}, errEngineSource
	}
	root, err := engineOpenRoot(permit.Reserved.Database.SourcePath())
	if err != nil {
		return managedCurrentProof{}, errEngineSource
	}
	disabled := engineModuleHTTPDisabled(root)
	closeErr := root.Close()
	if !disabled || closeErr != nil {
		return managedCurrentProof{}, errEngineSource
	}
	material, err := readEngineMaterial(observe, o.stateRoot, permit)
	if err != nil {
		return managedCurrentProof{}, errEngineSource
	}
	defer material.clear()
	native, err := o.native(observe, permit, material)
	if err != nil {
		return managedCurrentProof{}, errEngineSource
	}
	defer native.close()
	if native.alive() != nil {
		return managedCurrentProof{}, errEngineSource
	}
	if err := observeManagedMetadata(observe, permit, material); err != nil {
		return managedCurrentProof{}, errEngineSource
	}
	current, err := o.state.ReadEnginePermit(observe, binding)
	if err != nil || !sameEnginePermit(permit, current) || !current.AllowsVerification(time.Now().UTC()) || native.alive() != nil || verifyEngineService(observe, o.home, current) != nil || verifyEngineLoadedService(observe, o.session, o.home, unit) != nil {
		return managedCurrentProof{}, errEngineSource
	}
	pending, err = o.units.JobPending(observe, unit)
	if err != nil || pending || observe.Err() != nil {
		return managedCurrentProof{}, errEngineSource
	}
	return managedCurrentProof{permit: current, observedAt: time.Now().UTC()}, nil
}

func (o managedEngineObserver) native(ctx context.Context, permit store.EnginePermit, material engineMaterial) (*engineNativeHandle, error) {
	containers, err := listEngineContainers(ctx, o.session)
	if err != nil {
		return nil, errEngineSource
	}
	var candidate *engineContainer
	for i := range containers {
		container := &containers[i]
		if container.Config.Labels["BRINE_ENGINE_BINDING"] != string(permit.Runtime.BindingID) && container.Name != engineContainerName(permit.Runtime.BindingID) {
			continue
		}
		if candidate != nil {
			return nil, errEngineSource
		}
		candidate = container
	}
	if candidate == nil {
		return nil, errEngineSource
	}
	// Refuse every other process/container that can reach the native source.
	others := make([]engineContainer, 0, len(containers)-1)
	for _, container := range containers {
		if container.ID != candidate.ID {
			others = append(others, container)
		}
	}
	if !engineSourceAbsent(others, permit.Reserved.Database.SourcePath()) {
		return nil, errEngineSource
	}
	return bindEngineNative(ctx, o.session, *candidate, permit, material)
}

func observeManagedMetadata(ctx context.Context, permit store.EnginePermit, material engineMaterial) error {
	runtime := permit.Runtime
	endpoint, err := spacetimedb.NewPrivateEndpoint("127.0.0.1", runtime.HostPort, string(runtime.Database))
	if err != nil {
		return errEngineSource
	}
	credential, err := spacetimedb.NewPublisherCredential(material.publisher)
	if err != nil {
		return errEngineSource
	}
	expected, err := spacetimedb.NewExpectedDatabase(string(runtime.Database), string(runtime.Publisher), runtime.Names)
	if err != nil {
		return errEngineSource
	}
	observed, err := spacetimedb.ObserveCurrent(ctx, endpoint, credential, expected, spacetimedb.DefaultObserverLimits())
	if err != nil {
		return errEngineSource
	}
	evidence := engineReplayObservation{Database: data.NativeDatabaseIdentity(observed.Database()), Publisher: data.NativePublisherIdentity(observed.Publisher()), Names: observed.Names(), Program: data.NativeProgramWireHash(observed.CurrentModule()), Canonicalizer: observed.Schema().Version(), SchemaSHA256: observed.Schema().SHA256()}
	if !evidence.matches(runtime) {
		return errEngineSource
	}
	return ctx.Err()
}

func (o managedEngineObserver) stopped(ctx context.Context, permit store.EnginePermit) error {
	if o.state == nil || o.units == nil || !permit.AllowsVerification(time.Now().UTC()) {
		return errEngineSource
	}
	unit, err := systemd.ParseUnit(engineContainerName(permit.Runtime.BindingID) + ".service")
	if err != nil {
		return errEngineSource
	}
	pending, err := o.units.JobPending(ctx, unit)
	if err != nil || pending {
		return errEngineSource
	}
	properties, err := o.units.Show(ctx, unit)
	if err != nil && !managedUnitAbsent(err) {
		return errEngineSource
	}
	if err == nil && (properties.ActiveState != "inactive" && properties.ActiveState != "failed" || properties.SubState != "dead" && properties.SubState != "failed") {
		return errEngineSource
	}
	containers, err := listEngineContainers(ctx, o.session)
	if err != nil || !engineBindingAbsent(containers, permit) {
		return errEngineSource
	}
	current, err := o.state.ReadEnginePermit(ctx, permit.Runtime.BindingID)
	if err != nil || !sameEnginePermit(permit, current) || !current.AllowsVerification(time.Now().UTC()) {
		return errEngineSource
	}
	pending, err = o.units.JobPending(ctx, unit)
	if err != nil || pending {
		return errEngineSource
	}
	return ctx.Err()
}
