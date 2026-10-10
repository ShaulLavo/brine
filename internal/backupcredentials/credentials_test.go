package backupcredentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixtureScope() Scope {
	return Scope{TargetHash: "sha256:" + strings.Repeat("e", 64), App: "hello", CredentialRef: "primary", Destination: "primary", Binding: strings.Repeat("a", 32), Epoch: strings.Repeat("b", 32), PolicyHash: "sha256:" + strings.Repeat("c", 64)}
}
func TestPrivatePacket(t *testing.T) {
	packet := `{"access_key_id":"PLANTED_ACCESS","secret_access_key":"PLANTED_SECRET","session_token":"PLANTED_SESSION","expires_at":"2026-10-10T14:00:00Z"}`
	p, err := ReadPacket(strings.NewReader(packet))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	for _, out := range []string{string(b), fmt.Sprintf("%v %#v", p, p)} {
		if strings.Contains(out, "PLANTED") {
			t.Fatal("packet leaked")
		}
	}
	for _, bad := range []string{`{}`, `{"access_key_id":"a","access_key_id":"b","secret_access_key":"c"}`, `{"access_key_id":"a\nX=1","secret_access_key":"c"}`, `{"access_key_id":"a","secret_access_key":"c","unknown":"d"}`, strings.Repeat("x", PacketLimit+1)} {
		if _, err := ReadPacket(strings.NewReader(bad)); err == nil {
			t.Fatal("accepted invalid packet")
		}
	}
}
func TestVersionedDelivery(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	scope := fixtureScope()
	service := Service{Requester: "fixture-requester", Journal: &memoryJournal{plans: map[string]Plan{}}, Files: Files{Root: root}, Scope: func(context.Context, string) (Scope, error) { return scope, nil }, Now: func() time.Time { return now }}
	p, err := ReadPacket(strings.NewReader(`{"access_key_id":"PLANTED_ACCESS","secret_access_key":"PLANTED_SECRET","session_token":"PLANTED_SESSION"}`))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.Plan(context.Background(), "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := service.Deliver(context.Background(), plan, p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != 1 || r.Activated || r.ReceivedAt != now {
		t.Fatalf("receipt %+v", r)
	}
	b, err := os.ReadFile(filepath.Join(root, r.File))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "AWS_SESSION_TOKEN=PLANTED_SESSION\n") {
		t.Fatal("missing session token")
	}
	info, _ := os.Stat(filepath.Join(root, r.File))
	if info.Mode().Perm() != 0600 {
		t.Fatal("not private")
	}
	public, _ := json.Marshal(r)
	if strings.Contains(string(public), "PLANTED") {
		t.Fatal("receipt leaked")
	}
	if _, err := service.Deliver(context.Background(), plan, p); !errors.Is(err, ErrStale) {
		t.Fatalf("replayed delivery %v", err)
	}
	next, err := service.Plan(context.Background(), "hello", nil)
	if err != nil || next.Version != 2 {
		t.Fatal(next, err)
	}
	scope.Epoch = strings.Repeat("d", 32)
	if _, err := service.Deliver(context.Background(), next, p); !errors.Is(err, ErrStale) {
		t.Fatal("accepted stale epoch", err)
	}
}
func TestExpiryAndAge(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)
	r := Receipt{ReceivedAt: now.Add(-2 * time.Hour), ExpiresAt: &expiry}
	h := r.Health(now, 2*time.Hour)
	if h.AgeSeconds != 7200 || !h.RenewalNeeded || h.Expired {
		t.Fatal(h)
	}
	if err := r.Admit(now, 2*time.Hour); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	if err := r.Admit(expiry, 0); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
}
func TestPrivateStorageRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "s3")); err != nil {
		t.Fatal(err)
	}
	if _, err := (Files{Root: root}).Next("primary"); err == nil {
		t.Fatal("followed credential directory symlink")
	}
}

type memoryJournal struct {
	plans    map[string]Plan
	receipts []Receipt
}

func (m *memoryJournal) RecordPlan(_ context.Context, p Plan) error { m.plans[p.ID] = p; return nil }
func (m *memoryJournal) LoadPlan(_ context.Context, id string) (Plan, error) {
	p, ok := m.plans[id]
	if !ok {
		return Plan{}, ErrStale
	}
	return p, nil
}
func (m *memoryJournal) RecordReceipt(_ context.Context, r Receipt) error {
	m.receipts = append(m.receipts, r)
	return nil
}

func TestConcurrentDeliveryHasOneImmutableVersion(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	j := &memoryJournal{plans: map[string]Plan{}}
	s := Service{Requester: "fixture-requester", Journal: j, Files: Files{Root: root}, Scope: func(context.Context, string) (Scope, error) { return fixtureScope(), nil }}
	p, err := s.Plan(context.Background(), "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := ReadPacket(strings.NewReader(`{"access_key_id":"PLANTED_KEY","secret_access_key":"PLANTED_SECRET"}`))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() { _, err := s.Deliver(context.Background(), p, packet); results <- err })
	}
	wg.Wait()
	close(results)
	succeeded, stale := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrStale) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || stale != 1 || len(j.receipts) != 1 {
		t.Fatal(succeeded, stale, len(j.receipts))
	}
	if _, err := s.Files.Receipt("primary", 1); err != nil {
		t.Fatal(err)
	}
}

type brokenJournal struct {
	memoryJournal
	failPlan, failReceipt bool
}

func (j *brokenJournal) RecordPlan(ctx context.Context, p Plan) error {
	if j.failPlan {
		return errors.New("PLANTED")
	}
	return j.memoryJournal.RecordPlan(ctx, p)
}
func (j *brokenJournal) RecordReceipt(ctx context.Context, r Receipt) error {
	if j.failReceipt {
		return errors.New("PLANTED")
	}
	return j.memoryJournal.RecordReceipt(ctx, r)
}
func TestJournalFailureReservesVersionForReconciliation(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	j := &brokenJournal{memoryJournal: memoryJournal{plans: map[string]Plan{}}, failPlan: true}
	s := Service{Requester: "fixture-requester", Journal: j, Files: Files{Root: root}, Scope: func(context.Context, string) (Scope, error) { return fixtureScope(), nil }}
	if _, err := s.Plan(context.Background(), "hello", nil); !errors.Is(err, ErrStorage) || strings.Contains(err.Error(), "PLANTED") {
		t.Fatal(err)
	}
	j.failPlan = false
	j.failReceipt = true
	p, err := s.Plan(context.Background(), "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := ReadPacket(strings.NewReader(`{"access_key_id":"PLANTED_KEY","secret_access_key":"PLANTED_SECRET"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(context.Background(), "hello", p.ID, packet); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	if _, err := s.Set(context.Background(), "hello", p.ID, packet); !errors.Is(err, ErrStale) {
		t.Fatal("retried uncertain delivery", err)
	}
	if next, err := s.Files.Next("primary"); err != nil || next != 2 {
		t.Fatal(next, err)
	}
}
