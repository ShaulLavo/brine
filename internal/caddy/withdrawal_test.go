package caddy

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSettleWithdrawalReadbackFailureFencesManager(t *testing.T) {
	m, root, main, state, _, r := setup(t)
	ctx := context.Background()
	raw, err := os.ReadFile("testdata/managed-adapted.json")
	if err != nil {
		t.Fatal(err)
	}
	v := &managedLoadedFixture{raw: raw}
	m.validator = v
	site := managedTestSite(t, true)
	site.managed.authority = &managedAuthorityFixture{}
	first, err := m.Apply(ctx, main, state, Put(site))
	if err != nil {
		t.Fatal(err)
	}
	v.raw = []byte(`{"apps":{"http":{"servers":{"srv0":{"routes":[]}}}}}`)
	withdrawn, err := m.Apply(ctx, main, first.Next, Remove(site.name))
	if err != nil {
		t.Fatal(err)
	}
	v.failure = errors.New("readback unavailable")
	err = m.SettleWithdrawal(ctx, main, first.Next, withdrawn.Next, site.name)
	var unknown *UnknownOutcomeError
	if !errors.As(err, &unknown) || unknown.Stage != "withdrawal-readback" {
		t.Fatalf("expected unknown withdrawal readback, got %v", err)
	}
	v.failure = nil
	if _, err = m.Observe(); err == nil {
		t.Error("observation succeeded on fenced manager")
	}
	_, err = m.Apply(ctx, main, withdrawn.Next, Put(site))
	var operation *OperationError
	if !errors.As(err, &operation) || !strings.Contains(operation.Cause.Error(), "requires reconciliation") {
		t.Errorf("mutation was not refused by the fence: %v", err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewManager(root, v, r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	disk, err := reopened.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.SettleWithdrawal(ctx, main, first.Next, disk, site.name); err != nil {
		t.Fatal(err)
	}
}

func TestSettleWithdrawalValidatesDiskBeforeReload(t *testing.T) {
	m, root, main, state, v, r := setup(t)
	ctx := context.Background()
	first, err := m.Apply(ctx, main, state, Put(fixtureSite(t)))
	if err != nil {
		t.Fatal(err)
	}
	r.errors = []error{nil, context.DeadlineExceeded}
	withdrawn, err := m.Apply(ctx, main, first.Next, Remove("hello"))
	var unknown *UnknownOutcomeError
	if !errors.As(err, &unknown) || withdrawn.Outcome != Unknown {
		t.Fatal(withdrawn, err)
	}
	m.Close()
	reopened, err := NewManager(root, v, r)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	disk, err := reopened.Observe()
	if err != nil {
		t.Fatal(err)
	}
	v.adapted = []byte(`{"apps":{"http":{"servers":{"srv0":{"routes":[]}}}}}`)
	if err = reopened.SettleWithdrawal(ctx, main, first.Next, disk, "hello"); err != nil {
		t.Fatal(err)
	}
	if r.calls != 3 {
		t.Fatal("reload count", r.calls)
	}
	bad := disk
	bad.Files = map[string]string{"unowned.caddy": first.Next.Files["hello.caddy"]}
	if err = reopened.SettleWithdrawal(ctx, main, first.Next, bad, "hello"); err == nil || r.calls != 3 {
		t.Fatal("drift reloaded", err, r.calls)
	}
	v.adapted = []byte(`{"apps":{"http":{"servers":{"srv0":{"routes":[{"match":[{"host":["web.example.com"]}],"handle":[{"handler":"static_response","body":"unowned"}]}]}}}}}`)
	if err = reopened.SettleWithdrawal(ctx, main, first.Next, disk, "hello"); err == nil || r.calls != 3 {
		t.Fatal("withdrawn host still routes", err, r.calls)
	}
}
