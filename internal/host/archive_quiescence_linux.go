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
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/restore"
)

var containerIdentity = regexp.MustCompile(`^[a-f0-9]{64}$`)

type archiveRemovalUnits interface {
	VerifyArchiveRemove(context.Context, string, string) error
	RemoveArchived(context.Context, string, string) error
}
type archiveHost struct {
	removal          bool
	service          Service
	stateRoot        string
	session          localexec.Session
	executor         localexec.Executor
	home             string
	routes           apply.RemovalRoutes
	units            archiveRemovalUnits
	restoreTesting   *archiveRestoreTesting
	removalVerified  map[string]restore.SQLiteReceipt
	recoveryVerified map[string]time.Time
	nativeLifetimes  map[data.ReplicaBindingID]*data.EngineLock
}

func (h archiveHost) serviceStopped(ctx context.Context, name string, allowAbsent bool) (bool, error) {
	unit, err := showUnit(ctx, h.session, name)
	if err != nil {
		return false, archive.ErrRefused
	}
	return unit.stopped(allowAbsent), nil
}

// unitState is the bounded systemd projection shared by archive and SQLite
// preparation quiescence. Unknown, duplicate or malformed fields refuse.
type unitState struct {
	Load, Active, Sub, ControlGroup, Job string
	MainPID, ControlPID                  uint64
}

func showUnit(ctx context.Context, session localexec.Session, name string) (unitState, error) {
	r, err := session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "show", name, "--property=LoadState,ActiveState,SubState,MainPID,ControlPID,ControlGroup,Job"}, nil, false)
	if err != nil || r.ExitCode != 0 || r.Truncated {
		return unitState{}, errUnitState
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
		key, value, ok := strings.Cut(line, "=")
		switch key {
		case "LoadState", "ActiveState", "SubState", "MainPID", "ControlPID", "ControlGroup", "Job":
		default:
			return unitState{}, errUnitState
		}
		if _, exists := fields[key]; exists || !ok {
			return unitState{}, errUnitState
		}
		fields[key] = value
	}
	main, mainErr := strconv.ParseUint(fields["MainPID"], 10, 64)
	control, controlErr := strconv.ParseUint(fields["ControlPID"], 10, 64)
	if len(fields) != 7 || mainErr != nil || controlErr != nil || fields["LoadState"] != "loaded" && fields["LoadState"] != "not-found" {
		return unitState{}, errUnitState
	}
	return unitState{Load: fields["LoadState"], Active: fields["ActiveState"], Sub: fields["SubState"], ControlGroup: fields["ControlGroup"], Job: fields["Job"], MainPID: main, ControlPID: control}, nil
}

var errUnitState = errors.New("host: unit state unavailable")

// stopped requires no process, cgroup or pending job. An absent unit counts
// only where the caller has no recorded ownership of it.
func (u unitState) stopped(allowAbsent bool) bool {
	if u.MainPID != 0 || u.ControlPID != 0 || u.ControlGroup != "" || u.Job != "" {
		return false
	}
	closed := u.Active == "inactive" && u.Sub == "dead" || u.Active == "failed" && u.Sub == "failed"
	absent := allowAbsent && u.Load == "not-found" && closed
	return absent || !allowAbsent && u.Load == "loaded" && closed
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
	if p.Removal != nil {
		return removalWriterObservation(ctx, h, p)
	}
	if nativeArchiveCandidate(p.Candidate) {
		return h.stopNativeArchiveWriters(ctx, p)
	}
	return h.stopService(ctx, p.Candidate.App+".service", p.Candidate.ReleaseID == "")
}
func (h archiveHost) stopNativeArchiveWriters(ctx context.Context, p archive.Plan) error {
	facts, err := h.nativeArchiveOwnedUnits(ctx, p.Candidate)
	if err != nil {
		return archive.ErrRefused
	}
	if facts.frontend != nil {
		return h.stopService(ctx, p.Candidate.App+".service", p.Candidate.ReleaseID == "")
	}
	stopped, err := h.nativeArchiveStopped(ctx, p)
	if err != nil || !stopped {
		return archive.ErrRefused
	}
	return nil
}
func (h archiveHost) WritersStopped(ctx context.Context, p archive.Plan) (bool, error) {
	if nativeArchiveCandidate(p.Candidate) {
		facts, err := h.nativeArchiveOwnedUnits(ctx, p.Candidate)
		if err != nil {
			return false, archive.ErrRefused
		}
		if facts.frontend == nil {
			return h.nativeArchiveStopped(ctx, p)
		}
	}
	stopped, err := h.serviceStopped(ctx, p.Candidate.App+".service", p.Candidate.ReleaseID == "")
	if err != nil || !stopped {
		return false, archive.ErrRefused
	}
	return h.archiveContainersStopped(ctx, p)
}
func (h archiveHost) archiveContainersStopped(ctx context.Context, p archive.Plan) (bool, error) {
	sources := make([]string, 0, len(p.Trees))
	for _, tree := range p.Trees {
		sources = append(sources, filepath.Join(string(tree.Root), tree.SourceRelative))
	}
	if !containersOutside(ctx, h.session, sources) {
		return false, archive.ErrRefused
	}
	return true, nil
}

