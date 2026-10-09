package enroll

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"os"
)

func sshCandidate(main []byte, policy string) []byte {
	return append([]byte("Include "+policy+"\n"), main...)
}
func (h *host) validateSSH(ctx context.Context, path string) error {
	sources, err := (Prober{FS: inventory.HostFS{}}).sshAuditedSources(ctx, path)
	if err != nil {
		return err
	}
	if _, err := h.run(ctx, false, "/usr/sbin/sshd", "-t", "-f", path); err != nil {
		return err
	}
	for _, connection := range sshConnections {
		r, err := h.run(ctx, false, "/usr/sbin/sshd", "-ddd", "-T", "-f", path, "-C", connection)
		if err != nil {
			return err
		}
		if r.Truncated {
			return errors.New("SSH settings truncated")
		}
		if err := checkSSHSourceTrace(sources, r.Stderr); err != nil {
			return err
		}
		if err := checkForcedSSH(r.Stdout); err != nil {
			return err
		}
	}
	return nil
}
func promoteSSH(ctx context.Context, validate func(context.Context) error, promote func() error, reload func(context.Context) error, restore func() error, checkpoint func(bool) error) error {
	if err := validate(ctx); err != nil {
		return err
	}
	if err := checkpoint(true); err != nil {
		return err
	}
	err := promote()
	if err == nil {
		err = reload(ctx)
	}
	if err != nil {
		var commandErr *localexec.Error
		if errors.As(err, &commandErr) && commandErr.Kind == localexec.UnknownOutcome {
			return err
		}
		if e := restore(); e != nil {
			return errors.Join(err, e)
		}
		if e := reload(ctx); e != nil {
			return errors.Join(err, e)
		}
		return errors.Join(err, checkpoint(false))
	}
	return checkpoint(false)
}
func (h *host) sshPending(pending bool) error {
	h.r.SSHReloadPending = pending
	return h.Save(h.r.Journal)
}
func (h *host) reloadSSH(ctx context.Context) error {
	if _, err := h.run(ctx, false, "/usr/sbin/sshd", "-t"); err != nil {
		return err
	}
	if _, err := h.run(ctx, false, "systemctl", "is-active", "ssh.service"); err != nil {
		return err
	}
	_, err := h.run(ctx, true, "systemctl", "reload", "ssh.service")
	return err
}
func (h *host) cleanSSHCandidates() error {
	for _, p := range []string{sshMainCandidate, sshPolicyCandidate} {
		if err := h.restoreFile(p); err != nil {
			return err
		}
		delete(h.r.Files, p)
	}
	return h.Save(h.r.Journal)
}
func (h *host) installSSH(ctx context.Context) error {
	if h.r.SSHReloadPending {
		return errors.New("SSH reload outcome unknown; inspect and explicitly undo to reconcile")
	}
	if err := (Prober{FS: inventory.HostFS{}}).sshConfig(ctx, "/etc/ssh/sshd_config", nil, 0); err != nil {
		return err
	}
	main, err := boundedRead("/etc/ssh/sshd_config")
	if err != nil {
		return err
	}
	if err = h.file(ctx, sshPolicyCandidate, []byte(sshPolicy), 0644, false); err != nil {
		return err
	}
	if err = h.file(ctx, sshMainCandidate, sshCandidate(main, sshPolicyCandidate), 0644, false); err != nil {
		return err
	}
	err = promoteSSH(ctx, func(ctx context.Context) error { return h.validateSSH(ctx, sshMainCandidate) }, func() error { return h.file(ctx, sshPolicyPath, []byte(sshPolicy), 0644, false) }, h.reloadSSH, func() error { return h.restoreFile(sshPolicyPath) }, h.sshPending)
	if err != nil {
		return err
	}
	return h.cleanSSHCandidates()
}
func (h *host) checkSSH(ctx context.Context) (bool, error) {
	if h.r.SSHReloadPending {
		return false, errors.New("SSH reload outcome unknown; inspect and explicitly undo to reconcile")
	}
	ok, err := h.fileMatches(sshPolicyPath, hash([]byte(sshPolicy)))
	if err != nil || !ok {
		return ok, err
	}
	if err := h.checkAuthorizedKeyPaths(ctx); err != nil {
		return false, err
	}
	if err := h.validateSSH(ctx, "/etc/ssh/sshd_config"); err != nil {
		return false, err
	}
	return true, nil
}
func (h *host) undoSSH(ctx context.Context) error {
	if err := h.sshPending(true); err != nil {
		return err
	}
	if err := h.restoreFile(sshPolicyPath); err != nil {
		return err
	}
	if err := h.reloadSSH(ctx); err != nil {
		return err
	}
	if err := h.sshPending(false); err != nil {
		return err
	}
	return h.cleanSSHCandidates()
}
func (h *host) sshLayout(context.Context) error {
	for _, p := range []string{sshDir, sshKeyDir} {
		if err := h.dir(p, 0755, 0, 0); err != nil {
			return err
		}
	}
	return nil
}
func (h *host) checkSSHLayout(context.Context) (bool, error) {
	for _, p := range []string{sshDir, sshKeyDir} {
		d, ok := h.r.Dirs[p]
		if !ok {
			return false, nil
		}
		ok, err := h.checkDirectory(p, d)
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}
func (h *host) removeSSHLayout(context.Context) error {
	for _, p := range []string{sshKeyDir, sshDir} {
		d, ok := h.r.Dirs[p]
		if !ok {
			continue
		}
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			continue
		}
		ok, err := h.checkDirectory(p, d)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("SSH directory incomplete")
		}
		if p == sshDir && h.r.RetainedPolicy {
			entries, err := os.ReadDir(p)
			if err != nil {
				return err
			}
			if len(entries) != 1 || entries[0].Name() != "operator-policy.toml" {
				return errors.New("unexpected files beside retained operator policy")
			}
			if _, err := (diskOperatorPolicy{h}).Read(); err != nil {
				return err
			}
			continue
		}
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}

var plantedSSH = []string{home + "/.ssh/authorized_keys", home + "/.ssh/authorized_keys2", home + "/.ssh/environment"}

func (h *host) removeBypassFiles() error {
	for _, p := range append([]string{home + "/.bashrc"}, plantedSSH...) {
		if err := h.restoreFile(p); err != nil {
			return err
		}
		delete(h.r.Files, p)
	}
	return h.Save(h.r.Journal)
}

func (h *host) verifySSHSourceManifest(ctx context.Context, path string) error {
	sources, err := (Prober{FS: inventory.HostFS{}}).sshAuditedSources(ctx, path)
	if err != nil {
		return err
	}
	r, err := h.run(ctx, false, "/usr/sbin/sshd", "-ddd", "-T", "-f", path, "-C", sshConnections[0])
	if err != nil {
		return err
	}
	if r.Truncated {
		return errors.New("SSH source trace truncated")
	}
	return checkSSHSourceTrace(sources, r.Stderr)
}
