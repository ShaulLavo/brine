package enroll

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func (h *host) masked(ctx context.Context) (bool, error) {
	if len(h.r.Packages) == 0 || h.r.Journal.Intents["unmask"] {
		return true, nil
	}
	state, err := h.observeService(ctx, "is-enabled")
	return err == nil && state.masked(), err
}
func (h *host) mask(ctx context.Context) error {
	if len(h.r.Packages) == 0 {
		return nil
	}
	_, err := h.run(ctx, true, "systemctl", "mask", "caddy.service")
	return err
}
func (h *host) unmask(ctx context.Context) error {
	if len(h.r.Packages) == 0 {
		return nil
	}
	_, err := h.run(ctx, true, "systemctl", "unmask", "caddy.service")
	return err
}
func (h *host) packagesInstalled(ctx context.Context) (bool, error) {
	for _, p := range h.r.Packages {
		v, err := installed(ctx, probeRunner{h.exec}, p.Name)
		if err != nil {
			return false, err
		}
		if v == "" {
			return false, nil
		}
		if v != p.Version {
			return false, errors.New("package version drift")
		}
	}
	return true, nil
}
func (h *host) installPackages(ctx context.Context) error {
	args := []string{"--simulate", "--no-remove", "--no-install-recommends", "install"}
	for _, p := range h.r.Packages {
		args = append(args, p.Name+"="+p.Version)
	}
	r, err := h.run(ctx, false, "apt-get", args...)
	if err != nil {
		return err
	}
	next, err := aptInstalls(r.Stdout)
	if err != nil {
		return err
	}
	for _, p := range next {
		known := false
		for _, old := range h.r.Packages {
			if p == old {
				known = true
			}
		}
		if !known {
			return errors.New("apt transaction changed since inventory")
		}
	}
	args[0] = "--yes"
	_, err = h.run(ctx, true, "apt-get", args...)
	return err
}
func (h *host) removePackages(ctx context.Context) error {
	if len(h.r.Packages) == 0 {
		return nil
	}
	args := []string{"--simulate", "remove"}
	for _, p := range h.r.Packages {
		v, err := installed(ctx, probeRunner{h.exec}, p.Name)
		if err != nil {
			return err
		}
		if v != "" && v != p.Version {
			return errors.New("undo refused: installed package version changed")
		}
		if v != "" {
			verified, e := h.run(ctx, false, "dpkg", "--verify", p.Name)
			if e != nil || strings.TrimSpace(verified.Stdout) != "" {
				return errors.New("undo refused: installed package files changed")
			}
			args = append(args, p.Name)
		}
	}
	if len(args) == 2 {
		return nil
	}
	r, err := h.run(ctx, false, "apt-get", args...)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(r.Stdout, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if f[0] == "Inst" {
			return errors.New("undo package transaction installs unrelated packages")
		}
		if f[0] != "Remv" {
			continue
		}
		if len(f) < 2 {
			return errors.New("invalid undo package transaction")
		}
		allowed := false
		for _, p := range h.r.Packages {
			if p.Name == f[1] {
				allowed = true
			}
		}
		if !allowed {
			return errors.New("undo would remove a preexisting package")
		}
	}
	args[0] = "--yes"
	_, err = h.run(ctx, true, "apt-get", args...)
	return err
}
func (h *host) caddyWritable(context.Context) (bool, error) {
	s, err := os.Lstat("/etc/caddy/brine")
	if err != nil {
		return false, err
	}
	st := s.Sys().(*syscall.Stat_t)
	if st.Uid != 0 || st.Ino != h.r.Dirs["/etc/caddy/brine"].Inode {
		return false, errors.New("Caddy directory identity drift")
	}
	return int(st.Gid) == h.r.GID && s.Mode().Perm() == 0775, nil
}
func (h *host) allowCaddyWrites(context.Context) error {
	p := "/etc/caddy/brine"
	s, err := os.Lstat(p)
	if err != nil {
		return err
	}
	d := h.r.Dirs[p]
	if s.Sys().(*syscall.Stat_t).Ino != d.Inode {
		return errors.New("Caddy directory identity drift")
	}
	if err = os.Chown(p, 0, h.r.GID); err != nil {
		return err
	}
	return os.Chmod(p, 0775)
}
func (h *host) protectCaddyTree(context.Context) error {
	p := "/etc/caddy/brine"
	s, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	d := h.r.Dirs[p]
	if !s.IsDir() || s.Sys().(*syscall.Stat_t).Ino != d.Inode || s.Sys().(*syscall.Stat_t).Uid != 0 {
		return errors.New("Caddy directory identity drift")
	}
	if err = os.Chown(p, 0, 0); err != nil {
		return err
	}
	return os.Chmod(p, 0755)
}
func (h *host) undoPreflight() error {
	if err := h.reconcilePendingDirectories(); err != nil {
		return err
	}
	if err := h.checkUndoHome(); err != nil {
		return err
	}
	if err := verifyUndoFiles(h.r.Files, boundedRead, (diskOperatorPolicy{h}).Read); err != nil {
		return err
	}

	if _, ok := h.r.Dirs["/etc/caddy/brine"]; ok {
		link, err := os.Readlink("/etc/caddy/brine/current")
		if err == nil && link != "gen-0" {
			return errors.New("undo refused: Caddy generation changed")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, p := range []string{"/etc/caddy/brine", "/etc/caddy/brine/gen-0"} {
			entries, err := os.ReadDir(p)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if p == "/etc/caddy/brine" && (entry.Name() == "gen-0" || entry.Name() == "current") {
					continue
				}
				return errors.New("undo refused: Caddy tree contains unowned data")
			}
		}
	}
	return nil
}

