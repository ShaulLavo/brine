package backupcredentials

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type archiveCredentialJournal struct {
	plans    map[string]ArchivePlan
	receipts []ArchiveReceipt
	fail     bool
}

func (j *archiveCredentialJournal) RecordArchiveCredentialPlan(_ context.Context, p ArchivePlan) error {
	j.plans[p.ID] = p
	return nil
}
func (j *archiveCredentialJournal) LoadArchiveCredentialPlan(_ context.Context, id string) (ArchivePlan, error) {
	p, ok := j.plans[id]
	if !ok {
		return p, ErrInvalid
	}
	return p, nil
}
func (j *archiveCredentialJournal) RecordArchiveCredentialReceipt(_ context.Context, r ArchiveReceipt) error {
	if j.fail {
		return ErrStorage
	}
	j.receipts = append(j.receipts, r)
	return nil
}
func archiveCredentialFixture(t *testing.T) (ArchiveService, *archiveCredentialJournal, *ArchiveScope, *time.Time) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	active := fixtureScope()
	scope := ArchiveScope{ArchiveID: strings.Repeat("d", 32), Binding: active.Binding, Epoch: active.Epoch, CredentialRef: active.CredentialRef, Destination: active.Destination, TargetHash: active.TargetHash, PolicyHash: active.PolicyHash, RetainUntil: now.Add(30 * 24 * time.Hour)}
	journal := &archiveCredentialJournal{plans: map[string]ArchivePlan{}}
	s := ArchiveService{Requester: "fixture-requester", Journal: journal, Files: Files{Root: root}, Scope: func(context.Context, string, string, string) (ArchiveScope, error) { return scope, nil }, Lock: func(context.Context) (func() error, error) { return func() error { return nil }, nil }, Now: func() time.Time { return now }}
	return s, journal, &scope, &now
}
func archiveCredentialPacket(t *testing.T) Packet {
	t.Helper()
	packet, err := ReadPacket(strings.NewReader(`{"access_key_id":"PLANTED_ACCESS","secret_access_key":"PLANTED_SECRET","session_token":"PLANTED_SESSION"}`))
	if err != nil {
		t.Fatal(err)
	}
	return packet
}
func TestArchiveCredentialRenewalIsScopedAndNeverActivates(t *testing.T) {
	s, j, scope, now := archiveCredentialFixture(t)
	ctx := context.Background()
	p, err := s.Plan(ctx, scope.ArchiveID, scope.Binding, scope.Epoch, nil)
	if err != nil {
		t.Fatal(err)
	}
	packet := archiveCredentialPacket(t)
	defer packet.Clear()
	r, err := s.Deliver(ctx, p.ID, packet)
	if err != nil || !r.Valid() || len(j.receipts) != 1 {
		t.Fatal(r, err)
	}
	if !r.Plan.Scope.RetainUntil.Equal(scope.RetainUntil) {
		t.Fatal("deadline changed")
	}
	if _, err := s.Files.Receipt(scope.fileRef(), r.Plan.Version); err == nil {
		t.Fatal("archive receipt admitted as active replica credential")
	}
	public, err := json.Marshal(r)
	if err != nil || strings.Contains(string(public), "PLANTED") || strings.Contains(string(public), "activated") {
		t.Fatal("receipt exposed secrets or activation state", err)
	}
	active := fixtureScope()
	active.Fenced = true
	if active.valid() {
		t.Fatal("ordinary fenced scope became valid")
	}
	other := *scope
	other.Epoch = strings.Repeat("e", 32)
	if other.fileRef() == scope.fileRef() {
		t.Fatal("epochs share an archive credential namespace")
	}
	other = *scope
	other.ArchiveID = strings.Repeat("f", 32)
	if other.fileRef() == scope.fileRef() {
		t.Fatal("archives share a credential namespace")
	}
	*now = scope.RetainUntil
	if err := r.Admit(*now, 0); !errors.Is(err, ErrExpired) {
		t.Fatal("deadline admitted", err)
	}
	if _, err := s.Plan(ctx, scope.ArchiveID, scope.Binding, scope.Epoch, nil); !errors.Is(err, ErrExpired) {
		t.Fatal("expired archive renewed", err)
	}
}
func TestArchiveCredentialDeliveryAdoptsOnlyExactSealedMetadata(t *testing.T) {
	s, j, scope, _ := archiveCredentialFixture(t)
	ctx := context.Background()
	p, err := s.Plan(ctx, scope.ArchiveID, scope.Binding, scope.Epoch, nil)
	if err != nil {
		t.Fatal(err)
	}
	packet := archiveCredentialPacket(t)
	defer packet.Clear()
	j.fail = true
	if _, err := s.Deliver(ctx, p.ID, packet); !errors.Is(err, ErrStorage) {
		t.Fatal("receipt interruption not injected", err)
	}
	j.fail = false
	r, err := s.Deliver(ctx, p.ID, packet)
	if err != nil || r.Plan.ID != p.ID || len(j.receipts) != 1 {
		t.Fatal("sealed delivery not adopted", r, err)
	}
	// The same sealed environment cannot be relabeled for another policy scope.
	scope.PolicyHash = "sha256:" + strings.Repeat("f", 64)
	if _, err := s.Deliver(ctx, p.ID, packet); !errors.Is(err, ErrStale) {
		t.Fatal("changed admission adopted", err)
	}
}
func TestArchiveCredentialPartialVersionRemainsReservedAndUnusable(t *testing.T) {
	s, _, scope, now := archiveCredentialFixture(t)
	ctx := context.Background()
	p, err := s.Plan(ctx, scope.ArchiveID, scope.Binding, scope.Epoch, nil)
	if err != nil {
		t.Fatal(err)
	}
	packet := archiveCredentialPacket(t)
	defer packet.Clear()
	r := ArchiveReceipt{Plan: p, File: fileName(scope.fileRef(), p.Version, "env"), ReceivedAt: *now}
	env := packet.environment()
	defer clear(env)
	if err := s.Files.installArchive(r, env); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.Files.Root, fileName(scope.fileRef(), p.Version, "json"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deliver(ctx, p.ID, packet); !errors.Is(err, ErrStale) {
		t.Fatal("partial delivery overwritten or adopted", err)
	}
	if _, err := s.Files.ArchiveReceipt(scope.fileRef(), p.Version); err == nil {
		t.Fatal("partial version usable")
	}
	next, err := s.Plan(ctx, scope.ArchiveID, scope.Binding, scope.Epoch, nil)
	if err != nil || next.Version != p.Version+1 {
		t.Fatal("partial version not reserved", next, err)
	}
}

func TestArchiveCredentialDeliveryCannotSelectAnotherArchive(t *testing.T) {
	service, _, scope, _ := archiveCredentialFixture(t)
	plan, err := service.Plan(context.Background(), scope.ArchiveID, scope.Binding, scope.Epoch, nil)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := ReadPacket(strings.NewReader(`{"access_key_id":"PLANTED_ACCESS","secret_access_key":"PLANTED_SECRET"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Clear()
	if _, err = service.DeliverScoped(context.Background(), strings.Repeat("f", 32), scope.Binding, scope.Epoch, plan.ID, packet); !errors.Is(err, ErrStale) {
		t.Fatal("cross-archive credential plan applied", err)
	}
	if version, err := service.Files.Next(scope.FileReference()); err != nil || version != plan.Version {
		t.Fatal("refusal wrote private version", version, err)
	}
}
