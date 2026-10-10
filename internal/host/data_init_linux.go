//go:build linux

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/target"
)

const initializationEvidenceRoot = "/etc/ssh/brine/data-init"

func trustedInitJSON(ctx context.Context, kind, id string, out any) error {
	if !datainit.ValidID(id) {
		return datainit.ErrRefused
	}
	raw, err := trustedRead(ctx, filepath.Join(initializationEvidenceRoot, kind, strings.TrimPrefix(id, "sha256:")+".json"), 1<<20)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(sum[:]) != id {
		return datainit.ErrRefused
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		return datainit.ErrRefused
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return datainit.ErrRefused
	}
	return nil
}

func dataInitializationService(service Service, stateRoot string, operator bool) datainit.Service {
	return datainit.Service{PrepareRestorePoint: func(ctx context.Context, p datainit.Plan, op datainit.Operation) (datainit.VerifiedRestorePoint, error) {
		return prepareEmptyRestorePoint(ctx, service, stateRoot, p, op)
	}, Journal: service.Store, Requester: service.Requester, Authorize: func(ctx context.Context) error {
		pol, err := service.Policy.Load(ctx)
		if err != nil || (!operator && !pol.AllowAgentMigrations()) {
			return datainit.ErrRefused
		}
		return nil
	}, Lock: func(ctx context.Context) (func(), error) {
		lock, err := service.Store.AcquireHostLock(ctx)
		if err != nil {
			return nil, err
		}
		return func() { _ = lock.Release() }, nil
	}, Facts: func(ctx context.Context, r datainit.Request) (datainit.Facts, error) {
		return initializationFacts(ctx, service, stateRoot, r)
	}, Quiesce: func(ctx context.Context, p datainit.Plan) (func(), error) {
		// This minimal path admits only never-started, uncommitted bindings. Fresh
		// inventory and durable writer history must prove no registered writer; the
		// claimed durable fence also blocks both app and replica startup permits.
		f, err := initializationFacts(ctx, service, stateRoot, p.Request)
		if err != nil || f.Plan.Database != p.Database {
			return nil, datainit.ErrRefused
		}
		return func() {}, nil
	}}
}

func initializationFacts(ctx context.Context, s Service, stateRoot string, r datainit.Request) (datainit.Facts, error) {
	var f datainit.Facts
	if !datainit.ValidID(r.FirstReleasePlan) || !datainit.ValidID(r.Artifact) {
		return f, datainit.ErrRefused
	}
	pol, err := s.Policy.Load(ctx)
	if err != nil {
		return f, err
	}
	bounds, boundsValid := pol.InitializationBounds()
	if !boundsValid {
		return f, datainit.ErrRefused
	}
	candidate, err := s.Store.InitializationCandidate(ctx, r.App, r.FirstReleasePlan)
	if err != nil || len(candidate.Bindings) != 1 {
		return f, datainit.ErrRefused
	}
	releasePlan, desired, err := s.Store.LoadPlan(ctx, r.FirstReleasePlan)
	if err != nil || desired.PolicyHash != pol.Hash() {
		return f, datainit.ErrRefused
	}
	binding := candidate.Bindings[0]
	if binding.Name != desired.Databases[0].Name || binding.Root != desired.Databases[0].PersistentRoot || binding.MountPath != desired.Databases[0].MountPath || binding.Filename != desired.Databases[0].Filename || !slices.Contains(pol.PersistentRoots(), string(binding.Root)) {
		return f, datainit.ErrRefused
	}
	permit, err := s.Store.ReadReplicaPermit(ctx, binding.DatabaseID)
	if err != nil || permit.Replica.Committed {
		return f, datainit.ErrRefused
	}
	destination, ok := pol.BackupDestination(permit.Replica.Destination.Reference)
	if !ok || destination != permit.Replica.Destination {
		return f, datainit.ErrRefused
	}
	if err = trustedInitJSON(ctx, "schemas", r.Artifact, &f.Initializer); err != nil {
		return f, err
	}
	definition := f.Initializer.Definition
	if !slices.Contains(candidate.Definitions, definition) || !slices.Contains(desired.SchemaDefinitions, definition) {
		return f, datainit.ErrRefused
	}
	accepts := false
	for _, compatibility := range desired.SchemaCompatibility {
		if compatibility.Database == binding.Name && compatibility.Startup == "preserve" && slices.Contains(compatibility.Accepts, definition.Marker) {
			accepts = true
		}
	}
	if !accepts {
		return f, datainit.ErrRefused
	}
	snapshot, err := s.Inventory.Collect(ctx)
	if err != nil || snapshot.Generation.Status != target.KnownStatus || snapshot.Generation.Value == nil || snapshot.Apps.Status != target.KnownStatus || snapshot.Apps.Value == nil {
		return f, datainit.ErrRefused
	}
	for _, app := range *snapshot.Apps.Value {
		if app.Name == r.App {
			return f, datainit.ErrRefused
		}
	}
	identity, err := json.Marshal(snapshot.Identity)
	if err != nil {
		return f, err
	}
	sum := sha256.Sum256(identity)
	f.Plan = datainit.Plan{ReplicaEpoch: permit.Replica.EpochID, RemotePrefix: strings.TrimSuffix(permit.Replica.RemotePrefix, "/"), Bounds: bounds, PolicyHash: pol.Hash(), TargetHash: "sha256:" + hex.EncodeToString(sum[:]), Generation: *snapshot.Generation.Value, DesiredHash: releasePlan.DesiredHash, Database: binding, Definition: definition}
	if a, ok := candidate.Allocations[binding.DatabaseID]; ok {
		f.Allocation = &a
	}
	f.Observation = data.ObserveSchemaWithAllocation(ctx, binding, candidate.Definitions, f.Allocation)
	return f, nil
}

