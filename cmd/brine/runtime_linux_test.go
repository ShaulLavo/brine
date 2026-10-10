//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/cli"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestProductionRuntimeInitializationErrorReachesResponse(t *testing.T) {
	identity, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	if identity.Username == "brine" {
		t.Skip("requires a non-enrolled fixture process")
	}
	var out bytes.Buffer
	deps := cli.Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &out, Stderr: io.Discard, HostUID: func() int { return 1000 }}
	code := runWithRuntime(deps, []string{"host", "reconcile", "--json"}, "")
	var response result.Envelope
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if code != 4 || response.OK || response.Error.Code != result.DispatchOperationRefused {
		t.Fatalf("production initialization error lost: exit=%d %+v", code, response)
	}
}

func TestProductionRuntimeHelpAndInvalidInput(t *testing.T) {
	for _, args := range [][]string{
		{"host", "run-op", "--help", "--json"},
		{"host", "run-op", "--json"},
		{"host", "run-op", "../invalid", "--json"},
		{"host", "reconcile", "extra", "--json"},
		{"host", "reconcile", "--unknown", "--json"},
		{"host", "reconcile", "--json", "--jsonl"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps := cli.Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &out, Stderr: &diagnostics, HostUID: func() int { return 1000 }}
			code := runWithRuntime(deps, args, "")
			want := 2
			if strings.Contains(strings.Join(args, " "), "--help") {
				want = 0
			}
			if code != want {
				t.Fatalf("exit=%d want=%d diagnostics=%s", code, want, diagnostics.String())
			}
			var response result.Envelope
			if err := json.Unmarshal(out.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.OK != (code == 0) {
				t.Fatal("exit and response disagree")
			}
		})
	}
}

type suppliedDetachedRunner func(context.Context, string) error

func (f suppliedDetachedRunner) Run(ctx context.Context, id string) error { return f(ctx, id) }

func TestProductionWiringPreservesExplicitDetachedRunner(t *testing.T) {
	var out bytes.Buffer
	calls := 0
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	deps := cli.Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &out, Stderr: io.Discard, HostUID: func() int { return 1000 }, HostOperationRunner: suppliedDetachedRunner(func(_ context.Context, got string) error {
		calls++
		if got != id {
			t.Fatal("wrong detached operation")
		}
		return nil
	})}
	code := runWithRuntime(deps, []string{"host", "run-op", id, "--json"}, "")
	if code != 0 || calls != 1 {
		t.Fatalf("production replaced supplied worker: exit=%d calls=%d response=%s", code, calls, out.String())
	}
}
