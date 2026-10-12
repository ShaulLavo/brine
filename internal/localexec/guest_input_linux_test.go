//go:build linux

package localexec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
	"time"
)

func TestGuestInputEnvironmentHelper(t *testing.T) {
	if !slices.Contains(os.Args, "BRINE_GUEST_INPUT_HELPER=1") {
		return
	}
	values := []string{os.Getenv("PATH"), os.Getenv("HOME"), os.Getenv("TMPDIR"), os.Getenv("TOKIO_WORKER_THREADS"), os.Getenv("BRINE_GUEST_PRIVATE")}
	_ = json.NewEncoder(os.Stdout).Encode(values)
	os.Exit(0)
}

func TestGuestInputCoreUsesFixedEnvironmentWithoutHostAccountLookup(t *testing.T) {
	t.Setenv("BRINE_GUEST_PRIVATE", "must-not-inherit")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := Command{Path: os.Args[0], Args: []string{"-test.run=^TestGuestInputEnvironmentHelper$", "--", "BRINE_GUEST_INPUT_HELPER=1"}, Dir: "/tmp", OutputLimit: 1024}
	output, err := runInputCommand(ctx, command.Path, command, guestInputEnvironment())
	var values []string
	if err != nil || json.Unmarshal(output.Stdout, &values) != nil || !slices.Equal(values, []string{"/usr/bin:/bin", "/tmp", "/tmp", "2", ""}) {
		t.Fatal("real child did not receive the closed guest environment", err)
	}
	command.Env = guestInputEnvironment()
	if _, err = (ExecRunner{}).RunInput(ctx, command); err == nil {
		t.Fatal("generic host runner accepted private guest environment overrides")
	}
	var boundary *Error
	if !errors.As(err, &boundary) || boundary.Kind != Invalid {
		t.Fatal("generic host refusal changed", err)
	}
	command.Env = nil
	if _, err = (GuestRunner{}).RunInput(ctx, command); !errors.As(err, &boundary) || boundary.Kind != Invalid {
		t.Fatal("ordinary host escaped private guest boundary", err)
	}
}