func (h *host) checkUndoHome() error {
	if _, err := os.Lstat(home); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if _, ok := h.r.Dirs[home]; !ok {
		return nil
	}
	allowed := map[string]bool{home: true, home + "/.ssh": true, home + "/.config": true, home + "/.cache": true, home + "/.local": true, home + "/.local/state": true, home + "/.local/state/brine": true}
	root, e := os.OpenRoot(home)
	if e == nil {
		err := h.checkRuntime(root)
		root.Close()
		if err != nil {
			return err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	for p := range h.runtimeHomeAllowed() {
		allowed[p] = true
	}
	for p := range h.r.Files {
		if strings.HasPrefix(p, home+"/") {
			allowed[p] = true
		}
	}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !allowed[path] || d.Type()&os.ModeSymlink != 0 {
			return errors.New("undo refused: runner home contains unowned data")
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (h *host) checkRecordedTransaction(ctx context.Context, f Facts) error {
	missing := []Package{}
	for _, pkg := range h.r.Packages {
		version, err := installed(ctx, probeRunner{h.exec}, pkg.Name)
		if err != nil {
			return err
		}
		if version == "" {
			missing = append(missing, pkg)
		} else if version != pkg.Version {
			return errors.New("recorded package version changed; finish undo before changing enrollment")
		}
	}
	if len(missing) != len(f.PackageInstall) {
		return errors.New("confirmed transaction differs from unfinished package intent")
	}
	versions := map[string]string{}
	for _, pkg := range missing {
		versions[pkg.Name] = pkg.Version
	}
	for _, pkg := range f.PackageInstall {
		if versions[pkg.Name] != pkg.Version {
			return errors.New("confirmed transaction differs from unfinished package intent")
		}
		delete(versions, pkg.Name)
	}
	return nil
}

func verifyUndoFiles(files map[string]ownedFile, read func(string) ([]byte, error), policyRead func() ([]byte, error)) error {
	for p, old := range files {
		if p == operatorPolicyPath {
			_, err := policyRead()
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		data, err := read(p)
		if errors.Is(err, os.ErrNotExist) && !old.Existed {
			continue
		}
		if err != nil {
			return err
		}
		if hash(data) != old.Hash && (!old.Existed || hash(data) != hash(old.Before)) {
			return errors.New("undo refused: recorded file hash changed")
		}
	}
	return nil
}
