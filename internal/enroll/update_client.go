package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/transport"
)

func validateUpdateOptions(o Options) error {
	count := 0
	for _, enabled := range []bool{o.Undo, o.Finish, o.Update, o.UpdateAbort} {
		if enabled {
			count++
		}
	}
	if count > 1 {
		return errors.New("--undo, --finish, --update and --update-abort are mutually exclusive")
	}
	if o.Quiesce != "" && o.Quiesce != "store" && o.Quiesce != "runner" {
		return errors.New("--quiesce must be store or runner")
	}
	if !o.Update && !o.UpdateAbort && (o.ReplaceUnrecordedBinary != "" || o.Quiesce == "runner") {
		return errors.New("update flags require --update or --update-abort")
	}
	return nil
}

func (c Client) runUpdate(ctx context.Context, o Options, key []byte, remote []string) error {
	target, err := transport.LoadTarget(filepath.Join(o.ConfigDir, o.Name+".json"))
	if err != nil {
		return errors.New("update requires the finished private target config")
	}
	host := strings.Split(o.Destination, "@")
	if target.Name != o.Name || target.Destination != "brine@"+host[len(host)-1] {
		return errors.New("update target differs from pinned destination")
	}
	request := HostRequest{Action: "update-plan", IdentityKey: key, HostKey: target.PinnedHostKey, Quiesce: o.Quiesce, ReplaceUnrecordedBinary: o.ReplaceUnrecordedBinary}
	call := func() ([]byte, error) {
		data, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		return c.exec(ctx, "ssh", remote, data, 15*time.Minute)
	}
	data, err := call()
	if err != nil {
		return err
	}
	var plan UpdatePlan
	if err := json.Unmarshal(data, &plan); err != nil || plan.Digest == "" {
		return errors.New("invalid update plan")
	}
	if err := c.writeUpdateReview(o, plan); err != nil {
		return err
	}
	if err := Confirm(c.Input, c.Terminal, o.NoInput, o.Name); err != nil {
		return err
	}
	request.Action, request.Confirmed = "update-apply", plan.Digest
	if o.UpdateAbort {
		request.Action = "update-abort"
	}
	data, err = call()
	if err != nil {
		return err
	}
	var result UpdateResult
	if err := json.Unmarshal(data, &result); err != nil || !result.Committed && !result.Aborted {
		return errors.New("invalid update completion")
	}
	if result.Aborted {
		_, err := fmt.Fprintf(c.Output, "Update aborted. Previous control bytes restored. Retained recovery staging: %s. Retained quarantine: %s\n", result.Retained, result.Quarantine)
		return err
	}
	if err := c.verifyUsedHost(ctx, o.ConfigDir, target); err != nil {
		return fmt.Errorf("update_committed_verification_failed: %w", err)
	}
	_, err = fmt.Fprintf(c.Output, "Update committed and restricted deploy connection verified. Retained recovery files: %s\n", result.Retained)
	return err
}

func (c Client) verifyUsedHost(ctx context.Context, dir string, target transport.Target) error {
	tc := transport.Client{Runner: c.Runner, KnownHostsDir: filepath.Join(dir, "pins")}
	req := dispatch.Request{SchemaVersion: 1, Op: "ping", RequestID: "update-verification", Args: json.RawMessage(`{}`)}
	if _, err := tc.Call(ctx, target, req); err != nil {
		return err
	}
	if _, err := tc.VerifyRestriction(ctx, target, req); err != nil {
		return err
	}
	req.Op = "inventory"
	_, err := tc.Call(ctx, target, req)
	return err
}

func (c Client) writeUpdateReview(o Options, plan UpdatePlan) error {
	lines := []string{fmt.Sprintf("Update %s to %s. Candidate SHA256 %s.", o.Name, plan.CandidateVersion, plan.CandidateSHA256), fmt.Sprintf("Control schema %d to %d. Quiescence %s.", plan.SchemaFrom, plan.SchemaTo, plan.Quiescence)}
	lines = append(lines, "Recorded binary SHA256 "+plan.RecordedBinarySHA256, fmt.Sprintf("System daemon reload %t. Observer restart %t. User daemon reload %t. Admission-settle enable %t.", plan.DaemonReload, plan.ObserverRestart, plan.UserDaemonReload, plan.SettleEnable))
	if plan.ControlAbsent {
		lines = append(lines, "The control store is absent. The first ordinary open creates the candidate schema.")
	}
	if plan.Quiescence == "runner" {
		lines = append(lines, "All apps stop while the runner is fenced.")
	}
	for _, item := range plan.Artifacts {
		lines = append(lines, fmt.Sprintf("%s %s (%s -> %s)", item.Action, item.Path, item.Before.SHA256, item.After.SHA256))
	}
	for _, unit := range plan.StaleProcesses {
		lines = append(lines, "Process retained without restart: "+unit)
	}
	if o.UpdateAbort {
		lines = append(lines, "Abort restores the captured DB and WAL or original absence, plus recorded previous artifacts. Added settle enablement is withdrawn before its unit is removed.")
	}
	lines = append(lines, fmt.Sprintf("Type target name %q to confirm: ", o.Name))
	_, err := io.WriteString(c.Output, strings.Join(lines, "\n"))
	return err
}
