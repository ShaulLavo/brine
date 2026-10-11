package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestArchiveCommandRejectsPathsAndInvalidPlansBeforeRuntime(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	for _, args := range [][]string{{"host", "archive-prepare", "hello"}, {"host", "archive-prepare", "hello", "--plan-id", "bad"}, {"host", "archive-prepare", "hello", "--plan", "--plan-id", hash}, {"host", "archive-prepare", "hello", "--plan", "--archive-path", "/private/data"}, {"host", "archive-prepare", "hello", "--plan", "--requester", "local-operator"}} {
		var stdout, stderr bytes.Buffer
		deps := testDependencies(t, &stdout, &stderr)
		if err := ExecuteWithRuntime(deps, args, RuntimeLifecycle{Open: func(context.Context, bool) (RuntimeServices, error) {
			t.Fatal("invalid command opened host runtime")
			return RuntimeServices{}, nil
		}}); err == nil {
			t.Fatal("invalid command admitted", args)
		}
	}
}
func TestArchiveCommandIsNotRemoteDispatch(t *testing.T) {
	for _, test := range []struct {
		args    []string
		archive bool
	}{{[]string{"host", "archive-prepare", "hello", "--plan"}, true}, {[]string{"host", "serve"}, false}, {[]string{"app", "remove", "hello"}, false}} {
		if got := HostArchiveRequested(context.Background(), test.args); got != test.archive {
			t.Fatal("incorrect archive selection", test.args, got)
		}
	}
}
