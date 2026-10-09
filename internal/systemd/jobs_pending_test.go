package systemd

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestJobPendingReadsOnlyTheSelectedUnit(t *testing.T) {
	for _, tc := range []struct {
		output  string
		pending bool
	}{{"LoadState=loaded\nJob=\n", false}, {"LoadState=not-found\nJob=\n", false}, {"Job=17\nLoadState=loaded\n", true}, {"LoadState=loaded\nJob=4294967295\n", true}} {
		t.Run(tc.output, func(t *testing.T) {
			r := &recorder{results: []localexec.Result{{Stdout: tc.output}}}
			u := unit(t)
			pending, err := client(t, r).JobPending(context.Background(), u)
			if err != nil || pending != tc.pending {
				t.Fatalf("pending=%v err=%v", pending, err)
			}
			if len(r.commands) != 1 {
				t.Fatal(r.commands)
			}
			c := r.commands[0]
			want := []string{"--user", "show", u.String(), "--property=LoadState,Job"}
			if c.Path != "systemctl" || c.Mutation || !reflect.DeepEqual(c.Args, want) {
				t.Fatalf("job read command: %#v", c)
			}
		})
	}
}
func TestJobPendingNeverTreatsFailedOrMalformedReadAsEmptyQueue(t *testing.T) {
	for _, output := range []string{"", " \n", "private-output", "-1", "+0", "0\n1", "4294967296", "Job=0", "LoadState=loaded", "LoadState=\nJob=\n", "LoadState=loaded\nJob=-1\n", "LoadState=loaded\nJob=+0\n", "LoadState=loaded\nJob=4294967296\n", "LoadState=loaded\nJob=\nJob=\n", "LoadState=loaded\nJob=\nUnexpected=\n"} {
		t.Run(output, func(t *testing.T) {
			r := &recorder{results: []localexec.Result{{Stdout: output}}}
			pending, err := client(t, r).JobPending(context.Background(), unit(t))
			var runtime *localexec.Error
			if pending || !errors.As(err, &runtime) || runtime.Kind != localexec.Failed {
				t.Fatalf("pending=%v err=%v", pending, err)
			}
		})
	}
	for _, cause := range []error{&localexec.Error{Kind: localexec.UnknownOutcome}, &localexec.Error{Kind: localexec.Timeout}, &localexec.Error{Kind: localexec.NotFound}} {
		r := &recorder{err: cause}
		pending, err := client(t, r).JobPending(context.Background(), unit(t))
		if pending || !errors.Is(err, cause) {
			t.Fatalf("read failure hidden: %v", err)
		}
	}
}
func TestJobPendingRejectsInvalidUnitAndUnconfiguredFake(t *testing.T) {
	r := &recorder{}
	if _, err := client(t, r).JobPending(context.Background(), Unit{}); err == nil || len(r.commands) != 0 {
		t.Fatal("invalid unit reached the subprocess")
	}
	if _, err := (&Fake{}).JobPending(context.Background(), unit(t)); err == nil {
		t.Fatal("unconfigured job read implies an empty queue")
	}
}
