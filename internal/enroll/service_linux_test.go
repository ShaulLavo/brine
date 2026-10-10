package enroll

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestServiceChecksRefuseUnknown(t *testing.T) {
	cases := []struct {
		name   string
		result localexec.Result
		err    error
	}{
		{"missing executable", localexec.Result{}, &localexec.Error{Kind: localexec.NotFound}},
		{"failure", localexec.Result{}, errors.New("probe failed")},
		{"timeout with plausible output", localexec.Result{Stdout: "disabled\n", ExitCode: 1}, &localexec.Error{Kind: localexec.Timeout, ExitCode: 1}},
		{"truncated", localexec.Result{Stdout: "disabled\n", Truncated: true}, nil},
		{"empty", localexec.Result{}, nil},
		{"multiple states", localexec.Result{Stdout: "enabled\ndisabled\n"}, nil},
		{"unknown", localexec.Result{Stdout: "unknown\n", ExitCode: 4}, &localexec.Error{Kind: localexec.Failed, ExitCode: 4}},
		{"wrong exit", localexec.Result{Stdout: "active\n", ExitCode: 2}, &localexec.Error{Kind: localexec.Failed, ExitCode: 2}},
		{"missing error", localexec.Result{Stdout: "disabled\n", ExitCode: 1}, nil},
		{"mismatched error exit", localexec.Result{Stdout: "disabled\n", ExitCode: 1}, &localexec.Error{Kind: localexec.Failed, ExitCode: 2}},
		{"successful output with failure", localexec.Result{Stdout: "active\n"}, &localexec.Error{Kind: localexec.Failed, ExitCode: 0}},
	}
	for _, tt := range cases {
		for _, name := range []string{"mask", "unmask", "caddy-enable", "caddy-start"} {
			t.Run(tt.name+"/"+name, func(t *testing.T) {
				f := &fakeAdmin{handle: func(localexec.Command) (localexec.Result, error) { return tt.result, tt.err }}
				h := &host{exec: f, r: hostRecord{Packages: []Package{{"fixture", "1.0"}}}}
				for _, step := range h.steps() {
					if step.Name != name {
						continue
					}
					j := Journal{}
					if err := Apply(context.Background(), &memoryServiceJournal{}, &j, []Step{step}); err == nil {
						t.Fatal("unknown service state accepted")
					}
				}
				if len(f.commands) != 1 || f.commands[0].Mutation {
					t.Fatalf("mutation after unknown observation: %+v", f.commands)
				}
			})
		}
	}
}

type memoryServiceJournal struct{}

func (*memoryServiceJournal) Save(Journal) error { return nil }

func TestServiceObservationKnownStates(t *testing.T) {
	for _, group := range []struct {
		verb   string
		states []string
		code   int
	}{
		{"is-active", []string{"active", "reloading", "refreshing"}, 0},
		{"is-active", []string{"inactive", "failed", "activating", "deactivating", "maintenance"}, 3},
		{"is-enabled", []string{"enabled", "enabled-runtime", "alias", "static", "indirect", "generated"}, 0},
		{"is-enabled", []string{"disabled", "linked", "linked-runtime", "masked", "masked-runtime"}, 1},
		{"is-enabled", []string{"disabled", "linked", "linked-runtime", "masked", "masked-runtime"}, 3},
		{"is-enabled", []string{"not-found"}, 4},
	} {
		for _, state := range group.states {
			t.Run(group.verb+"/"+state, func(t *testing.T) {
				f := &fakeAdmin{handle: func(localexec.Command) (localexec.Result, error) {
					var err error
					if group.code != 0 {
						err = &localexec.Error{Kind: localexec.Failed, ExitCode: group.code}
					}
					return localexec.Result{Stdout: state + "\n", ExitCode: group.code}, err
				}}
				got, err := (&host{exec: f}).observeService(context.Background(), group.verb)
				if err != nil || got != serviceState(state) {
					t.Fatalf("state=%q error=%v", got, err)
				}
			})
		}
	}
}

func TestOriginalCaddyStateRefusesPartialObservation(t *testing.T) {
	for _, verb := range []string{"is-active", "is-enabled"} {
		for _, failure := range []struct {
			name   string
			result localexec.Result
			err    error
		}{
			{"timeout", localexec.Result{}, &localexec.Error{Kind: localexec.Timeout}},
			{"missing executable", localexec.Result{}, &localexec.Error{Kind: localexec.NotFound}},
			{"empty", localexec.Result{}, nil},
			{"malformed", localexec.Result{Stdout: "invalid"}, nil},
			{"truncated", localexec.Result{Stdout: "active", Truncated: true}, nil},
		} {
			t.Run(verb+"/"+failure.name, func(t *testing.T) {
				f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
					if c.Args[0] == verb {
						return failure.result, failure.err
					}
					return localexec.Result{Stdout: "active\n"}, nil
				}}
				h := &host{exec: f}
				got, err := h.originalCaddyState(context.Background())
				if err == nil || got != (caddyServiceState{}) || h.r.ID != "" {
					t.Fatalf("partial original state captured: %+v %v", got, err)
				}
				for _, c := range f.commands {
					if c.Mutation {
						t.Fatal("mutation during failed original-state capture")
					}
				}
				if verb == "is-active" && len(f.commands) != 1 {
					t.Fatal("continued after failed active observation")
				}
			})
		}
	}
}

