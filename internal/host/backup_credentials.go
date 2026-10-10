//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

// Credential journal contains references only. Authentication is supplied by
// the dispatcher composition, never taken from packet or plan contents.
type credentialJournal struct {
	state     *store.Store
	requester string
}

func credentialRecordID(kind, id string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + id))
	return hex.EncodeToString(sum[:16])
}
func credentialRecord(p backupcredentials.Plan, kind string) store.CredentialRecord {
	return store.CredentialRecord{ID: credentialRecordID(kind, p.ID), Kind: kind, App: p.Scope.App, CredentialRef: p.Scope.CredentialRef, Requester: p.Requester, PlanID: p.ID, PlanHash: p.ID, TargetHash: p.Scope.TargetHash, PolicyHash: p.Scope.PolicyHash, BindingID: data.ReplicaBindingID(p.Scope.Binding), Destination: data.BackupDestinationRef(p.Scope.Destination), EpochID: data.ReplicaEpochID(p.Scope.Epoch), Version: p.Version, ExpiresAt: p.ExpiresAt}
}
func (j credentialJournal) RecordPlan(ctx context.Context, p backupcredentials.Plan) error {
	if !p.Valid() || p.Requester != j.requester {
		return backupcredentials.ErrInvalid
	}
	return j.save(ctx, credentialRecord(p, "plan"))
}
func (j credentialJournal) save(ctx context.Context, r store.CredentialRecord) error {
	if old, err := j.state.LoadCredentialRecord(ctx, r.ID); err == nil {
		a, _ := json.Marshal(old)
		b, _ := json.Marshal(r)
		if string(a) == string(b) {
			return nil
		}
		return store.ErrConflict
	}
	return j.state.SaveCredentialRecord(ctx, r)
}
func (j credentialJournal) LoadPlan(ctx context.Context, id string) (backupcredentials.Plan, error) {
	r, err := j.state.LoadCredentialRecord(ctx, credentialRecordID("plan", id))
	if err != nil {
		return backupcredentials.Plan{}, err
	}
	p := backupcredentials.Plan{Requester: r.Requester, Kind: backupcredentials.Kind, ID: r.PlanID, Version: r.Version, ExpiresAt: r.ExpiresAt, Scope: backupcredentials.Scope{App: r.App, CredentialRef: r.CredentialRef, TargetHash: r.TargetHash, PolicyHash: r.PolicyHash, Binding: string(r.BindingID), Epoch: string(r.EpochID), Destination: string(r.Destination)}}
	if !p.Valid() || p.ID != id || p.Requester != j.requester || r.Kind != "plan" {
		return backupcredentials.Plan{}, backupcredentials.ErrInvalid
	}
	return p, nil
}
func (j credentialJournal) RecordReceipt(ctx context.Context, r backupcredentials.Receipt) error {
	if !r.Valid() || r.Requester != j.requester {
		return backupcredentials.ErrInvalid
	}
	record := credentialRecord(backupcredentials.Plan{Requester: r.Requester, Kind: backupcredentials.Kind, ID: r.PlanID, Scope: r.Scope, Version: r.Version, ExpiresAt: r.ExpiresAt}, "receipt")
	record.ReceivedAt = r.ReceivedAt
	return j.save(ctx, record)
}
func backupCredentialService(service Service, stateRoot, database string) backupcredentials.Service {
	return backupcredentials.Service{Requester: service.Requester, Files: backupcredentials.Files{Root: filepath.Join(stateRoot, "credentials")}, Journal: credentialJournal{state: service.Store, requester: service.Requester}, Lock: func(ctx context.Context) (func(), error) {
		lock, err := service.Store.AcquireHostLock(ctx)
		if err != nil {
			return nil, err
		}
		if err = ensurePrivateChild(stateRoot, "credentials"); err != nil {
			_ = lock.Release()
			return nil, err
		}
		return func() { _ = lock.Release() }, nil
	}, Scope: func(ctx context.Context, app string) (backupcredentials.Scope, error) {
		pol, err := service.Policy.Load(ctx)
		if err != nil {
			return backupcredentials.Scope{}, err
		}
		scopes, err := service.Store.ReadCredentialScopes(ctx, app)
		if err != nil {
			return backupcredentials.Scope{}, backupcredentials.ErrInvalid
		}
		s, err := selectCredentialScope(scopes, database)
		if err != nil {
			return backupcredentials.Scope{}, err
		}
		if s.PolicyHash != pol.Hash() {
			return backupcredentials.Scope{}, backupcredentials.ErrAdmissionRefresh
		}
		destination, ok := pol.BackupDestination(s.Replica.Destination.Reference)
		if !ok || destination != s.Replica.Destination {
			return backupcredentials.Scope{}, backupcredentials.ErrStale
		}
		snapshot, err := service.Inventory.Collect(ctx)
		if err != nil {
			return backupcredentials.Scope{}, err
		}
		raw, err := json.Marshal(snapshot.Identity)
		if err != nil {
			return backupcredentials.Scope{}, err
		}
		sum := sha256.Sum256(raw)
		return backupcredentials.Scope{TargetHash: "sha256:" + hex.EncodeToString(sum[:]), App: app, CredentialRef: destination.CredentialRef, Destination: string(destination.Reference), Binding: string(s.Replica.BindingID), Epoch: string(s.Replica.EpochID), PolicyHash: pol.Hash()}, nil
	}}
}

