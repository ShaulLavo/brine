//go:build linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/runnerstorage"
)

func validateRunnerStorage(ctx context.Context, session localexec.Session) error {
	if runnerstorage.CheckLocal() != nil {
		return runnerstorage.ErrProfile
	}
	version, err := session.Execute(ctx, runnerstorage.FusePath, []string{"--version"}, nil, false)
	if err != nil || runnerstorage.ValidateVersion(version.Stdout) != nil {
		return runnerstorage.ErrProfile
	}
	info, err := session.Execute(ctx, "/usr/bin/podman", []string{"info", "--format=json"}, nil, false)
	if err != nil || runnerstorage.ValidateInfo([]byte(info.Stdout)) != nil {
		return runnerstorage.ErrProfile
	}
	return nil
}
