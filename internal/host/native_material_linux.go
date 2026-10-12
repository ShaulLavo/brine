//go:build linux

package host

import (
	"context"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativeMaterialOperations struct{ service Service }

func (o nativeMaterialOperations) DeliverMaterial(ctx context.Context, args dispatch.NativeMaterialArgs) (nativematerial.Receipt, error) {
	if !args.Valid() || o.service.Store == nil || o.service.Policy == nil || o.service.managedObserver == nil || o.service.managedPreparation == nil {
		return nativematerial.Receipt{}, result.New(result.DependencyMissing, nil)
	}
	packet := args.Packet()
	defer clear(packet)
	material, expiry, err := decodeNativeMaterialPacket(packet, time.Now().UTC())
	if err != nil {
		return nativematerial.Receipt{}, result.New(result.InvalidUsage, nil)
	}
	defer material.Clear()
	lock, err := o.service.Store.AcquireHostLock(ctx)
	if err != nil {
		return nativematerial.Receipt{}, err
	}
	defer func() { _ = lock.Release() }()
	p, d, err := o.service.Store.LoadPlan(ctx, args.PreparationPlanID)
	if err != nil || p.App != args.App || p.Managed == nil || policy.ValidateManagedDesired(d) != nil {
		return nativematerial.Receipt{}, result.New(result.PolicyRefused, nil)
	}
	pol, err := o.service.Policy.Load(ctx)
	if err != nil || pol.Hash() != d.PolicyHash {
		return nativematerial.Receipt{}, result.New(result.PolicyRefused, nil)
	}
	projection, err := o.service.Store.ReadManagedApp(ctx, args.App)
	if err != nil || projection.Reserved.Database != p.Managed.Database || projection.Reserved.Archive != p.Managed.Archive {
		return nativematerial.Receipt{}, result.New(result.Conflict, nil)
	}
	prepared, err := o.service.Store.ReadManagedPreparation(ctx, p.Managed.Database.BindingID)
	if err != nil || prepared.PlanID != args.PreparationPlanID {
		return nativematerial.Receipt{}, result.New(result.Conflict, nil)
	}
	owner, err := o.service.Store.GetOperation(ctx, prepared.OperationID)
	if err != nil || owner.Requester != o.service.Requester {
		return nativematerial.Receipt{}, result.New(result.Conflict, nil)
	}
	input, previousErr := o.service.Store.NativeMaterialByKey(ctx, o.service.Requester, args.IdempotencyKey)
	if previousErr == nil {
		expected := nativeMaterialMetadata(d, material, expiry)
		actual := input.Receipt.Secrets
		actual.Publisher.Version = 1
		actual.SigningPrivate.Version = 1
		actual.SigningPublic.Version = 1
		actual.AuthPolicy.Version = 1
		if input.Receipt.App != args.App || input.Receipt.PreparationPlanID != args.PreparationPlanID || actual != expected {
			return nativematerial.Receipt{}, result.New(result.Conflict, nil)
		}
		return o.service.Store.NativeMaterialReceipt(ctx, input.Receipt.OperationID, o.service.Requester)
	}
	if !errors.Is(previousErr, store.ErrNotFound) {
		return nativematerial.Receipt{}, result.New(result.Conflict, nil)
	}
	candidate, err := o.candidate(ctx, p, d, material, expiry)
	if err != nil {
		return nativematerial.Receipt{}, result.New(result.Conflict, nil)
	}
	input, replay, err := o.service.Store.ReserveNativeMaterial(ctx, args.App, args.PreparationPlanID, o.service.Requester, args.IdempotencyKey, candidate)
	if err != nil {
		return nativematerial.Receipt{}, result.New(result.Conflict, nil)
	}
	if replay {
		return o.service.Store.NativeMaterialReceipt(ctx, input.Receipt.OperationID, o.service.Requester)
	}
	if err = o.service.Store.TransitionOperation(ctx, input.Receipt.OperationID, ops.Queued, ops.Preparing); err != nil {
		return o.materialUnknown(ctx, input)
	}
	if err = o.deliver(ctx, input, p, d, material); err != nil {
		return o.materialUnknown(ctx, input)
	}
	receipt, err := o.service.Store.CompleteNativeMaterial(ctx, input)
	if err != nil {
		return o.materialUnknown(ctx, input)
	}
	return receipt, nil
}
func (o nativeMaterialOperations) materialUnknown(ctx context.Context, input store.NativeMaterialInput) (nativematerial.Receipt, error) {
	inspect, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	op, err := o.service.Store.GetOperation(inspect, input.Receipt.OperationID)
	if err == nil && op.State == ops.Preparing {
		_ = o.service.Store.TransitionOperation(inspect, op.ID, ops.Preparing, ops.RecoveryRequired)
	}
	r := input.Receipt
	r.Outcome = "recovery_required"
	return r, nil
}
func (o nativeMaterialOperations) candidate(ctx context.Context, p plan.Plan, d policy.Desired, material *EngineSecretMaterial, expiry time.Time) (data.EngineRuntime, error) {
	_, current, err := o.service.Store.ReadEngineHead(ctx, p.Managed.Database.BindingID)
	if err == nil {
		current.Secrets = nativeMaterialMetadata(d, material, expiry)
		return current, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return data.EngineRuntime{}, err
	}
	prepared, err := o.service.Store.ReadManagedPreparation(ctx, p.Managed.Database.BindingID)
	if err != nil || prepared.PlanID != p.Hash {
		return data.EngineRuntime{}, errEngineSource
	}
	if _, err := o.service.Store.ReadManagedPreparationFence(ctx, prepared); err != nil {
		return data.EngineRuntime{}, err
	}
	ready, err := o.service.managedPreparation.ManagedPrepared(ctx, prepared.OperationID, p, d)
	if err != nil || !ready {
		return data.EngineRuntime{}, errEngineSource
	}
	metadata := d.SpacetimeDB.Module.Metadata
	image := p.Managed.EngineImage
	if metadata == nil || image.ManifestDigest.Value == nil || image.Platform.OS != "linux" {
		return data.EngineRuntime{}, errEngineSource
	}
	index, err := podman.ParseImage(data.SpacetimeDBImage)
	if err != nil {
		return data.EngineRuntime{}, err
	}
	repository := "docker.io/clockworklabs/spacetime@"
	selected, err := podman.ParseImage(repository + *image.ManifestDigest.Value)
	if err != nil {
		return data.EngineRuntime{}, err
	}
	client := podman.New(o.service.managedObserver.session)
	pull, cancel := context.WithTimeout(ctx, 2*time.Minute)
	err = client.Pull(pull, index)
	cancel()
	if err != nil {
		return data.EngineRuntime{}, errors.Join(errEngineSource, err)
	}
	observed, err := client.InspectStored(ctx, index, selected)
	if err != nil || observed.ManifestDigest != *image.ManifestDigest.Value || observed.Platform.OS != image.Platform.OS || observed.Platform.Architecture != image.Platform.Arch {
		return data.EngineRuntime{}, errors.Join(errEngineSource, err)
	}
	resources := d.SpacetimeDB.Resources
	runtime := data.EngineRuntime{BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, State: data.EngineUnpublished, Image: data.SpacetimeDBImage, ManifestDigest: observed.ManifestDigest, ImageID: observed.ImageID, Version: d.SpacetimeDB.Version, Runtime: d.SpacetimeDB.Runtime, HostPort: prepared.EnginePort, Names: []string{}, Module: data.EngineModuleDefinition{Program: data.NativeProgramWireHash(metadata.RuntimeModuleHash), Wasm: data.WasmSHA256(metadata.WasmSHA256), Schema: data.EngineSchemaDefinition{Marker: d.SpacetimeDB.Module.Marker, Canonicalizer: metadata.Canonicalizer, SHA256: metadata.SchemaSHA256}}, Secrets: nativeMaterialMetadata(d, material, expiry), MemoryMB: resources.MemoryMB, PIDsLimit: resources.PIDsLimit, PagePoolMB: resources.PagePoolMB, StopSeconds: resources.StopTimeoutSeconds, StartupSeconds: 60, CopyBytes: 64 << 20, CopyFiles: 16384}
	unit, err := EngineServiceBytes(runtime)
	if err != nil {
		return data.EngineRuntime{}, err
	}
	runtime.UnitSHA256 = managedServiceHash(unit)
	return runtime, runtime.Validate()
}
func nativeMaterialMetadata(d policy.Desired, m *EngineSecretMaterial, expiry time.Time) data.EngineSecrets {
	refs := []string{string(d.SpacetimeDB.Publisher), string(d.SpacetimeDB.SigningPrivate), string(d.SpacetimeDB.SigningPublic), string(d.SpacetimeDB.Auth)}
	var values [4]data.EngineSecretVersion
	for i, ref := range refs {
		values[i] = data.EngineSecretVersion{Reference: ref, Version: 1, SHA256: managedServiceHash(m.values[i])}
	}
	values[0].ExpiresAt = expiry
	return data.EngineSecrets{Publisher: values[0], SigningPrivate: values[1], SigningPublic: values[2], AuthPolicy: values[3]}
}
func (o nativeMaterialOperations) deliver(ctx context.Context, input store.NativeMaterialInput, p plan.Plan, d policy.Desired, material *EngineSecretMaterial) error {
	guard, err := o.service.Store.NativeMaterialPreparation(ctx, input)
	if err != nil {
		return err
	}
	defer func() { _ = guard.Close() }()
	if input.Initial {
		ready, err := o.service.managedPreparation.ManagedPrepared(ctx, input.PreparationOperationID, p, d)
		if err != nil || !ready {
			return errEngineSource
		}
	} else {
		permit, err := o.service.Store.ReadEnginePermit(ctx, input.Receipt.BindingID)
		if err != nil {
			return errEngineSource
		}
		current, err := readEngineMaterial(ctx, guard.StateRoot(), permit)
		if err != nil {
			return errEngineSource
		}
		current.clear()
	}
	if err = WriteEngineMaterial(ctx, guard, input.Runtime, material); err != nil {
		return err
	}
	if input.Initial {
		ready, err := o.service.managedPreparation.ManagedPrepared(ctx, input.PreparationOperationID, p, d)
		if err != nil || !ready {
			return errEngineSource
		}
	}
	if err = guard.Close(); err != nil {
		return err
	}
	if !input.Initial {
		return nil
	}
	if err = o.service.Store.RegisterEngineModule(ctx, input.Runtime.BindingID, input.Runtime.Module); err != nil {
		return err
	}
	return o.service.Store.CommitEngineRuntime(ctx, input.Runtime, input.FenceID, input.PreparationOperationID)
}
func (o nativeMaterialOperations) InspectMaterial(ctx context.Context, args dispatch.NativeMaterialInspectArgs) (nativematerial.Receipt, error) {
	return inspectNativeMaterial(ctx, o.service.Store, o.service.Requester, args)
}