func prepareEmptyRestorePoint(ctx context.Context, s Service, stateRoot string, p datainit.Plan, op datainit.Operation) (datainit.VerifiedRestorePoint, error) {
	var point datainit.VerifiedRestorePoint
	pol, err := s.Policy.Load(ctx)
	if err != nil || pol.Hash() != p.PolicyHash {
		return point, datainit.ErrRefused
	}
	permit, err := s.Store.ReadReplicaPermit(ctx, p.Database.DatabaseID)
	if err != nil || permit.Database != p.Database || permit.FenceState != "held" || permit.Replica.Committed {
		return point, datainit.ErrRefused
	}
	owned := false
	for _, fence := range permit.Fences {
		if fence.State == data.FenceHeld && fence.OperationID == op.ID && fence.ID == op.Fence {
			owned = true
		}
	}
	if !owned {
		return point, datainit.ErrRefused
	}
	destination, ok := pol.BackupDestination(permit.Replica.Destination.Reference)
	if !ok || destination != permit.Replica.Destination {
		return point, datainit.ErrRefused
	}
	retention, ok := pol.BackupRetention(destination.Reference)
	if !ok || !retention.Admits(destination, time.Now().UTC()) {
		return point, datainit.ErrRefused
	}
	receipt, err := s.Store.CredentialReceipt(ctx, permit.Replica.BindingID, 0)
	if err != nil || receipt.Destination != destination.Reference || receipt.EpochID != permit.Replica.EpochID || receipt.CredentialRef != destination.CredentialRef || receipt.PolicyHash != p.PolicyHash {
		return point, datainit.ErrRefused
	}
	packet, err := (backupcredentials.Files{Root: filepath.Join(stateRoot, "credentials")}).Read(destination.CredentialRef, receipt.Version)
	if err != nil {
		return point, datainit.ErrRefused
	}
	defer packet.Clear()
	credentials := restore.Credentials{ReceivedAt: receipt.ReceivedAt}
	if receipt.ExpiresAt != nil {
		credentials.ExpiresAt = *receipt.ExpiresAt
	}
	environment := packet.Environment()
	defer clear(environment)
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "AWS_ACCESS_KEY_ID":
			credentials.AccessKey = value
		case "AWS_SECRET_ACCESS_KEY":
			credentials.SecretKey = value
		case "AWS_SESSION_TOKEN":
			credentials.SessionToken = value
		}
	}
	root := filepath.Join(stateRoot, "init-restores")
	if err = ensurePrivateChild(stateRoot, "init-restores"); err != nil {
		return point, err
	}
	binding := restore.Binding{ID: string(permit.Replica.BindingID), Epoch: string(permit.Replica.EpochID), CredentialRef: destination.CredentialRef, Destination: restore.Destination{Endpoint: destination.Endpoint, Region: destination.Region, Bucket: destination.Bucket, Prefix: strings.TrimSuffix(permit.Replica.RemotePrefix, "/"), PathStyle: destination.PathStyle}}
	source, uploadedAt, err := restore.CreateEmptySnapshot(ctx, root, binding, credentials, p.RestorePointID, time.Minute, false)
	if err != nil {
		return point, datainit.ErrRecovery
	}
	engine := restore.Engine{Root: root, MaxSnapshotBytes: 1 << 20, Observer: restore.EmptySchemaObserver{}, Bindings: restore.BindingReaderFunc(func(context.Context, string, string) (restore.Binding, error) { return binding, nil }), Credentials: restore.CredentialReaderFunc(func(context.Context, string) (restore.Credentials, error) { return credentials, nil })}
	restored, err := engine.Test(ctx, restore.Request{OperationID: op.ID + "-empty-verify", Source: restore.RestoreSource{Kind: restore.SQLiteSnapshot, Snapshot: &source}, Budget: time.Minute, ExpectedSchema: restore.SchemaObservation{State: restore.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}})
	if err != nil {
		return point, datainit.ErrRecovery
	}
	point = datainit.VerifiedRestorePoint{PointID: p.RestorePointID, DatabaseID: p.Database.DatabaseID, UploadedAt: uploadedAt, Receipt: restored, RetainUntil: uploadedAt.Add(time.Duration(p.Bounds.RecoveryWindowSeconds+p.Bounds.MaxBackupAgeSeconds+p.Bounds.MaxRestoreTestAgeSeconds) * time.Second)}
	return point, nil
}
