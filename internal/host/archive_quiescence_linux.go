//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/replication"
)

var containerIdentity = regexp.MustCompile(`^[a-f0-9]{64}$`)

type archiveHost struct {
	service   Service
	stateRoot string
	session   localexec.Session
	executor  localexec.Executor
	home      string
	routes    apply.RemovalRoutes
	units     apply.RemovalUnits
}

func (h archiveHost) serviceStopped(ctx context.Context, name string, allowAbsent bool) (bool, error) {
	r, err := h.session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "show", name, "--property=LoadState,ActiveState,SubState,MainPID,ControlPID,ControlGroup,Job"}, nil, false)
	if err != nil || r.ExitCode != 0 || r.Truncated {
		return false, archive.ErrRefused
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
		key, value, ok := strings.Cut(line, "=")
		switch key {
		case "LoadState", "ActiveState", "SubState", "MainPID", "ControlPID", "ControlGroup", "Job":
		default:
			return false, archive.ErrRefused
		}
		if !ok {
			return false, archive.ErrRefused
		}
		if _, exists := fields[key]; exists {
			return false, archive.ErrRefused
		}
		fields[key] = value
	}
	if len(fields) != 7 {
		return false, archive.ErrRefused
	}
	for _, key := range []string{"MainPID", "ControlPID"} {
		if _, err := strconv.ParseUint(fields[key], 10, 64); err != nil {
			return false, archive.ErrRefused
		}
	}
	if fields["LoadState"] != "loaded" && fields["LoadState"] != "not-found" {
		return false, archive.ErrRefused
	}
	if fields["MainPID"] != "0" || fields["ControlPID"] != "0" || fields["ControlGroup"] != "" || fields["Job"] != "" {
		return false, nil
	}
	absent := allowAbsent && fields["LoadState"] == "not-found" && fields["ActiveState"] == "inactive" && fields["SubState"] == "dead"
	stopped := !allowAbsent && fields["LoadState"] == "loaded" && (fields["ActiveState"] == "inactive" && fields["SubState"] == "dead" || fields["ActiveState"] == "failed" && fields["SubState"] == "failed")
	return absent || stopped, nil
}
func (h archiveHost) stopService(ctx context.Context, name string, allowAbsent bool) error {
	stopped, err := h.serviceStopped(ctx, name, allowAbsent)
	if err == nil && stopped {
		return nil
	}
	// An unpublished service has no recorded ownership. Never stop it.
	if allowAbsent || err != nil {
		return archive.ErrRefused
	}
	r, err := h.session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "stop", name}, nil, true)
	if err != nil || r.ExitCode != 0 || r.Truncated {
		return archive.ErrRecovery
	}
	return nil
}
func (h archiveHost) StopWriters(ctx context.Context, p archive.Plan) error {
	return h.stopService(ctx, p.Candidate.App+".service", p.Candidate.ReleaseID == "")
}
func (h archiveHost) WritersStopped(ctx context.Context, p archive.Plan) (bool, error) {
	stopped, err := h.serviceStopped(ctx, p.Candidate.App+".service", p.Candidate.ReleaseID == "")
	if err != nil || !stopped {
		return false, archive.ErrRefused
	}
	listed, err := h.session.Execute(ctx, "/usr/bin/podman", []string{"ps", "--all", "--quiet", "--no-trunc"}, nil, false)
	if err != nil || listed.ExitCode != 0 || listed.Truncated {
		return false, archive.ErrRefused
	}
	ids := strings.Fields(listed.Stdout)
	if len(ids) > 128 {
		return false, archive.ErrRefused
	}
	if len(ids) == 0 {
		return true, nil
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !containerIdentity.MatchString(id) || seen[id] {
			return false, archive.ErrRefused
		}
		seen[id] = true
	}
	args := append([]string{"container", "inspect"}, ids...)
	inspected, err := h.session.Execute(ctx, "/usr/bin/podman", args, nil, false)
	if err != nil || inspected.ExitCode != 0 || inspected.Truncated {
		return false, archive.ErrRefused
	}
	var containers []struct {
		ID    string `json:"Id"`
		State struct {
			Running *bool `json:"Running"`
			PID     *int  `json:"Pid"`
		} `json:"State"`
		Mounts []struct {
			Source string `json:"Source"`
		} `json:"Mounts"`
	}
	if json.Unmarshal([]byte(inspected.Stdout), &containers) != nil || len(containers) != len(ids) {
		return false, archive.ErrRefused
	}
	observed := map[string]bool{}
	for _, container := range containers {
		if !seen[container.ID] || observed[container.ID] || container.State.Running == nil || container.State.PID == nil || container.Mounts == nil {
			return false, archive.ErrRefused
		}
		observed[container.ID] = true
		if !*container.State.Running && *container.State.PID == 0 {
			continue
		}
		for _, mount := range container.Mounts {
			if !filepath.IsAbs(mount.Source) || filepath.Clean(mount.Source) != mount.Source {
				return false, archive.ErrRefused
			}
			for _, tree := range p.Trees {
				if data.OverlappingPaths(mount.Source, filepath.Join(string(tree.Root), tree.SourceRelative)) {
					return false, archive.ErrRefused
				}
			}
		}
	}
	return true, nil
}
func (h archiveHost) StopReplicas(ctx context.Context, p archive.Plan) error {
	for _, database := range p.Candidate.Databases {
		for _, binding := range database.Epochs {
			name, err := replication.ServiceName(string(binding.BindingID))
			if err != nil {
				return err
			}
			if err = h.stopService(ctx, name, !binding.Committed); err != nil {
				return err
			}
		}
	}
	return nil
}
func (h archiveHost) ReplicasStopped(ctx context.Context, p archive.Plan) (bool, error) {
	for _, database := range p.Candidate.Databases {
		for _, binding := range database.Epochs {
			name, err := replication.ServiceName(string(binding.BindingID))
			if err != nil {
				return false, err
			}
			stopped, err := h.serviceStopped(ctx, name, !binding.Committed)
			if err != nil || !stopped {
				return false, archive.ErrRefused
			}
		}
	}
	return true, nil
}
func (h archiveHost) AcquireLifetimeLocks(ctx context.Context, p archive.Plan) (func() error, error) {
	releases := []func() error{}
	release := func() error {
		var errs []error
		for i := len(releases) - 1; i >= 0; i-- {
			errs = append(errs, releases[i]())
		}
		return errors.Join(errs...)
	}
	seen := map[data.ReplicaBindingID]bool{}
	for _, database := range p.Candidate.Databases {
		for _, binding := range database.Epochs {
			if seen[binding.BindingID] {
				continue
			}
			seen[binding.BindingID] = true
			expected := filepath.Join(h.stateRoot, "replica-locks", string(binding.BindingID)+".lock")
			if binding.Committed && (binding.LifetimeLockFile != expected || data.VerifyRunnerFile(expected) != nil) {
				return nil, errors.Join(archive.ErrRefused, release())
			}
			if err := ensurePrivateChild(h.stateRoot, "replica-locks"); err != nil {
				return nil, errors.Join(err, release())
			}
			lock, err := replication.AcquireLifetimeLock(ctx, expected)
			if err != nil {
				return nil, errors.Join(err, release())
			}
			releases = append(releases, lock.Release)
		}
	}
	return release, nil
}
