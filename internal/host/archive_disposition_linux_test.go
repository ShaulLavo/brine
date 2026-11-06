//go:build linux

package host

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/cdnoperator"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

func TestArchiveAbandonAllowsChangedContextButNeverChangedOwnership(t *testing.T) {
	planned := archive.Plan{TargetHash: "same", PolicyHash: "old", Generation: 1, Candidate: archive.Candidate{App: "hello", Incarnation: "original", ReleaseID: "release", Databases: []archive.Database{archive.SQLiteDatabaseVariant(archive.SQLiteDatabase{Binding: data.SQLiteDatabaseBinding{DatabaseID: "database"}, Retention: []data.RetentionEvidence{{VerificationID: "old"}}})}}}
	observed := planned
	observed.PolicyHash = "new"
	observed.Generation = 2
	observed.Candidate.Databases = []archive.Database{archive.SQLiteDatabaseVariant(archive.SQLiteDatabase{Binding: sqliteArchiveDatabase(t, planned.Candidate.Databases[0]).Binding, Retention: nil})}
	if !archiveDispositionFactsMatch(planned, observed, true) {
		t.Fatal("unrelated changes stranded abandonment")
	}
	if archiveDispositionFactsMatch(planned, observed, false) {
		t.Fatal("changed context authorized retirement")
	}
	for _, change := range []func(*archive.Plan){func(p *archive.Plan) { p.Candidate.Incarnation = "recreated" }, func(p *archive.Plan) { p.Candidate.ReleaseID = "new-release" }, func(p *archive.Plan) { p.TargetHash = "other-host" }, func(p *archive.Plan) { p.Candidate.Databases = nil }} {
		changed := observed
		change(&changed)
		if archiveDispositionFactsMatch(planned, changed, true) {
			t.Fatal("changed ownership authorized fence release")
		}
	}
	if len(planned.Candidate.Databases[0].Retention()) != 1 || observed.Candidate.Databases[0].Retention() != nil {
		t.Fatal("comparison mutated borrowed evidence")
	}
}

func TestArchiveAbandonSchemaVerificationNeedsNoManifestButRefusesUnknown(t *testing.T) {
	previous := data.SQLiteSchemaObservation{State: data.VerifiedSchema, Marker: "old", CatalogSHA256: "old"}
	fresh := data.SQLiteSchemaObservation{State: data.VerifiedSchema, Marker: "declared", CatalogSHA256: "verified"}
	if !archiveDispositionSchemaMatches(fresh, data.SQLiteSchemaObservation{}, true) || archiveDispositionSchemaMatches(fresh, previous, false) {
		t.Fatal("manifest-free exit or retirement binding is wrong")
	}
	for _, unknown := range []data.SQLiteSchemaObservation{
		{State: data.Unknown},
		{State: data.AllocatedEmpty},
		{State: data.VerifiedSchema, UnknownReason: "invalid_marker"},
		{State: data.VerifiedEmpty, UnknownReason: "unverified_path"},
	} {
		if archiveDispositionSchemaMatches(unknown, data.SQLiteSchemaObservation{}, true) {
			t.Fatal("unknown schema released fences", unknown)
		}
	}
	if !archiveDispositionSchemaMatches(previous, previous, false) {
		t.Fatal("exact coverage refused")
	}
}

func TestArchiveCDNHostRefusesBeforeCleanupAndRecovery(t *testing.T) {
	for _, mode := range []string{"reservation", "enabled"} {
		t.Run(mode, func(t *testing.T) { testArchiveCDNHostRefusal(t, mode) })
	}
}

func testArchiveCDNHostRefusal(t *testing.T, mode string) {
	rig, fixture, coordinator, request := cdnHostFixture(t)
	ctx := t.Context()
	p, err := coordinator.Plan(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "reservation" {
		if _, _, err := rig.store.AdmitCDN(ctx, p.ID, "operator", "archive-pending"); err != nil {
			t.Fatal(err)
		}
	} else {
		progress, err := coordinator.Apply(ctx, cdnoperator.ApplyRequest{PlanID: p.ID, IdempotencyKey: "archive-enable"})
		assertCDNFinished(t, progress, err)
	}
	before, headErr := rig.store.CDNHead(ctx, "hello")
	writes := fixture.NativeWrites(request.Profile)
	h := archiveHost{service: rig.service}
	planned := archive.Plan{Candidate: archive.Candidate{App: "hello"}}
	checks := []func(context.Context) error{
		func(ctx context.Context) error { return h.RecheckArchive(ctx, planned, archive.Operation{}, false) },
		func(ctx context.Context) error { return h.RetireArchiveRuntime(ctx, planned) },
		func(ctx context.Context) error {
			return h.CheckArchiveRecovery(ctx, planned, archive.Operation{}, archive.Manifest{})
		},
		func(ctx context.Context) error { return h.ReconcileArchiveRuntime(ctx, planned) },
	}
	for i, check := range checks {
		if err := check(ctx); !errors.Is(err, store.ErrConflict) {
			t.Fatal("CDN conflict reached runtime checks or effects", i, err)
		}
	}
	after, err := rig.store.CDNHead(ctx, "hello")
	if !errors.Is(err, headErr) || !reflect.DeepEqual(before, after) || fixture.NativeWrites(request.Profile) != writes {
		t.Fatal("refusal changed CDN work or configuration", err)
	}
}

func TestNativeArchiveCallerRefusesWithoutPreparedAuthority(t *testing.T) {
	f := initializedMaterialFixture(t)
	ctx := t.Context()
	reserved, err := f.service.Store.ReadReservedEngine(ctx, f.plan.Managed.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	head, runtime, err := f.service.Store.ReadEngineHead(ctx, reserved.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	candidate := archive.Candidate{App: f.plan.App, Incarnation: reserved.Database.IncarnationID, Databases: []archive.Database{archive.NativeDatabaseVariant(archive.NativeDatabase{Binding: reserved.Database, Head: head, State: runtime.State})}}
	if !nativeArchiveCandidate(candidate) {
		t.Fatal("genuine native allocation did not select native caller")
	}
	if err := f.service.Store.CheckArchiveCDN(ctx, candidate.App); err != nil {
		t.Fatal("absent CDN blocked native control", err)
	}
	h := archiveHost{service: f.service, stateRoot: f.stateRoot, home: f.home}
	if err := h.RetireArchiveRuntime(ctx, archive.Plan{Candidate: candidate}); err == nil {
		t.Fatal("native runtime retired without prepared authority")
	}
	caller := nativeRetireOperations{state: f.service.Store}
	if _, err := caller.PlanNativeArchive(ctx, dispatch.NativeRetirePlanArgs{App: candidate.App}); err == nil || result.Classify(err).Code() != result.PolicyRefused {
		t.Fatal("native task planner admitted unprepared allocation", err)
	}
	retained, err := f.service.Store.ReadReservedEngine(ctx, reserved.Database.BindingID)
	if err != nil || !reflect.DeepEqual(retained, reserved) {
		t.Fatal("native refusal changed protected allocation", err)
	}
}
