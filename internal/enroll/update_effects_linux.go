//go:build linux

package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/workspaceobserve"
)

func (h *host) updateCommand(ctx context.Context, mutation bool, path string, args ...string) (string, error) {
	out, err := h.run(ctx, mutation, path, args...)
	if err != nil || out.Truncated || out.ExitCode != 0 {
		return "", errors.Join(errors.New("update command outcome unavailable"), err)
	}
	return out.Stdout, nil
}
func (h *host) fenceRunner(ctx context.Context) error {
	u := h.r.Update
	if u.Fence == nil {
		fence, err := h.observeRunnerFence(ctx)
		if err != nil {
			return err
		}
		u.Fence = &fence
		if err := h.updateSave(); err != nil {
			return err
		}
	}
	f := u.Fence
	f.ExpireIntent = true
	if err := h.updateSave(); err != nil {
		return err
	}
	shadow, err := h.updateCommand(ctx, false, "getent", "shadow", "brine")
	if err != nil {
		return err
	}
	if !proofExpiry(shadow, 1) {
		if _, err := h.updateCommand(ctx, true, "usermod", "--expiredate", "1", "brine"); err != nil {
			return err
		}
	}
	f.LingerIntent = true
	if err := h.updateSave(); err != nil {
		return err
	}
	if _, err := h.updateCommand(ctx, true, "loginctl", "disable-linger", "brine"); err != nil {
		return err
	}
	f.StopIntent = true
	if err := h.updateSave(); err != nil {
		return err
	}
	if _, err := h.updateCommand(ctx, true, "systemctl", "stop", h.fenceUnit()); err != nil {
		return err
	}
	if _, err := h.updateCommand(ctx, true, "loginctl", "terminate-user", "brine"); err != nil {
		return err
	}
	return h.updateProof(ctx)
}
func (h *host) observeRunnerFence(ctx context.Context) (updateRunnerFence, error) {
	fs := h.updateEnv.fs
	p := AdmissionProofs{Root: fs.root, ProcRoot: fs.path("/proc"), Executor: h.exec}
	if err := p.sshPAM(ctx); err != nil {
		return updateRunnerFence{}, errors.New("runner_fence_unsupported")
	}
	pam, err := proofRead(ctx, fs.path("/etc/pam.d/runuser"), 16384)
	if err != nil || !proofRunuserPAM(pam) {
		return updateRunnerFence{}, errors.New("runner_fence_unsupported")
	}
	shadow, err := h.updateCommand(ctx, false, "getent", "shadow", "brine")
	if err != nil {
		return updateRunnerFence{}, err
	}
	fields := strings.Split(strings.TrimSuffix(shadow, "\n"), ":")
	if len(fields) != 9 || fields[0] != "brine" {
		return updateRunnerFence{}, errors.New("runner identity drift")
	}
	if fields[7] != "" {
		if _, err := strconv.ParseInt(fields[7], 10, 64); err != nil {
			return updateRunnerFence{}, errors.New("invalid original account expiry")
		}
	}
	active, err := h.updateCommand(ctx, false, "systemctl", "show", h.fenceUnit(), "--property=ActiveState", "--value")
	if err != nil || active != "active\n" && active != "inactive\n" {
		return updateRunnerFence{}, errors.Join(errors.New("runner manager unknown"), err)
	}
	linger, err := os.Lstat(fs.path("/var/lib/systemd/linger/brine"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return updateRunnerFence{}, err
	}
	if err == nil && !linger.Mode().IsRegular() {
		return updateRunnerFence{}, errors.New("linger drift")
	}
	return updateRunnerFence{Expiry: fields[7], Linger: err == nil, ManagerActive: active == "active\n"}, nil
}

func nativeProperties(raw string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil
		}
		if _, exists := values[key]; exists {
			return nil
		}
		values[key] = value
	}
	return values
}
func (h *host) nativeUnit(ctx context.Context, unit string) (map[string]string, error) {
	raw, err := h.updateCommand(ctx, false, "systemctl", "show", unit, "--property=LoadState,ActiveState,SubState,Result,ExecMainStatus,InvocationID,MainPID,Job")
	if err != nil {
		return nil, err
	}
	properties := nativeProperties(raw)
	if len(properties) != 8 {
		return nil, errors.New("native unit readback incomplete")
	}
	return properties, nil
}
func nativeEffectDone(p map[string]string) bool {
	return p["LoadState"] == "loaded" && p["InvocationID"] != "" && p["Result"] == "success" && p["ExecMainStatus"] == "0" && p["MainPID"] == "0" && (p["ActiveState"] == "active" && p["SubState"] == "exited" || p["ActiveState"] == "inactive" && p["SubState"] == "dead") && (p["Job"] == "" || p["Job"] == "0")
}
func (h *host) effectName(label string) string {
	suffix := "forward"
	if h.r.Update.Aborting {
		suffix = "abort"
	}
	return "brine-update-" + h.r.Update.ID + "-" + suffix + "-" + label + ".service"
}