func TestOriginalCaddyStateKnownAndMasked(t *testing.T) {
	for _, state := range []string{"enabled", "disabled", "masked", "masked-runtime"} {
		t.Run(state, func(t *testing.T) {
			f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
				if c.Args[0] == "is-active" {
					return localexec.Result{Stdout: "inactive\n", ExitCode: 3}, &localexec.Error{Kind: localexec.Failed, ExitCode: 3}
				}
				code := 0
				if state != "enabled" {
					code = 1
				}
				var err error
				if code != 0 {
					err = &localexec.Error{Kind: localexec.Failed, ExitCode: code}
				}
				return localexec.Result{Stdout: state + "\n", ExitCode: code}, err
			}}
			got, err := (&host{exec: f}).originalCaddyState(context.Background())
			if serviceState(state).masked() {
				if err == nil || got != (caddyServiceState{}) {
					t.Fatal("preexisting mask accepted")
				}
			} else if err != nil || got.active != "inactive" || got.enabled != serviceState(state) {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestServiceObservationCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeAdmin{handle: func(localexec.Command) (localexec.Result, error) {
		cancel()
		return localexec.Result{Stdout: "active\n"}, nil
	}}
	got, err := (&host{exec: f}).observeService(ctx, "is-active")
	if got != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %q %v", got, err)
	}
}

func TestOriginalCaddyStateConfirmedAbsent(t *testing.T) {
	f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
		switch c.Args[0] {
		case "is-active":
			return localexec.Result{Stdout: "inactive\n", ExitCode: 4}, &localexec.Error{Kind: localexec.Failed, ExitCode: 4}
		case "show":
			return localexec.Result{Stdout: "not-found\n"}, nil
		case "is-enabled":
			return localexec.Result{Stdout: "not-found\n", ExitCode: 4}, &localexec.Error{Kind: localexec.Failed, ExitCode: 4}
		}
		t.Fatalf("unexpected command %+v", c)
		return localexec.Result{}, nil
	}}
	got, err := (&host{exec: f}).originalCaddyState(context.Background())
	if err != nil || got.active != "not-found" || got.enabled != "not-found" {
		t.Fatalf("absent unit refused: %+v %v", got, err)
	}
	if len(f.commands) != 3 || strings.Join(f.commands[1].Args, " ") != "show -p LoadState --value caddy.service" {
		t.Fatalf("absence not independently confirmed: %+v", f.commands)
	}
	for _, c := range f.commands {
		if c.Mutation {
			t.Fatal("absence probe mutated")
		}
	}
}

func TestServiceTransientNonzero(t *testing.T) {
	f := &fakeAdmin{handle: func(localexec.Command) (localexec.Result, error) {
		return localexec.Result{Stdout: "transient\n", ExitCode: 1}, &localexec.Error{Kind: localexec.Failed, ExitCode: 1}
	}}
	got, err := (&host{exec: f}).observeService(context.Background(), "is-enabled")
	if err != nil || got != "transient" {
		t.Fatalf("transient state refused: %q %v", got, err)
	}
}

func TestAbsentServiceRequiresKnownLoadState(t *testing.T) {
	for _, tt := range []struct {
		name   string
		result localexec.Result
		err    error
	}{
		{"loaded", localexec.Result{Stdout: "loaded\n"}, nil},
		{"empty", localexec.Result{}, nil},
		{"malformed", localexec.Result{Stdout: "not-found\nloaded\n"}, nil},
		{"truncated", localexec.Result{Stdout: "not-found\n", Truncated: true}, nil},
		{"failed", localexec.Result{Stdout: "not-found\n", ExitCode: 4}, &localexec.Error{Kind: localexec.Failed, ExitCode: 4}},
		{"missing executable", localexec.Result{}, &localexec.Error{Kind: localexec.NotFound}},
		{"timeout", localexec.Result{Stdout: "not-found\n"}, &localexec.Error{Kind: localexec.Timeout}},
		{"unreported failure", localexec.Result{Stdout: "not-found\n", ExitCode: 1}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
				if c.Args[0] == "is-active" {
					return localexec.Result{Stdout: "inactive\n", ExitCode: 4}, &localexec.Error{Kind: localexec.Failed, ExitCode: 4}
				}
				if c.Args[0] == "show" {
					return tt.result, tt.err
				}
				t.Fatalf("continued after unknown absence: %+v", c)
				return localexec.Result{}, nil
			}}
			got, err := (&host{exec: f}).originalCaddyState(context.Background())
			if err == nil || got != (caddyServiceState{}) {
				t.Fatalf("unknown absence accepted: %+v %v", got, err)
			}
			if len(f.commands) != 2 {
				t.Fatalf("commands %+v", f.commands)
			}
			for _, c := range f.commands {
				if c.Mutation {
					t.Fatal("mutation after unknown absence")
				}
			}
		})
	}
}

func TestAbsentServiceConfirmationCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
		if c.Args[0] == "is-active" {
			return localexec.Result{Stdout: "inactive\n", ExitCode: 4}, &localexec.Error{Kind: localexec.Failed, ExitCode: 4}
		}
		cancel()
		return localexec.Result{Stdout: "not-found\n"}, nil
	}}
	got, err := (&host{exec: f}).originalCaddyState(ctx)
	if got != (caddyServiceState{}) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled absence accepted: %+v %v", got, err)
	}
}

func TestServiceTransientRejectsZeroExit(t *testing.T) {
	f := &fakeAdmin{handle: func(localexec.Command) (localexec.Result, error) { return localexec.Result{Stdout: "transient\n"}, nil }}
	got, err := (&host{exec: f}).observeService(context.Background(), "is-enabled")
	if err == nil || got != "" {
		t.Fatalf("inconsistent transient status accepted: %q %v", got, err)
	}
}
