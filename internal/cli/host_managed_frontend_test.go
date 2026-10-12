package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestManagedFrontendStartupUsesOnlyItsReadOnlyBundle(t *testing.T) {
	opened, called, closed := 0, 0, 0
	binding := strings.Repeat("a", 32)
	deps := Dependencies{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Context: context.Background(), HostUID: func() int { return 1000 }}
	lifecycle := RuntimeLifecycle{Open: func(context.Context, bool) (RuntimeServices, error) {
		t.Fatal("frontend gate opened full deployment bundle")
		return RuntimeServices{}, nil
	}, OpenEngineStartup: func(context.Context) (RuntimeServices, error) {
		t.Fatal("frontend gate invoked native startup")
		return RuntimeServices{}, nil
	}, OpenManagedFrontendPermit: func(context.Context) (RuntimeServices, error) {
		opened++
		return RuntimeServices{ManagedFrontendPermit: func(_ context.Context, actual, app string) error {
			called++
			if actual != binding || app != "hello" {
				t.Fatal("frontend binding changed")
			}
			return nil
		}, Close: func() error { closed++; return nil }}, nil
	}}
	err := ExecuteWithRuntime(deps, []string{"host", "managed-frontend-permit", binding, "hello"}, lifecycle)
	if err != nil || opened != 1 || called != 1 || closed != 1 {
		t.Fatal("frontend gate lifecycle", err, opened, called, closed)
	}
}

func TestManagedFrontendStartupInvalidSelectorDoesNotOpenState(t *testing.T) {
	lifecycle := RuntimeLifecycle{OpenManagedFrontendPermit: func(context.Context) (RuntimeServices, error) {
		t.Fatal("malformed frontend selector opened protected state")
		return RuntimeServices{}, errors.New("unreachable")
	}}
	for _, args := range [][]string{{"host", "managed-frontend-permit", "../escape", "hello"}, {"host", "managed-frontend-permit", strings.Repeat("a", 32), "../escape"}} {
		deps := Dependencies{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Context: context.Background(), HostUID: func() int { return 1000 }}
		if ExecuteWithRuntime(deps, args, lifecycle) == nil {
			t.Fatal("invalid frontend selector admitted")
		}
	}
}
