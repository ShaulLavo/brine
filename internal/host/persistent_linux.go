//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type DataPreparation struct {
	State           *store.Store
	StateRoot, Home string
	Permits         replication.PermitReader
	Publisher       interface {
		Publish(context.Context, replication.Artifacts) error
	}
	Services apply.ReplicaServices
	Units    systemd.Adapter
}

// Preparation is journaled under the operation's host lock. It never stops an
// existing replicator, initializes SQLite, or deletes data during compensation.
func (p DataPreparation) PreparePersistent(ctx context.Context, operation string, planned plan.Plan, desired policy.Desired) error {
	if desired.Stateless() {
		return nil
	}
	if p.State == nil || p.Publisher == nil || p.Units == nil || p.Services == nil || p.Permits == nil {
		return replication.ErrPermit
	}
	if err := ensurePrivateChild(p.Home, ".config/systemd/user"); err != nil {
		return err
	}
	artifacts, bindings, err := p.expected(ctx, planned, desired)
	if err != nil {
		return err
	}
	for i, artifact := range artifacts {
		if err = p.Publisher.Publish(ctx, artifact); err != nil {
			return err
		}
		if err = p.State.CommitReplicaBinding(ctx, bindings[i]); err != nil {
			return err
		}
	}
	if err = p.Units.DaemonReload(ctx); err != nil {
		return err
	}
	orchestrator := apply.ReplicaOrchestrator{Permits: p.Permits, Services: p.Services, Locks: apply.KernelReplicaLocks{}}
	for i, artifact := range artifacts {
		binding := bindings[i]
		request := replication.ReplicaPermitRequest{DatabaseID: string(binding.DatabaseID), BindingID: string(binding.BindingID), EpochID: string(binding.EpochID), ConfigHash: replication.ConfigHash(artifact.Config)}
		if err = orchestrator.Activate(ctx, apply.ReplicaActivation{ReplicaPermitRequest: request, LifetimeLock: artifact.LifetimeLock}); err != nil {
			return err
		}
	}
	return nil
}
func (p DataPreparation) PersistentPrepared(ctx context.Context, operation string, planned plan.Plan, desired policy.Desired) (bool, error) {
	if desired.Stateless() {
		return true, nil
	}
	if p.State == nil || p.Units == nil || p.Permits == nil {
		return false, replication.ErrPermit
	}
	artifacts, bindings, err := p.expected(ctx, planned, desired)
	if err != nil {
		return false, err
	}
	for i, artifact := range artifacts {
		binding := bindings[i]
		committed, err := p.State.ReadReplicaPermitByBinding(ctx, binding.BindingID)
		if err != nil || !committed.Replica.Committed || committed.Replica != binding {
			return false, replication.ErrPermit
		}
		if err = replication.ReplicaPermit(ctx, p.Permits, replication.ReplicaPermitRequest{DatabaseID: string(binding.DatabaseID), BindingID: string(binding.BindingID), EpochID: string(binding.EpochID), ConfigHash: replication.ConfigHash(artifact.Config)}); err != nil {
			return false, err
		}
		if err = data.VerifyRunnerFile(artifact.ServicePath); err != nil {
			return false, err
		}
		raw, err := os.ReadFile(artifact.ServicePath)
		if err != nil || string(raw) != string(artifact.Service) {
			return false, replication.ErrPermit
		}
		name, err := replication.ServiceName(string(binding.BindingID))
		if err != nil {
			return false, err
		}
		unit, err := systemd.ParseUnit(name)
		if err != nil {
			return false, err
		}
		properties, err := p.Units.Show(ctx, unit)
		if err != nil || properties.ActiveState != "active" || properties.SubState != "running" {
			return false, replication.ErrPermit
		}
	}
	return ctx.Err() == nil, ctx.Err()
}
func (p DataPreparation) expected(ctx context.Context, planned plan.Plan, desired policy.Desired) ([]replication.Artifacts, []data.ReplicaBinding, error) {
	if len(planned.DataMounts) == 0 || len(planned.DataMounts) != len(desired.Databases) || desired.Backup == nil {
		return nil, nil, replication.ErrPermit
	}
	schema, err := p.State.CandidateWriterSchema(ctx, planned.DataMounts[0].Database.IncarnationID, desired)
	if err != nil || !data.WriterCompatibleWithAllocations(ctx, schema.Bindings, desired.SchemaCompatibility, schema.Definitions, schema.Allocations) {
		return nil, nil, replication.ErrPermit
	}
	if len(planned.DataCredentials) != len(planned.DataMounts) {
		return nil, nil, replication.ErrPermit
	}
	frozen := map[data.ReplicaBindingID]data.CredentialEvidence{}
	for _, proof := range planned.DataCredentials {
		if _, duplicate := frozen[proof.BindingID]; duplicate || !proof.Admits(proof.BindingID, desired.PolicyHash, time.Now().UTC(), time.Minute) {
			return nil, nil, replication.ErrPermit
		}
		frozen[proof.BindingID] = proof
	}
	artifacts := make([]replication.Artifacts, 0, len(planned.DataMounts))
	bindings := make([]data.ReplicaBinding, 0, len(planned.DataMounts))
	for _, mount := range planned.DataMounts {
		permit, err := p.State.ReadReplicaPermitByBinding(ctx, mount.BindingID)
		if err != nil || permit.Database != mount.Database || mount.HostPath != filepath.Join(string(mount.Database.Root), mount.Database.RelativeDirectory) || mount.ContainerPath != mount.Database.MountPath || mount.BindingID != mount.Database.ReplicaBindingID {
			return nil, nil, replication.ErrPermit
		}
		binding := permit.Replica
		var declaration *data.Database
		for i := range desired.Databases {
			if desired.Databases[i].Name == mount.Database.Name {
				declaration = &desired.Databases[i]
			}
		}
		if declaration == nil || (declaration.SyncInterval < desired.Backup.MinSyncInterval || declaration.SyncInterval > desired.Backup.MaxSyncInterval || desired.Backup.SnapshotInterval < desired.Backup.MinSnapshotInterval || desired.Backup.SnapshotInterval > desired.Backup.MaxSnapshotInterval) {
			return nil, nil, replication.ErrPermit
		}
		proof, ok := frozen[binding.BindingID]
		if !ok || (binding.Committed && binding.CredentialVersion != proof.Version) {
			return nil, nil, replication.ErrPermit
		}
		delete(frozen, binding.BindingID)
		version := proof.Version
		record, err := p.State.CredentialReceipt(ctx, binding.BindingID, version)
		if err != nil || record.EpochID != binding.EpochID || record.Destination != binding.Destination.Reference || record.PolicyHash != desired.PolicyHash || (record.ExpiresAt != nil && !time.Now().UTC().Add(time.Minute).Before(*record.ExpiresAt)) {
			return nil, nil, replication.ErrPermit
		}
		observedCredential := data.CredentialEvidence{BindingID: record.BindingID, EpochID: record.EpochID, Destination: record.Destination, Reference: record.CredentialRef, Version: record.Version, PolicyHash: record.PolicyHash, ReceivedAt: record.ReceivedAt, ExpiresAt: record.ExpiresAt}
		if !proof.Equal(observedCredential) || record.CredentialRef != binding.Destination.CredentialRef {
			return nil, nil, replication.ErrPermit
		}
		files := backupcredentials.Files{Root: filepath.Join(p.StateRoot, "credentials")}
		credentials, err := files.Read(binding.Destination.CredentialRef, record.Version)
		if err != nil {
			return nil, nil, err
		}
		credentials.Clear()
		credentialPath, err := files.Path(binding.Destination.CredentialRef, record.Version)
		if err != nil {
			return nil, nil, err
		}
		configDir := filepath.Join(p.StateRoot, "replication", string(binding.BindingID))
		configPath := filepath.Join(configDir, "litestream.yml")
		lockPath := filepath.Join(p.StateRoot, "replica-locks", string(binding.BindingID)+".lock")
		projected := replication.Binding{IncarnationID: string(mount.Database.IncarnationID), DatabaseID: string(mount.Database.DatabaseID), BindingID: string(binding.BindingID), EpochID: string(binding.EpochID), DBPath: filepath.Join(mount.HostPath, string(mount.Database.Filename)), SocketPath: filepath.Join(configDir, "control.sock"), Endpoint: binding.Destination.Endpoint, Bucket: binding.Destination.Bucket, Prefix: binding.RemotePrefix, Region: binding.Destination.Region, ForcePathStyle: binding.Destination.PathStyle, Cadence: replication.Cadence{SyncInterval: declaration.SyncInterval, SnapshotInterval: desired.Backup.SnapshotInterval}}
		config, err := replication.RenderConfig(projected)
		if err != nil {
			return nil, nil, err
		}
		unit, err := quadlet.RenderReplica(quadlet.ReplicaUnitOptions{Binding: projected, Config: config, ConfigPath: configPath, CredentialPath: credentialPath, LifetimeLock: lockPath})
		if err != nil {
			return nil, nil, err
		}
		binding.ConfigContent = string(config)
		binding.ConfigSHA256 = strings.TrimPrefix(replication.ConfigHash(config), "sha256:")
		binding.UnitSHA256 = strings.TrimPrefix(unit.Hash(), "sha256:")
		binding.ConfigFile = configPath
		binding.CredentialVersion = record.Version
		binding.CredentialFile = credentialPath
		binding.SocketFile = projected.SocketPath
		binding.LifetimeLockFile = lockPath
		binding.Committed = true
		artifacts = append(artifacts, replication.Artifacts{Binding: projected, Config: config, Service: unit.Bytes(), ConfigPath: configPath, LifetimeLock: lockPath, ServicePath: filepath.Join(p.Home, ".config/systemd/user", unit.Name())})
		bindings = append(bindings, binding)
	}
	return artifacts, bindings, nil
}

func ensurePrivateChild(home, relative string) error {
	if filepath.IsAbs(relative) || filepath.Clean(relative) != relative || strings.HasPrefix(relative, "..") {
		return data.ErrInvalid
	}
	if _, err := data.InspectRoot(home); err != nil {
		return err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	current := ""
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		if err = root.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			return data.ErrInvalid
		}
		if _, err = data.InspectRoot(filepath.Join(home, current)); err != nil {
			return err
		}
		directory, err := root.Open(current)
		if err != nil {
			return err
		}
		err = directory.Sync()
		directory.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