// containersOutside proves that no running or paused container mounts any
// source path. Unparseable or truncated Podman output refuses.
func containersOutside(ctx context.Context, session localexec.Session, sources []string) bool {
	listed, err := session.Execute(ctx, "/usr/bin/podman", []string{"ps", "--all", "--quiet", "--no-trunc"}, nil, false)
	if err != nil || listed.ExitCode != 0 || listed.Truncated {
		return false
	}
	ids := strings.Fields(listed.Stdout)
	if len(ids) > 128 {
		return false
	}
	if len(ids) == 0 {
		return true
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !containerIdentity.MatchString(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	args := append([]string{"container", "inspect"}, ids...)
	inspected, err := session.Execute(ctx, "/usr/bin/podman", args, nil, false)
	if err != nil || inspected.ExitCode != 0 || inspected.Truncated {
		return false
	}
	var containers []inspectedContainer
	if json.Unmarshal([]byte(inspected.Stdout), &containers) != nil || len(containers) != len(ids) {
		return false
	}
	observed := map[string]bool{}
	for _, container := range containers {
		if !seen[container.ID] || observed[container.ID] || container.State.Running == nil || container.State.PID == nil || container.Mounts == nil {
			return false
		}
		observed[container.ID] = true
		if (*container.State.Running || *container.State.PID != 0) && !container.mountsOutside(sources) {
			return false
		}
	}
	return true
}

type inspectedContainer struct {
	ID    string `json:"Id"`
	State struct {
		Running *bool `json:"Running"`
		PID     *int  `json:"Pid"`
	} `json:"State"`
	Mounts []struct {
		Source string `json:"Source"`
	} `json:"Mounts"`
}

func (c inspectedContainer) mountsOutside(sources []string) bool {
	for _, mount := range c.Mounts {
		if !filepath.IsAbs(mount.Source) || filepath.Clean(mount.Source) != mount.Source {
			return false
		}
		for _, source := range sources {
			if data.OverlappingPaths(mount.Source, source) {
				return false
			}
		}
	}
	return true
}
func (h archiveHost) StopReplicas(ctx context.Context, p archive.Plan) error {
	if p.Removal != nil {
		return removalReplicaObservation(ctx, h, p)
	}
	if nativeArchiveCandidate(p.Candidate) {
		if stopped, err := h.nativeArchiveStopped(ctx, p); err != nil || !stopped {
			return archive.ErrRefused
		}
		return nil
	}
	for _, variant := range p.Candidate.Databases {
		if err := h.stopSQLiteArchiveReplicas(ctx, variant); err != nil {
			return err
		}
	}
	return nil
}
func (h archiveHost) ReplicasStopped(ctx context.Context, p archive.Plan) (bool, error) {
	if nativeArchiveCandidate(p.Candidate) {
		return h.nativeArchiveStopped(ctx, p)
	}
	for _, variant := range p.Candidate.Databases {
		database, sqlite := variant.SQLite()
		if !sqlite {
			return false, archive.ErrRefused
		}
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
	if nativeArchiveCandidate(p.Candidate) {
		return h.nativeArchiveLifetimeLocks(ctx, p)
	}
	releases := []func() error{}
	release := func() error {
		var errs []error
		for i := len(releases) - 1; i >= 0; i-- {
			errs = append(errs, releases[i]())
		}
		return errors.Join(errs...)
	}
	seen := map[data.ReplicaBindingID]bool{}
	for _, variant := range p.Candidate.Databases {
		database, sqlite := variant.SQLite()
		if !sqlite {
			return nil, errors.Join(archive.ErrRefused, release())
		}
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

func (h archiveHost) stopSQLiteArchiveReplicas(ctx context.Context, variant archive.Database) error {
	database, ok := variant.SQLite()
	if !ok {
		return archive.ErrRefused
	}
	for _, binding := range database.Epochs {
		name, err := replication.ServiceName(string(binding.BindingID))
		if err != nil {
			return err
		}
		if err = h.stopService(ctx, name, !binding.Committed); err != nil {
			return err
		}
	}
	return nil
}
