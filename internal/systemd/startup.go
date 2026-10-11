package systemd

import (
	"context"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type StarterProcess struct {
	PID          uint32
	StartTicks   uint64
	InvocationID string
	ControlGroup string
}

type StartInvocation struct {
	unit Unit
	self StarterProcess
	job  uint32
}

func (s StartInvocation) Unit() Unit              { return s.unit }
func (s StartInvocation) Process() StarterProcess { return s.self }
func (s StartInvocation) JobID() uint32           { return s.job }

func (c *Client) StartupInvocation(ctx context.Context, unit Unit, self StarterProcess) (StartInvocation, error) {
	if unit.value == "" || self.PID == 0 || self.StartTicks == 0 || len(self.InvocationID) != 32 || self.ControlGroup == "" {
		return StartInvocation{}, &localexec.Error{Kind: localexec.Invalid}
	}
	properties := "LoadState,ActiveState,SubState,MainPID,ExecMainPID,ControlPID,ControlGroup,InvocationID,Job,NeedDaemonReload"
	unitResult, err := c.run(ctx, []string{"--user", "show", unit.value, "--all", "--property=" + properties}, false)
	if err != nil {
		return StartInvocation{}, err
	}
	job, err := parseStarterUnit(unitResult.Stdout, self)
	if err != nil {
		return StartInvocation{}, err
	}
	jobResult, err := c.run(ctx, []string{"--user", "show", strconv.FormatUint(uint64(job), 10), "--all", "--property=Id,Unit,JobType,State"}, false)
	if err != nil {
		return StartInvocation{}, err
	}
	if err = parseStarterJob(jobResult.Stdout, job, unit); err != nil {
		return StartInvocation{}, err
	}
	rechecked, err := c.run(ctx, []string{"--user", "show", unit.value, "--all", "--property=" + properties}, false)
	if err != nil {
		return StartInvocation{}, err
	}
	current, err := parseStarterUnit(rechecked.Stdout, self)
	if err != nil || current != job || ctx.Err() != nil {
		return StartInvocation{}, &localexec.Error{Kind: localexec.Failed}
	}
	return StartInvocation{unit: unit, self: self, job: job}, nil
}

func parseStarterUnit(output string, self StarterProcess) (uint32, error) {
	fields, err := startupProperties(output, []string{"LoadState", "ActiveState", "SubState", "MainPID", "ExecMainPID", "ControlPID", "ControlGroup", "InvocationID", "Job", "NeedDaemonReload"})
	if err != nil {
		return 0, err
	}
	pid := strconv.FormatUint(uint64(self.PID), 10)
	if fields["LoadState"] != "loaded" || fields["ActiveState"] != "activating" || fields["SubState"] != "start" || fields["MainPID"] != pid || fields["ExecMainPID"] != pid || fields["ControlPID"] != "0" || fields["ControlGroup"] != self.ControlGroup || fields["InvocationID"] != self.InvocationID || fields["NeedDaemonReload"] != "no" {
		return 0, &localexec.Error{Kind: localexec.Failed}
	}
	job, err := strconv.ParseUint(fields["Job"], 10, 32)
	if err != nil || job == 0 || strconv.FormatUint(job, 10) != fields["Job"] {
		return 0, &localexec.Error{Kind: localexec.Failed}
	}
	return uint32(job), nil
}

func parseStarterJob(output string, expected uint32, unit Unit) error {
	fields, err := startupProperties(output, []string{"Id", "Unit", "JobType", "State"})
	if err != nil {
		return err
	}
	if fields["Id"] != strconv.FormatUint(uint64(expected), 10) || fields["Unit"] != unit.value || fields["JobType"] != "start" || fields["State"] != "running" {
		return &localexec.Error{Kind: localexec.Failed}
	}
	return nil
}

func startupProperties(output string, names []string) (map[string]string, error) {
	bad := func() (map[string]string, error) { return nil, &localexec.Error{Kind: localexec.Failed} }
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	fields := make(map[string]string, len(names))
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || !allowed[key] {
			return bad()
		}
		if _, duplicate := fields[key]; duplicate {
			return bad()
		}
		fields[key] = value
	}
	if len(fields) != len(names) {
		return bad()
	}
	return fields, nil
}