// Native issuer identity is fixed by this transaction. Intent is saved before
// delivery. Missing or unfinished readback never causes a second delivery.
func (h *host) issueUpdateEffect(ctx context.Context, e *updateEffect, label, path string, args ...string) error {
	if e.Done {
		return nil
	}
	unit := h.effectName(label)
	if !e.Intent {
		e.Intent = true
		if err := h.updateSave(); err != nil {
			return err
		}
		argv := []string{"--no-block", "--unit", unit, "--property=Type=exec", "--property=RemainAfterExit=yes", "--property=RuntimeMaxSec=60s", "--property=KillMode=control-group", "--property=Restart=no", "--", path}
		_, _ = h.run(ctx, true, "systemd-run", append(argv, args...)...)
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		p, err := h.nativeUnit(wait, unit)
		if err == nil && nativeEffectDone(p) {
			e.Done = true
			return h.updateSave()
		}
		if err != nil || p["LoadState"] == "not-found" || p["ActiveState"] == "failed" {
			return errors.Join(errors.New("activation_pending"), err)
		}
		select {
		case <-wait.Done():
			return errors.Join(errors.New("activation_pending"), wait.Err())
		case <-ticker.C:
		}
	}
}
func (h *host) activateUpdate(ctx context.Context, a *updateActivation, abort bool) error {
	plan := h.r.Update.Plan
	if plan.DaemonReload {
		if err := h.issueUpdateEffect(ctx, &a.Reload, "daemon-reload", "/usr/bin/systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	if plan.SettleEnable && !abort {
		if err := h.issueUpdateEffect(ctx, &a.Enable, "settle-enable", "/usr/bin/systemctl", "enable", "brine-admission-settle.service"); err != nil {
			return err
		}
	}
	if plan.ObserverRestart {
		if err := h.activateUpdateObserver(ctx, &a.Observer, abort); err != nil {
			return err
		}
	}
	// A stopped runner has no user bus. Its reload obligation is fulfilled after
	// admission opens and the recorded manager has been restored.
	if plan.UserDaemonReload && plan.Quiescence != admission.Runner {
		return h.userUpdateEffect(ctx, &a.UserReload, "user-reload", "daemon-reload")
	}
	return nil
}
func (h *host) activateUpdateObserver(ctx context.Context, e *updateEffect, abort bool) error {
	if !e.Intent {
		p, err := h.nativeUnit(ctx, workspaceobserve.UnitName)
		if err != nil {
			return err
		}
		e.Before = p["InvocationID"]
	}
	if err := h.issueUpdateEffect(ctx, e, "observer", "/usr/bin/systemctl", "restart", workspaceobserve.UnitName); err != nil {
		return err
	}
	p, err := h.nativeUnit(ctx, workspaceobserve.UnitName)
	if err != nil || p["ActiveState"] != "active" || p["InvocationID"] == "" || p["InvocationID"] == e.Before {
		return errors.Join(errors.New("observer_activation_unverified"), err)
	}
	pid, err := strconv.Atoi(p["MainPID"])
	if err != nil || pid <= 0 {
		return errors.New("observer_activation_unverified")
	}
	info, err := os.Stat(h.updateEnv.fs.path(filepath.Join("/proc", strconv.Itoa(pid), "exe")))
	if err != nil {
		return err
	}
	binary, err := os.Stat(h.updateEnv.fs.path(binaryPath))
	if err != nil || !os.SameFile(info, binary) {
		return errors.Join(errors.New("observer_binary_drift"), err)
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unixpacket", h.updateEnv.fs.path(workspaceobserve.SocketPath))
	if err != nil {
		return errors.Join(errors.New("observer_listener_unavailable"), err)
	}
	return conn.Close()
}
func (h *host) userUpdateEffect(ctx context.Context, e *updateEffect, label, verb string, extra ...string) error {
	uid := strconv.Itoa(h.r.UID)
	args := []string{"-u", "brine", "--", "/usr/bin/env", "XDG_RUNTIME_DIR=/run/user/" + uid, "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/" + uid + "/bus", "/usr/bin/systemctl", "--user"}
	if label == "reconcile" {
		args = append(args, "--no-block")
	}
	args = append(args, verb)
	return h.issueUpdateEffect(ctx, e, label, "/usr/sbin/runuser", append(args, extra...)...)
}
func (h *host) verifyCandidateVersion(ctx context.Context) error {
	out, err := h.updateCommand(ctx, false, "runuser", "-u", "brine", "--", binaryPath, "version", "--json")
	if err != nil {
		return err
	}
	var response struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil || response.Data.Version != h.r.Update.Plan.CandidateVersion {
		return errors.New("candidate_version_drift")
	}
	return nil
}

func (h *host) settleActivationBeforeAbort(ctx context.Context) error {
	u := h.r.Update
	for label, e := range map[string]*updateEffect{"daemon-reload": &u.Activation.Reload, "settle-enable": &u.Activation.Enable, "observer": &u.Activation.Observer, "user-reload": &u.Activation.UserReload} {
		if !e.Intent || e.Done {
			continue
		}
		unit := "brine-update-" + u.ID + "-forward-" + label + ".service"
		p, err := h.nativeUnit(ctx, unit)
		if err != nil || !nativeEffectDone(p) {
			return errors.Join(errors.New("activation_pending: abort waits for native completion"), err)
		}
		e.Done = true
		if err := h.updateSave(); err != nil {
			return err
		}
	}
	return nil
}

func (h *host) disableAddedSettle(ctx context.Context) error {
	u := h.r.Update
	if !u.Plan.SettleEnable || !u.Activation.Enable.Done {
		return nil
	}
	effect := &u.RollbackActivation.Enable
	if !effect.Intent {
		current, _, err := h.updateEnv.fs.read(settleUnitPath)
		if err != nil || !current.Present || current.UID != h.updateEnv.fs.owner || current.Mode != 0644 || current.SHA256 != hash([]byte(settleUnit)) {
			return errors.Join(errors.New("update_drift: added settle unit"), err)
		}
	}
	return h.issueUpdateEffect(ctx, effect, "settle-enable", "/usr/bin/systemctl", "disable", "brine-admission-settle.service")
}
