//go:build linux

package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fixtureCleanupDirectory = "/tmp/brine-enroll.ABCDEFGH"

func TestClientEnrollmentCleanupFailure(t *testing.T) {
	o, f := fakeClientOptions(t)
	f.cleanupError = errors.New("cleanup transport unavailable")
	client := Client{Runner: f, Input: strings.NewReader("fixture\n"), Output: &bytes.Buffer{}, Terminal: true}

	err := client.Run(context.Background(), o)
	if err == nil {
		t.Fatal("cleanup failure was discarded")
	}
	message := err.Error()
	if !strings.Contains(message, "enrollment temporary upload cleanup failed") || !strings.Contains(message, fixtureCleanupDirectory) {
		t.Fatalf("cleanup diagnostic = %q", message)
	}
	if !strings.Contains(message, "enrollment state may be complete") {
		t.Fatalf("cleanup diagnostic does not preserve completion warning: %q", message)
	}
	if _, statErr := os.Stat(filepath.Join(o.ConfigDir, "fixture.json")); statErr != nil {
		t.Fatalf("verified target missing after cleanup failure: %v", statErr)
	}
	if f.cleanupCount != 1 || f.cleanupRemote != "rm -- "+fixtureCleanupDirectory+"/brine; rmdir -- "+fixtureCleanupDirectory {
		t.Fatalf("cleanup count=%d command=%q", f.cleanupCount, f.cleanupRemote)
	}
	if !f.facts.OwnedRunner {
		t.Fatal("cleanup failure changed enrolled host state")
	}
}

func TestClientDeclinedEnrollmentCleanupFailure(t *testing.T) {
	o, f := fakeClientOptions(t)
	f.cleanupError = errors.New("cleanup transport unavailable")
	client := Client{Runner: f, Input: strings.NewReader("other\n"), Output: &bytes.Buffer{}, Terminal: true}

	err := client.Run(context.Background(), o)
	if err == nil {
		t.Fatal("declined enrollment unexpectedly succeeded")
	}
	message := err.Error()
	if !strings.Contains(message, "enrollment confirmation must exactly match the target name") {
		t.Fatalf("primary confirmation error lost: %q", message)
	}
	if !strings.Contains(message, "enrollment temporary upload cleanup failed") {
		t.Fatalf("cleanup error lost: %q", message)
	}
	if f.cleanupCount != 1 || f.cleanupRemote != "rm -- "+fixtureCleanupDirectory+"/brine; rmdir -- "+fixtureCleanupDirectory {
		t.Fatalf("cleanup count=%d command=%q", f.cleanupCount, f.cleanupRemote)
	}
	if hasEnrollmentAction(f, "apply") {
		t.Fatal("declined enrollment invoked apply")
	}
}

func TestClientCanceledEnrollmentCleanup(t *testing.T) {
	o, f := fakeClientOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.cancelPrimary = cancel
	client := Client{Runner: f, Input: strings.NewReader("fixture\n"), Output: &bytes.Buffer{}, Terminal: true}

	if err := client.Run(ctx, o); err == nil {
		t.Fatal("canceled enrollment unexpectedly succeeded")
	}
	if f.cleanupCount != 1 || f.cleanupContextErr != nil {
		t.Fatalf("cleanup count=%d context error=%v", f.cleanupCount, f.cleanupContextErr)
	}
	remaining := time.Until(f.cleanupDeadline)
	if remaining <= 0 || remaining > 30*time.Second {
		t.Fatalf("cleanup deadline remaining=%s", remaining)
	}
}

func TestClientCleanupRunsForValidDirectoryPaths(t *testing.T) {
	cases := []struct {
		name      string
		options   func(*testing.T) (Options, *operatorFake)
		input     string
		setup     func(*operatorFake, *Options)
		wantErr   bool
		wantApply bool
	}{
		{name: "upload failure", options: fakeClientOptions, setup: func(f *operatorFake, _ *Options) { f.failUpload = true }, wantErr: true},
		{name: "probe failure", options: fakeClientOptions, setup: func(f *operatorFake, _ *Options) { f.refuseProbe = true }, wantErr: true},
		{name: "declined confirmation", options: fakeClientOptions, input: "other\n", wantErr: true},
		{name: "successful enrollment", options: fakeClientOptions, wantApply: true},
		{name: "successful undo", options: fakeClientOptions, setup: func(f *operatorFake, o *Options) {
			f.facts.OwnedRunner = true
			o.Undo = true
		}},
		{name: "successful finish", options: finishClientOptions},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			o, f := testCase.options(t)
			if testCase.setup != nil {
				testCase.setup(f, &o)
			}
			input := testCase.input
			if input == "" {
				input = "fixture\n"
			}
			client := Client{Runner: f, Input: strings.NewReader(input), Output: &bytes.Buffer{}, Terminal: true}
			err := client.Run(context.Background(), o)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("error=%v want error=%v", err, testCase.wantErr)
			}
			if f.cleanupCount != 1 || f.cleanupRemote != "rm -- "+fixtureCleanupDirectory+"/brine; rmdir -- "+fixtureCleanupDirectory {
				t.Fatalf("cleanup count=%d command=%q", f.cleanupCount, f.cleanupRemote)
			}
			if testCase.wantApply != hasEnrollmentAction(f, "apply") {
				t.Fatalf("apply invoked=%v want=%v", hasEnrollmentAction(f, "apply"), testCase.wantApply)
			}
		})
	}
}

func TestClientInvalidTempDirectoryNeverCleansGuessedPath(t *testing.T) {
	for _, reply := range [][]byte{[]byte("/tmp/not-brine\n"), []byte("\n")} {
		t.Run(strings.TrimSpace(string(reply)), func(t *testing.T) {
			o, f := fakeClientOptions(t)
			f.tempReply = reply
			client := Client{Runner: f, Input: strings.NewReader("fixture\n"), Output: &bytes.Buffer{}, Terminal: true}
			if err := client.Run(context.Background(), o); err == nil {
				t.Fatal("invalid temp directory accepted")
			}
			if f.cleanupCount != 0 {
				t.Fatalf("cleanup count=%d for invalid directory", f.cleanupCount)
			}
		})
	}
}

func hasEnrollmentAction(f *operatorFake, want string) bool {
	for _, command := range f.commands {
		var request HostRequest
		if len(command.Stdin) == 0 || json.Unmarshal(command.Stdin, &request) != nil {
			continue
		}
		if request.Action == want {
			return true
		}
	}
	return false
}
