package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/result"
)

func TestDoctorRequiredMissing(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LookPath = func(string) (string, error) { return "", errors.New("synthetic-secret") }
	err := Execute(deps, []string{"doctor", "--json"})
	if result.ExitCode(err) != 3 {
		t.Fatalf("exit = %d, want 3", result.ExitCode(err))
	}
	if out.String() != "{\"schema_version\":1,\"command\":\"brine doctor\",\"ok\":false,\"data\":null,\"error\":{\"code\":\"dependency_missing\",\"message\":\"A required dependency is missing or incompatible.\",\"retryable\":false}}\n" {
		t.Fatalf("response = %q", out.String())
	}
}

type doctorRunnerFunc func(context.Context, string, ...string) (string, error)

func (f doctorRunnerFunc) Run(ctx context.Context, path string, args ...string) (string, error) {
	return f(ctx, path, args...)
}

func configureDoctorFixture(t *testing.T, deps *Dependencies) {
	t.Helper()
	deps.LookPath = func(name string) (string, error) {
		if name == "ssh" {
			return "/fixture/bin/ssh", nil
		}
		if name != "git" {
			t.Fatalf("unexpected lookup: %q", name)
		}
		return "", errors.New("not found")
	}
	deps.DoctorRunner = doctorRunnerFunc(func(ctx context.Context, path string, args ...string) (string, error) {
		if path != "/fixture/bin/ssh" || !reflect.DeepEqual(args, []string{"-V"}) {
			t.Fatalf("invocation = %q %q", path, args)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > doctorTimeout {
			t.Fatal("missing bounded deadline")
		}
		return "OpenSSH_9.9p2, OpenSSL 3.4.1\n", nil
	})
}

func TestDoctorHumanOutput(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	configureDoctorFixture(t, &deps)
	if err := Execute(deps, []string{"doctor"}); err != nil {
		t.Fatal(err)
	}
	assertStreams(t, &out, &diagnostics, "Local client dependency check\nChecks this machine only; not a remote host readiness audit. No changes are made.\n  ssh          required 9.9p2\n  git          optional not_found\n")
}

func TestDoctorChecks(t *testing.T) {
	for _, tt := range []struct {
		name    string
		tool    int
		output  string
		runErr  error
		missing bool
		want    toolCheck
	}{
		{"ssh found", 0, "OpenSSH_9.9p2, OpenSSL 3.4.1\n", nil, false, toolCheck{"ssh", true, true, "9.9p2", ""}},
		{"windows ssh", 0, "OpenSSH_for_Windows_9.5p1, LibreSSL 3.8.2\n", nil, false, toolCheck{"ssh", true, true, "9.5p1", ""}},
		{"git found", 1, "git version 2.50.1 (Apple Git-155)\n", nil, false, toolCheck{"git", false, true, "2.50.1", ""}},
		{"missing", 1, "", nil, true, toolCheck{"git", false, false, "", "not_found"}},
		{"timeout", 1, "synthetic-secret", context.DeadlineExceeded, false, toolCheck{"git", false, true, "", "timeout"}},
		{"unparseable", 1, "synthetic-secret\x1b[31m", nil, false, toolCheck{"git", false, true, "", "unparseable"}},
		{"exit error", 1, "git version 2.50.1\nsynthetic-secret", errors.New("synthetic-secret"), false, toolCheck{"git", false, true, "", "exit_error"}},
		{"version injection", 0, "OpenSSH_9.9p2-synthetic-secret\n", nil, false, toolCheck{"ssh", true, true, "", "unparseable"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			lookup := func(string) (string, error) {
				if tt.missing {
					return "synthetic-secret", errors.New("not found")
				}
				return "/fixture/bin/tool", nil
			}
			runner := doctorRunnerFunc(func(ctx context.Context, path string, args ...string) (string, error) {
				called = true
				if path != "/fixture/bin/tool" || !reflect.DeepEqual(args, clientTools[tt.tool].args) {
					t.Fatalf("invocation = %q %q", path, args)
				}
				return tt.output, tt.runErr
			})
			got := checkTool(context.Background(), clientTools[tt.tool], lookup, runner)
			if got != tt.want {
				t.Fatalf("check = %+v, want %+v", got, tt.want)
			}
			if called == tt.missing {
				t.Fatalf("runner called = %t, missing = %t", called, tt.missing)
			}
		})
	}
}

func TestDoctorProbeFailures(t *testing.T) {
	for _, required := range []bool{false, true} {
		for _, reason := range []string{"timeout", "unparseable", "exit_error"} {
			t.Run(fmt.Sprintf("required=%t/%s", required, reason), func(t *testing.T) {
				var out, diagnostics bytes.Buffer
				deps := testDependencies(t, &out, &diagnostics)
				deps.LookPath = func(name string) (string, error) { return name, nil }
				deps.DoctorRunner = doctorRunnerFunc(func(_ context.Context, path string, _ ...string) (string, error) {
					if (path == "ssh") == required {
						switch reason {
						case "timeout":
							return "synthetic-secret", context.DeadlineExceeded
						case "exit_error":
							return "synthetic-secret", errors.New("synthetic-secret")
						default:
							return "synthetic-secret\x1b[31m", nil
						}
					}
					if path == "ssh" {
						return "OpenSSH_9.9p2\n", nil
					}
					return "git version 2.50.1\n", nil
				})
				err := Execute(deps, []string{"doctor", "--json"})
				wantExit := 0
				if required {
					wantExit = 3
				}
				if result.ExitCode(err) != wantExit {
					t.Fatalf("exit = %d", result.ExitCode(err))
				}
				if strings.Contains(out.String()+diagnostics.String(), "synthetic-secret") || strings.Contains(out.String()+diagnostics.String(), "\x1b") {
					t.Fatal("raw output leaked")
				}
				if !required && (!strings.Contains(out.String(), `"reason":"`+reason+`"`) || !strings.Contains(out.String(), `"scope":"local_client"`)) {
					t.Fatalf("response = %q", out.String())
				}
			})
		}
	}
}

func TestDoctorCancellation(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps.Context = ctx
	deps.LookPath = func(name string) (string, error) { return name, nil }
	deps.DoctorRunner = doctorRunnerFunc(func(context.Context, string, ...string) (string, error) {
		cancel()
		return "", context.Canceled
	})
	if err := Execute(deps, []string{"doctor", "--json"}); result.ExitCode(err) != 130 {
		t.Fatalf("error = %v", err)
	}
}

func TestDoctorHumanWriteError(t *testing.T) {
	var diagnostics bytes.Buffer
	wantErr := errors.New("write failure")
	deps := testDependencies(t, failingWriter{wantErr}, &diagnostics)
	configureDoctorFixture(t, &deps)
	if err := Execute(deps, []string{"doctor"}); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v", err)
	}
}

func TestDoctorRequiredMissingHuman(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LookPath = func(string) (string, error) { return "", errors.New("synthetic-secret") }
	if err := Execute(deps, []string{"doctor"}); result.ExitCode(err) != 3 {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(out.String(), "Checks this machine only") || !strings.Contains(out.String(), "ssh          required not_found") {
		t.Fatalf("stdout = %q", out.String())
	}
	if strings.Contains(out.String()+diagnostics.String(), "synthetic-secret") {
		t.Fatal("lookup error leaked")
	}
}
