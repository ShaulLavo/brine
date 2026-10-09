package systemd

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type recorder struct {
	commands []localexec.Command
	results  []localexec.Result
	err      error
	errors   []error
}

func (r *recorder) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	r.commands = append(r.commands, c)
	err := r.err
	if len(r.errors) > 0 {
		err = r.errors[0]
		r.errors = r.errors[1:]
	}
	if len(r.results) == 0 {
		return localexec.Result{}, err
	}
	v := r.results[0]
	r.results = r.results[1:]
	return v, err
}
func client(t *testing.T, r *recorder) *Client {
	t.Helper()
	s, e := localexec.NewSession(r, 1234, "/tmp", time.Second)
	if e != nil {
		t.Fatal(e)
	}
	return New(s)
}
func unit(t *testing.T) Unit {
	t.Helper()
	u, e := ParseUnit("brine-app.service")
	if e != nil {
		t.Fatal(e)
	}
	return u
}

const showOutput = "LoadState=loaded\nNRestarts=0\nActiveState=active\nSubState=running\nActiveEnterTimestampMonotonic=16502208\n"

func TestArgv(t *testing.T) {
	ctx := context.Background()
	u := unit(t)
	tests := []struct {
		want     []string
		mutation bool
		output   string
		run      func(*Client) error
	}{
		{[]string{"--user", "daemon-reload"}, true, "", func(c *Client) error { return c.DaemonReload(ctx) }},
		{[]string{"--user", "start", u.String()}, true, "", func(c *Client) error { return c.Start(ctx, u) }},
		{[]string{"--user", "stop", u.String()}, true, "", func(c *Client) error { return c.Stop(ctx, u) }},
		{[]string{"--user", "restart", u.String()}, true, "", func(c *Client) error { return c.Restart(ctx, u) }},
		{[]string{"--user", "is-active", u.String()}, false, "active\n", func(c *Client) error { _, e := c.IsActive(ctx, u); return e }},
		{[]string{"--user", "show", u.String(), "--property=LoadState,ActiveState,SubState,NRestarts,ActiveEnterTimestampMonotonic"}, false, showOutput, func(c *Client) error { _, e := c.Show(ctx, u); return e }},
		{[]string{"reload", "caddy.service"}, true, "", func(c *Client) error { return c.ReloadCaddy(ctx) }},
	}
	for _, tt := range tests {
		r := &recorder{results: []localexec.Result{{Stdout: tt.output}}}
		if len(tt.want) > 1 && (tt.want[1] == "start" || tt.want[1] == "stop" || tt.want[1] == "restart") {
			r.results = append([]localexec.Result{{Stdout: showOutput}}, r.results...)
		}
		if e := tt.run(client(t, r)); e != nil {
			t.Fatal(e)
		}
		cmd := r.commands[len(r.commands)-1]
		if cmd.Path != "systemctl" || !reflect.DeepEqual(cmd.Args, tt.want) || cmd.Mutation != tt.mutation || cmd.Stdin != nil || cmd.Timeout != time.Second || cmd.Dir != "/tmp" {
			t.Fatalf("command = %#v", cmd)
		}
	}
}

