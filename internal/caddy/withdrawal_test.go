package caddy

import (
	"context"
	"errors"
	"testing"
)

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
