package replicapermits

import (
	"bytes"
	"context"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

// PermitStore is read-only. Production composition must use store.OpenReadOnly.
type PermitStore interface {
	ReadReplicaPermitByBinding(context.Context, data.ReplicaBindingID) (store.ReplicaPermit, error)
	ReadWriterPermits(context.Context, data.AppIncarnationID) ([]store.ReplicaPermit, error)
	ReadWriterSchema(context.Context, data.AppIncarnationID) (store.WriterSchema, error)
	ReadWriterStart(context.Context, data.AppIncarnationID) (store.WriterStartResolution, error)
	CandidateWriterSchema(context.Context, data.AppIncarnationID, policy.Desired) (store.WriterSchema, error)
}

// WriterEvidence observes the running operation and installed committed units afresh.
type WriterEvidence interface {
	OperationActive(context.Context, string) (bool, error)
	CommittedUnitsMatch(context.Context, []target.Unit) (bool, error)
}

type StorePermits struct {
	Writers WriterEvidence
	State   PermitStore
	Configs replication.ConfigFiles
}

func (r StorePermits) ReadReplicaPermit(ctx context.Context, binding string) (replication.PermitState, error) {
	if ctx.Err() != nil || r.State == nil || r.Configs == nil || !data.ValidID(binding) {
		return replication.PermitState{}, replication.ErrPermit
	}
	p, err := r.State.ReadReplicaPermitByBinding(ctx, data.ReplicaBindingID(binding))
	if err != nil || string(p.Replica.BindingID) != binding {
		return replication.PermitState{}, replication.ErrPermit
	}
	return r.project(ctx, p)
}

func (r StorePermits) ReadWriterPermits(ctx context.Context, incarnation string) ([]replication.PermitState, error) {
	if ctx.Err() != nil || r.State == nil || r.Configs == nil || !data.ValidID(incarnation) {
		return nil, replication.ErrPermit
	}
	id := data.AppIncarnationID(incarnation)
	permits, err := r.State.ReadWriterPermits(ctx, id)
	if err != nil || len(permits) == 0 {
		return nil, replication.ErrPermit
	}
	schema, err := r.writerSchema(ctx, id)
	if err != nil || len(schema.Bindings) != len(permits) {
		return nil, replication.ErrPermit
	}
	bindings := make(map[data.DatabaseID]data.DatabaseBinding, len(schema.Bindings))
	for _, binding := range schema.Bindings {
		if binding.IncarnationID != id {
			return nil, replication.ErrPermit
		}
		if _, duplicate := bindings[binding.DatabaseID]; duplicate {
			return nil, replication.ErrPermit
		}
		bindings[binding.DatabaseID] = binding
	}
	states := make([]replication.PermitState, 0, len(permits))
	for _, permit := range permits {
		binding, found := bindings[permit.Database.DatabaseID]
		if !found || binding != permit.Database {
			return nil, replication.ErrPermit
		}
		delete(bindings, binding.DatabaseID)
		state, err := r.project(ctx, permit)
		if err != nil || (state.Fence != replication.Unfenced && state.Fence != replication.FenceReleased || state.Ownership != replication.LocalOwner || !state.SourceSettled) {
			return nil, replication.ErrPermit
		}
		states = append(states, state)
	}
	if !data.WriterCompatibleWithAllocations(ctx, schema.Bindings, schema.Desired.SchemaCompatibility, schema.Definitions, schema.Allocations) || ctx.Err() != nil {
		return nil, replication.ErrPermit
	}
	for i := range states {
		states[i].WriterCompatible = true
	}
	return states, nil
}

func (r StorePermits) project(ctx context.Context, p store.ReplicaPermit) (replication.PermitState, error) {
	d, b := p.Database, p.Replica
	if ctx.Err() != nil || !b.Committed || b.DatabaseID != d.DatabaseID || b.BindingID != d.ReplicaBindingID || p.Fences == nil || p.ConfigPath != b.ConfigFile || p.SocketPath != b.SocketFile || p.CredentialPath != b.CredentialFile || p.LifetimeLockPath != b.LifetimeLockFile || (p.FenceState != "held" && p.FenceState != "released" && p.FenceState != "unfenced") {
		return replication.PermitState{}, replication.ErrPermit
	}
	for _, fence := range p.Fences {
		if fence.DatabaseID != d.DatabaseID || fence.IncarnationID != d.IncarnationID || (fence.State != data.FenceHeld && fence.State != data.FenceReleased) || (fence.State == data.FenceHeld && p.FenceState != "held") {
			return replication.PermitState{}, replication.ErrPermit
		}
	}
	raw := []byte(b.ConfigContent)
	hash := "sha256:" + b.ConfigSHA256
	if replication.ConfigHash(raw) != hash {
		return replication.PermitState{}, replication.ErrPermit
	}
	cadence, err := replication.ConfigCadence(raw)
	if err != nil {
		return replication.PermitState{}, replication.ErrPermit
	}
	binding := replication.Binding{IncarnationID: string(d.IncarnationID), DatabaseID: string(d.DatabaseID), BindingID: string(b.BindingID), EpochID: string(b.EpochID), DBPath: p.DBPath, SocketPath: p.SocketPath, Endpoint: b.Destination.Endpoint, Bucket: b.Destination.Bucket, Prefix: b.RemotePrefix, Region: b.Destination.Region, ForcePathStyle: b.Destination.PathStyle, Cadence: cadence}
	if _, err := replication.ParseConfig(raw, binding); err != nil {
		return replication.PermitState{}, replication.ErrPermit
	}
	disk, err := r.Configs.ReadConfig(ctx, p.ConfigPath)
	if err != nil || ctx.Err() != nil || !bytes.Equal(raw, disk) || replication.ConfigHash(disk) != hash {
		return replication.PermitState{}, replication.ErrPermit
	}
	return replication.PermitState{Binding: binding, Config: disk, ConfigHash: hash, ConfigPath: p.ConfigPath, CredentialPath: p.CredentialPath, LifetimeLock: p.LifetimeLockPath, Fence: replication.FenceState(p.FenceState), Ownership: replication.DestinationOwnership(p.DestinationOwnership), SourceSettled: p.SourceSettled}, nil
}

func (r StorePermits) writerSchema(ctx context.Context, id data.AppIncarnationID) (store.WriterSchema, error) {
	if r.Writers == nil {
		return store.WriterSchema{}, replication.ErrPermit
	}
	start, err := r.State.ReadWriterStart(ctx, id)
	if err != nil || ctx.Err() != nil {
		return store.WriterSchema{}, replication.ErrPermit
	}
	switch start.State {
	case store.WriterStartPending:
		if start.Intent == nil || start.Intent.IncarnationID != id || start.Intent.OperationID == "" {
			return store.WriterSchema{}, replication.ErrPermit
		}
		active, err := r.Writers.OperationActive(ctx, start.Intent.OperationID)
		if err != nil || !active || ctx.Err() != nil {
			return store.WriterSchema{}, replication.ErrPermit
		}
		return r.State.CandidateWriterSchema(ctx, id, start.Intent.Desired)
	case store.WriterStartNone:
		if start.Intent != nil {
			return store.WriterSchema{}, replication.ErrPermit
		}
		schema, err := r.State.ReadWriterSchema(ctx, id)
		if err != nil || schema.ReleaseID == "" || len(schema.Units) == 0 {
			return store.WriterSchema{}, replication.ErrPermit
		}
		matches, err := r.Writers.CommittedUnitsMatch(ctx, schema.Units)
		if err != nil || !matches || ctx.Err() != nil {
			return store.WriterSchema{}, replication.ErrPermit
		}
		return schema, nil
	default:
		return store.WriterSchema{}, replication.ErrPermit
	}
}