// Captured with systemctl show -p on Debian 13 arm64, systemd 257.13.
// Only selected state properties are retained; no host or unit identity remains.
func TestRealShowFixture(t *testing.T) {
	b, e := os.ReadFile("testdata/show.txt")
	if e != nil {
		t.Fatal(e)
	}
	r := &recorder{results: []localexec.Result{{Stdout: string(b)}}}
	p, e := client(t, r).Show(context.Background(), unit(t))
	if e != nil || p.ActiveState != "active" || p.SubState != "running" || p.NRestarts != 0 || p.ActiveEnterTimestampMonotonic != 16502208 {
		t.Fatalf("properties = %#v %v", p, e)
	}
}
func TestMissingAndMalformedShow(t *testing.T) {
	for _, out := range []string{"LoadState=not-found\n", "private-secret", strings.ReplaceAll(showOutput, "NRestarts=0", "NRestarts=-1"), showOutput + "ActiveState=failed\n"} {
		r := &recorder{results: []localexec.Result{{Stdout: out}}}
		_, e := client(t, r).Show(context.Background(), unit(t))
		var re *localexec.Error
		if !errors.As(e, &re) || strings.Contains(e.Error(), "private-secret") {
			t.Fatalf("error = %v", e)
		}
		if strings.HasPrefix(out, "LoadState=not-found") && re.Kind != localexec.NotFound {
			t.Fatal(e)
		}
	}
}
func TestIsActiveExitClassification(t *testing.T) {
	for _, tt := range []struct {
		code int
		out  string
		want bool
		kind localexec.ErrorKind
	}{{0, "active\n", true, ""}, {3, "inactive\n", false, ""}, {3, "failed\n", false, ""}, {4, "unknown\n", false, localexec.NotFound}, {1, "private-secret", false, localexec.Failed}} {
		r := &recorder{results: []localexec.Result{{Stdout: tt.out}}}
		if tt.code != 0 {
			r.errors = []error{&localexec.Error{Kind: localexec.Failed, ExitCode: tt.code}, nil}
			if tt.code == 3 {
				r.results = append(r.results, localexec.Result{Stdout: showOutput})
			}
		}
		v, e := client(t, r).IsActive(context.Background(), unit(t))
		if v != tt.want {
			t.Fatal(v)
		}
		if tt.kind == "" {
			if e != nil {
				t.Fatal(e)
			}
		} else {
			var re *localexec.Error
			if !errors.As(e, &re) || re.Kind != tt.kind {
				t.Fatal(e)
			}
		}
	}
}
func TestUnits(t *testing.T) {
	for _, v := range []string{"", "--help", "a.service b.service", "*.service", "a;cmd.service", "a\n.service", "a/../b.service", "a.socket", "a@@b.service", "a=flag.service"} {
		if _, e := ParseUnit(v); e == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	r := &recorder{}
	if e := client(t, r).Start(context.Background(), Unit{}); e == nil || len(r.commands) > 0 {
		t.Fatal("zero unit executed")
	}
	for _, v := range []string{"brine-app.service", "worker@instance.service"} {
		if _, e := ParseUnit(v); e != nil {
			t.Fatal(e)
		}
	}
}
func TestFake(t *testing.T) {
	var a Adapter = &Fake{StartFunc: func(context.Context, Unit) error { return nil }}
	if e := a.Start(context.Background(), unit(t)); e != nil {
		t.Fatal(e)
	}
	if e := a.ReloadCaddy(context.Background()); e == nil {
		t.Fatal("unconfigured fake succeeded")
	}
}

func TestIsActiveMissingUnitIsNotJustInactive(t *testing.T) {
	b, e := os.ReadFile("testdata/missing-show.txt")
	if e != nil {
		t.Fatal(e)
	}
	r := &recorder{results: []localexec.Result{{Stdout: "inactive\n"}, {Stdout: string(b)}}, errors: []error{&localexec.Error{Kind: localexec.Failed, ExitCode: 3}, nil}}
	_, e = client(t, r).IsActive(context.Background(), unit(t))
	var re *localexec.Error
	if !errors.As(e, &re) || re.Kind != localexec.NotFound {
		t.Fatalf("missing unit = %v", e)
	}
}

func TestMissingServicePreventsMutation(t *testing.T) {
	for _, run := range []func(*Client) error{func(c *Client) error { return c.Start(context.Background(), unit(t)) }, func(c *Client) error { return c.Stop(context.Background(), unit(t)) }, func(c *Client) error { return c.Restart(context.Background(), unit(t)) }} {
		r := &recorder{results: []localexec.Result{{Stdout: "LoadState=not-found\n"}}}
		e := run(client(t, r))
		var re *localexec.Error
		if !errors.As(e, &re) || re.Kind != localexec.NotFound || len(r.commands) != 1 || r.commands[0].Mutation {
			t.Fatalf("missing unit mutated: %v", e)
		}
	}
}
func TestCaddyUnknownOutcome(t *testing.T) {
	r := &recorder{err: &localexec.Error{Kind: localexec.UnknownOutcome}}
	e := client(t, r).ReloadCaddy(context.Background())
	var re *localexec.Error
	if !errors.As(e, &re) || re.Kind != localexec.UnknownOutcome {
		t.Fatal(e)
	}
}
func TestAllFakeOperations(t *testing.T) {
	ctx := context.Background()
	u := unit(t)
	calls := 0
	f := &Fake{DaemonReloadFunc: func(context.Context) error { calls++; return nil }, StartFunc: func(context.Context, Unit) error { calls++; return nil }, StopFunc: func(context.Context, Unit) error { calls++; return nil }, RestartFunc: func(context.Context, Unit) error { calls++; return nil }, IsActiveFunc: func(context.Context, Unit) (bool, error) { calls++; return true, nil }, ShowFunc: func(context.Context, Unit) (Properties, error) { calls++; return Properties{}, nil }, ReloadCaddyFunc: func(context.Context) error { calls++; return nil }}
	f.DaemonReload(ctx)
	f.Start(ctx, u)
	f.Stop(ctx, u)
	f.Restart(ctx, u)
	f.IsActive(ctx, u)
	f.Show(ctx, u)
	f.ReloadCaddy(ctx)
	if calls != 7 {
		t.Fatal(calls)
	}
	empty := &Fake{}
	a := empty.DaemonReload(ctx)
	b := empty.Start(ctx, u)
	c := empty.Stop(ctx, u)
	d := empty.Restart(ctx, u)
	_, e := empty.IsActive(ctx, u)
	_, g := empty.Show(ctx, u)
	h := empty.ReloadCaddy(ctx)
	for _, err := range []error{a, b, c, d, e, g, h} {
		if err == nil {
			t.Fatal("unconfigured fake succeeded")
		}
	}
}
