//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
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
	activate  func(context.Context, backupcredentials.Receipt) (backupcredentials.Receipt, error)
}

func (o credentialOperations) Plan(ctx context.Context, app, database string, expiry *time.Time) (backupcredentials.Plan, error) {
	return backupCredentialService(o.service, o.stateRoot, database).Plan(ctx, app, expiry)
}
func (o credentialOperations) Set(ctx context.Context, app, database, planID string, packet backupcredentials.Packet) (backupcredentials.Receipt, error) {
	service := backupCredentialService(o.service, o.stateRoot, database)
	service.Activate = o.activate
	receipt, err := service.Set(ctx, app, planID, packet)
	if err != nil {
		return backupcredentials.Receipt{}, err
	}
	health := receipt.Health(time.Now().UTC(), time.Minute)
	receipt.CredentialHealth = &health
	return receipt, nil
}
