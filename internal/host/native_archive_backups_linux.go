//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type nativeArchiveBackups struct {
	home    string
	session localexec.Session
	units   [2]target.Unit
}

func (h archiveHost) nativeArchiveBackups(ctx context.Context, p archive.Plan) (nativeArchiveBackups, error) {
	d, ok := p.Candidate.Databases[0].Native()
	if !ok {
		return nativeArchiveBackups{}, archive.ErrRefused
	}
	backup, err := h.service.Store.ReadNativeBackupPolicy(ctx, d.Binding.BindingID)
	if err != nil {
		return nativeArchiveBackups{}, err
	}
	return newNativeArchiveBackups(h.home, h.session, backup)
}

func newNativeArchiveBackups(home string, session localexec.Session, backup store.NativeBackupPolicy) (nativeArchiveBackups, error) {
	metadata := store.NativeBackupActivationMetadata{Plan: store.NativeBackupRevisionPlan{BindingID: backup.BindingID}}
	if !data.ValidID(string(backup.BindingID)) || !data.ValidCatalogHash(backup.UnitSHA256) || !data.ValidCatalogHash(backup.TimerSHA256) {
		return nativeArchiveBackups{}, archive.ErrRefused
	}
	name := nativeBackupUnitName(metadata)
	return nativeArchiveBackups{home: home, session: session, units: [2]target.Unit{{Name: name + ".timer", Hash: backup.TimerSHA256}, {Name: name + ".service", Hash: backup.UnitSHA256}}}, nil
}

func (b nativeArchiveBackups) inspect(ctx context.Context) (bool, error) {
	units, err := openManagedUnits(b.home)
	if err != nil {
		return false, err
	}
	defer func() { _ = units.Close() }()
	present := false
	for _, unit := range b.units {
		raw, exists, err := units.read(ctx, filepath.Join(managedServiceDirectory, unit.Name))
		if err != nil || exists && managedServiceHash(raw) != unit.Hash {
			return false, archive.ErrRefused
		}
		fields, err := b.manager(ctx, unit.Name)
		if err != nil {
			return false, err
		}
		present = present || exists || fields["LoadState"] == "loaded"
	}
	link, err := b.enabledLink(units)
	if err != nil || present || link {
		return present || link, err
	}
	return false, b.stopped(ctx)
}

func (b nativeArchiveBackups) enabledLink(units *managedUnits) (bool, error) {
	parent := filepath.Join(managedServiceDirectory, "timers.target.wants")
	if _, err := units.root.Lstat(parent); os.IsNotExist(err) {
		return false, nil
	}
	if err := units.directoryEntry(parent, false, false); err != nil {
		return false, err
	}
	link := filepath.Join(parent, b.units[0].Name)
	if _, err := units.root.Lstat(link); os.IsNotExist(err) {
		return false, nil
	}
	destination, err := units.root.Readlink(link)
	expected := filepath.Join(b.home, managedServiceDirectory, b.units[0].Name)
	if err != nil || destination != expected && destination != "../"+b.units[0].Name {
		return false, archive.ErrRefused
	}
	return true, nil
}

func (b nativeArchiveBackups) manager(ctx context.Context, name string) (map[string]string, error) {
	args := []string{"--user", "show", name, "--property=LoadState,ActiveState,FragmentPath,DropInPaths,Job"}
	observed, err := b.session.Execute(ctx, "/usr/bin/systemctl", args, nil, false)
	if err != nil || observed.ExitCode != 0 || observed.Truncated {
		return nil, archive.ErrRecovery
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(observed.Stdout), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, archive.ErrRefused
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, archive.ErrRefused
		}
		switch key {
		case "LoadState", "ActiveState", "FragmentPath", "DropInPaths", "Job":
		default:
			return nil, archive.ErrRefused
		}
		fields[key] = value
	}
	if len(fields) != 5 || fields["DropInPaths"] != "" {
		return nil, archive.ErrRefused
	}
	loaded := fields["LoadState"] == "loaded" && fields["FragmentPath"] == filepath.Join(b.home, managedServiceDirectory, name)
	absent := fields["LoadState"] == "not-found" && fields["FragmentPath"] == "" && fields["Job"] == "" && (fields["ActiveState"] == "inactive" || fields["ActiveState"] == "failed")
	if !loaded && !absent {
		return nil, archive.ErrRefused
	}
	return fields, nil
}

func (b nativeArchiveBackups) retire(ctx context.Context) error {
	if _, err := b.inspect(ctx); err != nil {
		return err
	}
	for _, unit := range b.units {
		if err := b.stop(ctx, unit.Name); err != nil {
			return err
		}
	}
	if err := b.stopped(ctx); err != nil {
		return err
	}
	units, err := openManagedUnits(b.home)
	if err != nil {
		return err
	}
	defer func() { _ = units.Close() }()
	linked, err := b.enabledLink(units)
	if err != nil {
		return err
	}
	if linked {
		parent := filepath.Join(managedServiceDirectory, "timers.target.wants")
		if units.root.Remove(filepath.Join(parent, b.units[0].Name)) != nil {
			return archive.ErrRecovery
		}
	}
	parent := filepath.Join(managedServiceDirectory, "timers.target.wants")
	if _, err := units.root.Lstat(parent); err == nil && units.syncDirectory(parent) != nil {
		return archive.ErrRecovery
	}
	for _, unit := range b.units {
		path := filepath.Join(managedServiceDirectory, unit.Name)
		raw, present, err := units.read(ctx, path)
		if err != nil || present && managedServiceHash(raw) != unit.Hash {
			return archive.ErrRefused
		}
		if present && units.root.Remove(path) != nil {
			return archive.ErrRecovery
		}
	}
	if units.syncDirectory(managedServiceDirectory) != nil || b.command(ctx, "daemon-reload") != nil {
		return archive.ErrRecovery
	}
	present, err := b.inspect(ctx)
	if err != nil || present {
		return archive.ErrRecovery
	}
	return nil
}

func (b nativeArchiveBackups) stop(ctx context.Context, name string) error {
	fields, err := b.manager(ctx, name)
	if err != nil || fields["LoadState"] == "not-found" {
		return err
	}
	return b.command(ctx, "stop", name)
}

func (b nativeArchiveBackups) stopped(ctx context.Context) error {
	fields, err := b.manager(ctx, b.units[0].Name)
	if err != nil || fields["Job"] != "" || fields["ActiveState"] != "inactive" && fields["ActiveState"] != "failed" {
		return archive.ErrRecovery
	}
	h := archiveHost{session: b.session}
	stopped, err := h.stoppedOrAbsentService(ctx, b.units[1].Name)
	if err != nil || !stopped {
		return archive.ErrRecovery
	}
	return nil
}

func (b nativeArchiveBackups) command(ctx context.Context, args ...string) error {
	observed, err := b.session.Execute(ctx, "/usr/bin/systemctl", append([]string{"--user"}, args...), nil, true)
	if err != nil || observed.ExitCode != 0 || observed.Truncated {
		return archive.ErrRecovery
	}
	return nil
}

func (h archiveHost) RetireArchivedBackups(ctx context.Context, p archive.Plan) error {
	if !nativeArchiveCandidate(p.Candidate) {
		return nil
	}
	disposition, exists, err := h.service.Store.ReadArchiveDisposition(ctx, p.ArchiveID)
	if err != nil || !exists || disposition.Stage != archive.Retired {
		return archive.ErrRefused
	}
	backups, err := h.nativeArchiveBackups(ctx, p)
	if err != nil {
		return err
	}
	return backups.retire(ctx)
}
