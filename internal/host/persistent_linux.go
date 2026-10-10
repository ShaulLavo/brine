//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type DataPreparation struct {
	Now             func() time.Time
	Runner          localexec.Runner
	ProbeRoot       func(context.Context, string) (data.RootEvidence, error)
	ProbeMapping    func(context.Context, localexec.Runner, data.RootEvidence, data.RuntimeIdentity) (data.MappingEvidence, error)
	State           *store.Store
	StateRoot, Home string
	Permits         replication.PermitReader
	Publisher       interface {
		Publish(context.Context, replication.Artifacts) error
		PublishConfig(context.Context, replication.Artifacts) error
	}
	Services apply.ReplicaServices
	Units    systemd.Adapter
}

// PreparePersistent runs under the journaled host lock. Cadence revisions restart
// only the selected replica; no path initializes SQLite or deletes application data.
func (p DataPreparation) PreparePersistent(ctx context.Context, operation string, planned plan.Plan, desired policy.Desired) error {
	if planned.Lifecycle == plan.PrepareData {
		return p.prepareAllocation(ctx, planned, desired)
	}
	if desired.Stateless() {
		return nil
	}
	if p.State == nil || p.Publisher == nil || p.Units == nil || p.Services == nil || p.Permits == nil {
		return replication.ErrPermit
	}
	if err := p.verifyApprovedMapping(ctx, planned, desired); err != nil {
		return err
	}
	if err := ensurePrivateChild(p.Home, ".config/systemd/user"); err != nil {
		return err
	}
	artifacts, bindings, err := p.expected(ctx, planned, desired)
	if err != nil {
		return err
	}
	for i, artifact := range artifacts {
		if err = p.prepareReplica(ctx, operation, desired, artifact, bindings[i]); err != nil {
			return err
		}
	}

	return nil
}
func (p DataPreparation) PersistentPrepared(ctx context.Context, operation string, planned plan.Plan, desired policy.Desired) (bool, error) {
	if planned.Lifecycle == plan.PrepareData {
		return p.allocationPrepared(ctx, planned, desired)
	}
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
		if planned.Lifecycle == plan.ReviseReplica {
			record, err := p.State.ReadReplicaRevision(ctx, revisionID(operation, binding))
			if err == nil && (record.After != binding || record.Stage != data.RotationActive) {
				return false, replication.ErrRestartUnknown
			}
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return false, err
			}
		}
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
	readSchema := p.State.CandidateWriterSchema
	if planned.Lifecycle == plan.ReviseReplica {
		readSchema = p.State.ReplicaRevisionSchema
	}
	schema, err := readSchema(ctx, planned.DataMounts[0].Database.IncarnationID, desired)
	if err != nil || !data.WriterCompatibleWithAllocations(ctx, schema.Bindings, desired.SchemaCompatibility, schema.Definitions, schema.Allocations) {
		return nil, nil, replication.ErrPermit
	}
	if len(planned.DataCredentials) != len(planned.DataMounts) {
		return nil, nil, replication.ErrPermit
	}
	frozen := map[data.ReplicaBindingID]data.CredentialEvidence{}
	for _, proof := range planned.DataCredentials {
		if _, duplicate := frozen[proof.BindingID]; duplicate || !proof.Admits(proof.BindingID, desired.PolicyHash, p.now(), time.Minute) {
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
		if err != nil || record.EpochID != binding.EpochID || record.Destination != binding.Destination.Reference || record.PolicyHash != desired.PolicyHash || (record.ExpiresAt != nil && !p.now().Add(time.Minute).Before(*record.ExpiresAt)) {
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
		lockPath := filepath.Join(p.StateRoot, "replica-locks", string(binding.BindingID)+".lock")
		projected := replication.Binding{IncarnationID: string(mount.Database.IncarnationID), DatabaseID: string(mount.Database.DatabaseID), BindingID: string(binding.BindingID), EpochID: string(binding.EpochID), DBPath: filepath.Join(mount.HostPath, string(mount.Database.Filename)), SocketPath: filepath.Join(configDir, "control.sock"), Endpoint: binding.Destination.Endpoint, Bucket: binding.Destination.Bucket, Prefix: binding.RemotePrefix, Region: binding.Destination.Region, ForcePathStyle: binding.Destination.PathStyle, Cadence: replication.Cadence{SyncInterval: declaration.SyncInterval, SnapshotInterval: desired.Backup.SnapshotInterval}}
		config, err := replication.RenderConfig(projected)
		if err != nil {
			return nil, nil, err
		}
		configPath := filepath.Join(configDir, "configs", strings.TrimPrefix(replication.ConfigHash(config), "sha256:")+".yml")
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
	info, err := os.Lstat(home)
	if err != nil {
		return err
	}
	home, relative, err = privateChildBase(home, relative, info, uint32(os.Getegid())) //nolint:gosec // Linux GID is a uint32 syscall identity.
	if err != nil {
		return err
	}
	if _, err := data.InspectRoot(home); err != nil {
		return err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
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
		err = errors.Join(directory.Sync(), directory.Close())
		if err != nil {
			return err
		}
	}
	return nil
}

// privateChildBase recognizes only D7's exact protected home. Its existing
// runner-owned private child is the anchor; the root-owned home is never changed.
func privateChildBase(home, relative string, info os.FileInfo, gid uint32) (string, string, error) {
	if home != "/home/brine" {
		return home, relative, nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode() != os.ModeDir|0755 || stat.Uid != 0 || stat.Gid != gid {
		return "", "", data.ErrInvalid
	}
	parts := strings.SplitN(relative, "/", 2)
	if len(parts) != 2 || (parts[0] != ".config" && parts[0] != ".local" && parts[0] != ".cache") {
		return "", "", data.ErrInvalid
	}
	return filepath.Join(home, parts[0]), parts[1], nil
}

func (p DataPreparation) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}
