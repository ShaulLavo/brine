//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type rotationJournal struct{ state *store.Store }

func (j rotationJournal) ReadRotation(ctx context.Context, id string) (data.CredentialRotation, error) {
	r, err := j.state.ReadCredentialRotation(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		err = backupcredentials.ErrRotationNotFound
	}
	return r, err
}
func (j rotationJournal) WriteRotation(ctx context.Context, previous data.RotationStage, r data.CredentialRotation) error {
	return j.state.WriteCredentialRotation(ctx, previous, r)
}

type replicaRotation struct {
	state           *store.Store
	stateRoot, home string
	services        apply.ReplicaServices
	units           systemd.Adapter
	permits         replication.PermitReader
	verifyRemote    func(context.Context, restore.Destination, restore.Credentials) error
}

func (h replicaRotation) Inspect(ctx context.Context, id data.ReplicaBindingID) (data.ReplicaBinding, bool, error) {
	permit, err := h.state.ReadReplicaPermitByBinding(ctx, id)
	if err != nil {
		return data.ReplicaBinding{}, false, err
	}
	if permit.FenceState == "held" {
		return permit.Replica, true, nil
	}
	if !permit.Replica.Committed {
		return permit.Replica, false, nil
	}
	if !permit.SourceSettled || permit.DestinationOwnership != "local" {
		return data.ReplicaBinding{}, false, replication.ErrPermit
	}
	return permit.Replica, permit.FenceState == "held", nil
}
func (h replicaRotation) artifact(ctx context.Context, binding data.ReplicaBinding) (replication.Artifacts, string, error) {
	permit, err := h.state.ReadReplicaPermitByBinding(ctx, binding.BindingID)
	if err != nil || permit.Database.DatabaseID != binding.DatabaseID {
		return replication.Artifacts{}, "", replication.ErrPermit
	}
	if err := data.VerifyRunnerFile(binding.ConfigFile); err != nil {
		return replication.Artifacts{}, "", err
	}
	raw, err := os.ReadFile(binding.ConfigFile)
	if err != nil || string(raw) != binding.ConfigContent || strings.TrimPrefix(replication.ConfigHash(raw), "sha256:") != binding.ConfigSHA256 {
		return replication.Artifacts{}, "", replication.ErrPermit
	}
	cadence, err := replication.ConfigCadence(raw)
	if err != nil {
		return replication.Artifacts{}, "", err
	}
	database := permit.Database
	destination := binding.Destination
	projected := replication.Binding{IncarnationID: string(database.IncarnationID), DatabaseID: string(database.DatabaseID), BindingID: string(binding.BindingID), EpochID: string(binding.EpochID), DBPath: filepath.Join(string(database.Root), database.RelativeDirectory, string(database.Filename)), SocketPath: binding.SocketFile, Endpoint: destination.Endpoint, Bucket: destination.Bucket, Prefix: binding.RemotePrefix, Region: destination.Region, ForcePathStyle: destination.PathStyle, Cadence: cadence}
	if _, err := replication.ParseConfig(raw, projected); err != nil {
		return replication.Artifacts{}, "", err
	}
	unit, err := quadlet.RenderReplica(quadlet.ReplicaUnitOptions{Binding: projected, Config: raw, ConfigPath: binding.ConfigFile, CredentialPath: binding.CredentialFile, LifetimeLock: binding.LifetimeLockFile})
	if err != nil {
		return replication.Artifacts{}, "", err
	}
	return replication.Artifacts{Binding: projected, Config: raw, ConfigPath: binding.ConfigFile, LifetimeLock: binding.LifetimeLockFile, Service: unit.Bytes(), ServicePath: filepath.Join(h.home, ".config/systemd/user", unit.Name())}, strings.TrimPrefix(unit.Hash(), "sha256:"), nil
}
func (h replicaRotation) Prepare(ctx context.Context, r backupcredentials.Receipt, current data.ReplicaBinding) (data.ReplicaBinding, error) {
	if err := replication.ReplicaPermit(ctx, h.permits, replication.ReplicaPermitRequest{DatabaseID: string(current.DatabaseID), BindingID: string(current.BindingID), EpochID: string(current.EpochID), ConfigHash: "sha256:" + current.ConfigSHA256}); err != nil {
		return data.ReplicaBinding{}, err
	}
	_, oldHash, err := h.artifact(ctx, current)
	if err != nil || oldHash != current.UnitSHA256 {
		return data.ReplicaBinding{}, replication.ErrPermit
	}
	files := backupcredentials.Files{Root: filepath.Join(h.stateRoot, "credentials")}
	packet, err := files.Read(r.Scope.CredentialRef, r.Version)
	if err != nil {
		return data.ReplicaBinding{}, err
	}
	packet.Clear()
	credentialFile, err := files.Path(r.Scope.CredentialRef, r.Version)
	if err != nil {
		return data.ReplicaBinding{}, err
	}
	next := current
	next.CredentialFile = credentialFile
	next.CredentialVersion = r.Version
	_, hash, err := h.artifact(ctx, next)
	if err != nil {
		return data.ReplicaBinding{}, err
	}
	next.UnitSHA256 = hash
	return next, nil
}
func (h replicaRotation) Stop(ctx context.Context, b data.ReplicaBinding) error {
	name, err := replication.ServiceName(string(b.BindingID))
	if err != nil {
		return err
	}
	return h.services.Stop(ctx, name)
}
func (h replicaRotation) Stopped(ctx context.Context, b data.ReplicaBinding) (bool, error) {
	name, err := replication.ServiceName(string(b.BindingID))
	if err != nil {
		return false, err
	}
	return h.services.ReplicaStopped(ctx, name)
}
func (h replicaRotation) Acquire(ctx context.Context, b data.ReplicaBinding) (func() error, error) {
	if err := data.VerifyRunnerFile(b.LifetimeLockFile); err != nil {
		return nil, err
	}
	lock, err := replication.AcquireLifetimeLock(ctx, b.LifetimeLockFile)
	if err != nil {
		return nil, err
	}
	return lock.Release, nil
}
func (h replicaRotation) Commit(ctx context.Context, before, after data.ReplicaBinding) error {
	actual, fenced, err := h.Inspect(ctx, before.BindingID)
	if err != nil || fenced || actual != before && actual != after {
		return replication.ErrPermit
	}
	artifact, hash, err := h.artifact(ctx, after)
	if err != nil || hash != after.UnitSHA256 {
		return replication.ErrPermit
	}
	files := backupcredentials.Files{Root: filepath.Join(h.stateRoot, "credentials")}
	packet, err := files.Read(after.Destination.CredentialRef, after.CredentialVersion)
	if err != nil {
		return err
	}
	packet.Clear()
	publisher := replication.ArtifactPublisher{StateRoot: h.stateRoot, UnitRoot: filepath.Join(h.home, ".config/systemd/user")}
	if err := publisher.ReplaceService(ctx, artifact, before.UnitSHA256); err != nil {
		return err
	}
	return h.state.CommitReplicaBinding(ctx, after)
}
func (h replicaRotation) Reload(ctx context.Context) error { return h.units.DaemonReload(ctx) }
func (h replicaRotation) Permit(ctx context.Context, b data.ReplicaBinding) error {
	return replication.ReplicaPermit(ctx, h.permits, replication.ReplicaPermitRequest{DatabaseID: string(b.DatabaseID), BindingID: string(b.BindingID), EpochID: string(b.EpochID), ConfigHash: "sha256:" + b.ConfigSHA256})
}
func (h replicaRotation) Start(ctx context.Context, b data.ReplicaBinding) error {
	name, err := replication.ServiceName(string(b.BindingID))
	if err != nil {
		return err
	}
	return h.services.Start(ctx, name)
}
func (h replicaRotation) Running(ctx context.Context, b data.ReplicaBinding) (bool, error) {
	name, err := replication.ServiceName(string(b.BindingID))
	if err != nil {
		return false, err
	}
	unit, err := systemd.ParseUnit(name)
	if err != nil {
		return false, err
	}
	properties, err := h.units.Show(ctx, unit)
	if err != nil || properties.ActiveState != "active" || properties.SubState != "running" {
		return false, replication.ErrPermit
	}
	pending, err := h.units.JobPending(ctx, unit)
	return err == nil && !pending, err
}
func (h replicaRotation) VerifyRemote(ctx context.Context, r backupcredentials.Receipt, b data.ReplicaBinding) error {
	files := backupcredentials.Files{Root: filepath.Join(h.stateRoot, "credentials")}
	packet, err := files.Read(r.Scope.CredentialRef, r.Version)
	if err != nil {
		return err
	}
	defer packet.Clear()
	c := restore.Credentials{ReceivedAt: r.ReceivedAt}
	if r.ExpiresAt != nil {
		c.ExpiresAt = *r.ExpiresAt
	}
	for _, entry := range packet.Environment() {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case "AWS_ACCESS_KEY_ID":
			c.AccessKey = value
		case "AWS_SECRET_ACCESS_KEY":
			c.SecretKey = value
		case "AWS_SESSION_TOKEN":
			c.SessionToken = value
		}
	}
	d := b.Destination
	verify := h.verifyRemote
	if verify == nil {
		verify = restore.VerifyRemoteAccess
	}
	return verify(ctx, restore.Destination{Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, Prefix: strings.TrimSuffix(b.RemotePrefix, "/"), PathStyle: d.PathStyle}, c)
}

func (j rotationJournal) PendingRotation(ctx context.Context, id data.ReplicaBindingID) (data.CredentialRotation, error) {
	r, err := j.state.PendingCredentialRotation(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		err = backupcredentials.ErrRotationNotFound
	}
	return r, err
}
func (j rotationJournal) SupersedeRotation(ctx context.Context, old, next data.CredentialRotation) error {
	return j.state.SupersedeCredentialRotation(ctx, old, next)
}
