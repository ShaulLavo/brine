//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

func TestPublicationPreservationRequiresExactImmutablePointSeal(t *testing.T) {
	state, _, point := nativeCompletedPointFixture(t)
	ctx := context.Background()
	recorded, err := state.ReadNativePreparedPoint(ctx, point.PointID)
	if err != nil {
		t.Fatal(err)
	}
	observed := PublicationSourcePreservationObservation{OriginalSealSHA256: recorded.SourceSealSHA256, EngineAbsent: true, ApplicationAbsent: true, IngressClosed: true, PrivateReadersAbsent: true, ObservedAt: time.Now().UTC()}
	if err := checkPublicationPreservedPoint(recorded, observed); err != nil {
		t.Fatal("recorded point seal refused preservation boundary", err)
	}
	observed.OriginalSealSHA256 = strings.Repeat("f", 64)
	if err := checkPublicationPreservedPoint(recorded, observed); !errors.Is(err, ErrConflict) {
		t.Fatal("different producer digest passed preservation boundary", err)
	}
	after, err := state.ReadNativePreparedPoint(ctx, point.PointID)
	beforeRaw, _ := json.Marshal(recorded)
	afterRaw, _ := json.Marshal(after)
	if err != nil || string(beforeRaw) != string(afterRaw) {
		t.Fatal("observation mismatch changed the immutable prepared point", err)
	}
}

func TestPreparedEmptyCompletionPreservesStoredOwnerSourceSeal(t *testing.T) {
	state, preparation, lifetime, _ := nativePreparationFixture(t)
	ctx := context.Background()
	stopped, err := state.RecordNativeStoppedPreparation(ctx, preparation, lifetime, nativeAbsentStopFixture())
	if err != nil {
		t.Fatal(err)
	}
	permit, err := state.ReadEnginePermit(ctx, preparation.intent.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	signing := permit.Runtime.Secrets.SigningPrivate
	config := strings.Repeat("c", 64)
	source, err := nativecapture.UnpublishedSource(nativecapture.EmptyRecord{
		Format:       nativecapture.Format,
		Scope:        nativecapture.EmptyScope{BindingID: string(permit.Runtime.BindingID), EpochID: string(permit.Runtime.EpochID)},
		GenerationID: strings.Repeat("e", 32), ConfigurationSHA256: config,
		PublisherReference: permit.Runtime.Secrets.Publisher.Reference,
		SigningRecovery:    nativecapture.SigningRecovery{Kind: "external_immutable", Reference: signing.Reference, Version: signing.Version, SHA256: signing.SHA256},
		Members:            []nativecapture.CheckpointMember{{Path: "config.toml", Bytes: 10, SHA256: config}},
	}, nativecapture.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	owner := retainedPreparedOwnerFixture(t, stopped, permit, source)
	if err := state.RecordNativeInitialOwner(ctx, stopped, owner); err != nil {
		t.Fatal(err)
	}
	stored, err := state.ReadNativeInitialOwner(ctx, stopped)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := state.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	point := NativePreparedPointView{SourceSHA256: source.SHA256(), SourceSealSHA256: stored.SourceSealSHA256}
	if err := completeNativePreparedEmptySource(ctx, tx, stopped, source, &point); err != nil {
		t.Fatal("protected retained owner refused empty-point completion boundary", err)
	}
	ownerRaw, _ := json.Marshal(stored)
	if point.SourceSealSHA256 != stored.SourceSealSHA256 || point.SourceWitnessSHA256 != nativePointHash(ownerRaw) || point.GenerationID != stored.GenerationID || point.CheckpointSHA256 != source.SHA256() {
		t.Fatal("completion lost the original owner and its canonical witness")
	}
	changed := point
	changed.SourceSealSHA256 = strings.Repeat("d", 64)
	if err := completeNativePreparedEmptySource(ctx, tx, stopped, source, &changed); !errors.Is(err, ErrConflict) {
		t.Fatal("recomputed replacement seal admitted beside immutable owner", err)
	}
	var afterRaw []byte
	if err := tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_initial_owners WHERE point_id=?", stopped.PointID()).Scan(&afterRaw); err != nil || string(afterRaw) != string(ownerRaw) {
		t.Fatal("completion rewrote the retained owner", err)
	}
}

func retainedPreparedOwnerFixture(t *testing.T, stopped *NativeStoppedPreparation, permit EnginePermit, source nativecapture.Source) NativeInitialOwnerObservation {
	t.Helper()
	endpoint, err := spacetimedb.NewPrivateServerEndpoint("127.0.0.1", 23991)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	publisher := strings.Repeat("f", 64)
	proof, err := json.Marshal(map[string]any{"purpose": "native_supplied_key_publisher_verification", "endpoint_sha256": endpoint.SHA256(), "method": "GET", "path": "/v1/identity/" + publisher + "/verify", "status": 204, "publisher": publisher, "credential_sha256": permit.Runtime.Secrets.Publisher.SHA256, "verified_at": now})
	if err != nil {
		t.Fatal(err)
	}
	empty, _ := source.Empty()
	return NativeInitialOwnerObservation{Format: "brine-native-initial-owner-provenance-v1", PointID: stopped.PointID(), OperationID: stopped.OperationID(), BindingID: permit.Runtime.BindingID, EpochID: permit.Runtime.EpochID, GenerationID: empty.GenerationID, SourceSHA256: source.SHA256(), SourceSealSHA256: strings.Repeat("2", 64), AllocationID: permit.Reserved.Database.DatabaseID, Runtime: permit.Runtime.Clone(), StageDevice: permit.Allocation.DirectoryDevice, StageInode: permit.Allocation.DirectoryInode, HelperPID: 20, HelperStartTicks: 1, HelperCgroup: "/fixture", ChildPID: 21, ChildStartTicks: 2, ChildBound: true, ChildStopped: true, HelperDisposed: true, Publisher: publisher, CredentialSHA256: permit.Runtime.Secrets.Publisher.SHA256, PublisherProofSHA256: nativePointHash(proof), PublisherProof: proof, StartedAt: now, FinishedAt: now}
}
