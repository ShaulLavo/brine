//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/quadlet"
)

type frontendLoadedRunner struct {
	output  string
	failure error
}

func (r frontendLoadedRunner) Execute(_ context.Context, command localexec.Command) (localexec.Result, error) {
	if command.Path != "/usr/bin/systemctl" || command.Mutation || strings.Join(command.Args, " ") != "--user show frontend.service --all --property=SourcePath,FragmentPath,DropInPaths,NeedDaemonReload" {
		return localexec.Result{}, &localexec.Error{Kind: localexec.Invalid}
	}
	return localexec.Result{Stdout: r.output}, r.failure
}
func TestManagedFrontendLoadedServiceRequiresOwnedGeneratorProvenance(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, quadlet.ActiveDirectory, "frontend.container")
	fragment := filepath.Join("/run/user", strconv.Itoa(os.Geteuid()), "systemd/generator/frontend.service")
	original := "SourcePath=" + source + "\nFragmentPath=" + fragment + "\nDropInPaths=\nNeedDaemonReload=no\n"
	for _, test := range []struct {
		output   string
		failure  error
		accepted bool
	}{
		{original, nil, true},
		{strings.Replace(original, source, "/foreign/frontend.container", 1), nil, false},
		{strings.Replace(original, fragment, filepath.Join(home, ".config/systemd/user/frontend.service"), 1), nil, false},
		{strings.Replace(original, "DropInPaths=", "DropInPaths=/foreign/override.conf", 1), nil, false},
		{strings.Replace(original, "NeedDaemonReload=no", "NeedDaemonReload=yes", 1), nil, false},
		{original + "SourcePath=" + source + "\n", nil, false},
		{strings.Replace(original, "NeedDaemonReload=no\n", "", 1), nil, false},
		{original, &localexec.Error{Kind: localexec.NotFound, ExitCode: -1}, false},
	} {
		session, err := localexec.NewSession(frontendLoadedRunner{test.output, test.failure}, 1234, home, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		err = verifyManagedFrontendLoaded(context.Background(), session, home, "frontend")
		if (err == nil) != test.accepted {
			t.Fatal("foreign loaded frontend accepted", err, test.accepted)
		}
	}
}