func selectCredentialScope(scopes []store.CredentialScope, database string) (store.CredentialScope, error) {
	if database == "" {
		if len(scopes) != 1 {
			return store.CredentialScope{}, backupcredentials.ErrInvalid
		}
		return scopes[0], nil
	}
	var selected store.CredentialScope
	found := false
	for _, scope := range scopes {
		if string(scope.Database.Name) == database {
			if found {
				return store.CredentialScope{}, backupcredentials.ErrInvalid
			}
			selected, found = scope, true
		}
	}
	if !found {
		return store.CredentialScope{}, backupcredentials.ErrInvalid
	}
	return selected, nil
}

type credentialOperations struct {
	service   Service
	stateRoot string
	jobs      jobs.Service
}

func (o credentialOperations) Plan(ctx context.Context, app, database string, expiry *time.Time) (backupcredentials.Plan, error) {
	return backupCredentialService(o.service, o.stateRoot, database).Plan(ctx, app, expiry)
}
func (o credentialOperations) Set(ctx context.Context, app, database, planID string, packet backupcredentials.Packet) (jobs.Accepted, error) {
	service := backupCredentialService(o.service, o.stateRoot, database)
	service.ResumeStored = true
	receipt, err := service.Set(ctx, app, planID, packet)
	if err != nil {
		return jobs.Accepted{}, err
	}
	key, err := data.NewID()
	if err != nil {
		return jobs.Accepted{}, err
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: ops.CredentialActivation, App: receipt.Scope.App, SecretRef: receipt.PlanID}, key)
}

func credentialTaskHandler(service Service, dir string, rotator backupcredentials.Rotator) jobs.TaskHandler {
	return func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		lock, err := service.Store.AcquireHostLock(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = lock.Release() }()
		plan, err := (credentialJournal{state: service.Store, requester: op.Requester}).LoadPlan(ctx, op.SecretRef)
		if err != nil || plan.Scope.App != op.App {
			return nil, result.New(result.PolicyRefused, nil)
		}
		pol, err := service.Policy.Load(ctx)
		if err != nil {
			return nil, result.New(result.PolicyRefused, nil)
		}
		if pol.Hash() != plan.Scope.PolicyHash {
			return nil, result.New(result.BackupAdmissionRefreshRequired, nil)
		}
		receipt, err := (backupcredentials.Files{Root: filepath.Join(dir, "credentials")}).Receipt(plan.Scope.CredentialRef, plan.Version)
		if err != nil || receipt.PlanID != plan.ID || receipt.Requester != op.Requester {
			return nil, result.New(result.PolicyRefused, nil)
		}
		activated, err := rotator.Activate(ctx, receipt)
		if err != nil {
			if errors.Is(err, backupcredentials.ErrExpired) || errors.Is(err, backupcredentials.ErrStale) || errors.Is(err, backupcredentials.ErrInvalid) {
				return nil, result.New(result.PolicyRefused, nil)
			}
			return nil, result.New(result.RecoveryRequired, err)
		}
		health := activated.Health(time.Now().UTC(), time.Minute)
		activated.CredentialHealth = &health
		return json.Marshal(activated)
	}
}
