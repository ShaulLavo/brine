package systemd

import (
	"context"
	"strconv"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func (c *Client) StopPostInvocation(ctx context.Context, unit Unit, self StarterProcess) error {
	if unit.value == "" || self.PID == 0 || self.StartTicks == 0 || len(self.InvocationID) != 32 || self.ControlGroup == "" {
		return &localexec.Error{Kind: localexec.Invalid}
	}
	properties := "LoadState,ActiveState,SubState,MainPID,ControlPID,ControlGroup,InvocationID,Job,NeedDaemonReload"
	first, err := c.run(ctx, []string{"--user", "show", unit.value, "--all", "--property=" + properties}, false)
	if err != nil {
		return err
	}
	job, err := parseStopPostUnit(first.Stdout, self)
	if err != nil {
		return err
	}
	if job != 0 {
		observed, err := c.run(ctx, []string{"--user", "show", strconv.FormatUint(uint64(job), 10), "--all", "--property=Id,Unit,JobType,State"}, false)
		if err != nil {
			return err
		}
		if err = parseStopPostJob(observed.Stdout, job, unit); err != nil {
			return err
		}
	}
	second, err := c.run(ctx, []string{"--user", "show", unit.value, "--all", "--property=" + properties}, false)
	if err != nil {
		return err
	}
	current, err := parseStopPostUnit(second.Stdout, self)
	if err != nil || current != job || ctx.Err() != nil {
		return &localexec.Error{Kind: localexec.Failed}
	}
	return nil
}

func parseStopPostUnit(output string, self StarterProcess) (uint32, error) {
	fields, err := startupProperties(output, []string{"LoadState", "ActiveState", "SubState", "MainPID", "ControlPID", "ControlGroup", "InvocationID", "Job", "NeedDaemonReload"})
	if err != nil {
		return 0, err
	}
	if fields["LoadState"] != "loaded" || fields["ActiveState"] != "deactivating" || fields["SubState"] != "stop-post" || fields["MainPID"] != "0" || fields["ControlPID"] != strconv.FormatUint(uint64(self.PID), 10) || fields["ControlGroup"] != self.ControlGroup || fields["InvocationID"] != self.InvocationID || fields["NeedDaemonReload"] != "no" {
		return 0, &localexec.Error{Kind: localexec.Failed}
	}
	if fields["Job"] == "" {
		return 0, nil
	}
	job, err := strconv.ParseUint(fields["Job"], 10, 32)
	if err != nil || job == 0 || strconv.FormatUint(job, 10) != fields["Job"] {
		return 0, &localexec.Error{Kind: localexec.Failed}
	}
	return uint32(job), nil
}

func parseStopPostJob(output string, expected uint32, unit Unit) error {
	fields, err := startupProperties(output, []string{"Id", "Unit", "JobType", "State"})
	if err != nil {
		return err
	}
	typeValid := fields["JobType"] == "start" || fields["JobType"] == "stop" || fields["JobType"] == "restart"
	if fields["Id"] != strconv.FormatUint(uint64(expected), 10) || fields["Unit"] != unit.value || !typeValid || fields["State"] != "running" {
		return &localexec.Error{Kind: localexec.Failed}
	}
	return nil
}
