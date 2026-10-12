//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func verifyManagedFrontendLoaded(ctx context.Context, session localexec.Session, home, app string) error {
	unit, err := systemd.ParseUnit(app + ".service")
	if err != nil {
		return errEngineSource
	}
	result, err := session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "show", unit.String(), "--all", "--property=SourcePath,FragmentPath,DropInPaths,NeedDaemonReload"}, nil, false)
	if err != nil {
		return errEngineSource
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key != "SourcePath" && key != "FragmentPath" && key != "DropInPaths" && key != "NeedDaemonReload" {
			return errEngineSource
		}
		if _, duplicate := fields[key]; duplicate {
			return errEngineSource
		}
		fields[key] = value
	}
	source := filepath.Join(home, quadlet.ActiveDirectory, app+".container")
	fragment := filepath.Join("/run/user", strconv.Itoa(os.Geteuid()), "systemd/generator", unit.String())
	if len(fields) != 4 || fields["SourcePath"] != source || fields["FragmentPath"] != fragment || fields["DropInPaths"] != "" || fields["NeedDaemonReload"] != "no" {
		return errEngineSource
	}
	return ctx.Err()
}
