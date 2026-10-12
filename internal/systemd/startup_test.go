package systemd

import (
	"context"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func starterFixture() (StarterProcess, string) {
	self := StarterProcess{PID: 101, StartTicks: 9, InvocationID: strings.Repeat("a", 32), ControlGroup: "/user.slice/fixture.service"}
	output := "LoadState=loaded\nActiveState=activating\nSubState=start\nMainPID=101\nExecMainPID=101\nControlPID=0\nControlGroup=" + self.ControlGroup + "\nInvocationID=" + self.InvocationID + "\nJob=17\nNeedDaemonReload=no\n"
	return self, output
}

func TestStartupInvocationAllowsOnlyExactCurrentStartJob(t *testing.T) {
	self, output := starterFixture()
	unit := unit(t)
	job := "Id=17\nUnit=" + unit.String() + "\nJobType=start\nState=running\n"
	runner := &recorder{results: []localexec.Result{{Stdout: output}, {Stdout: job}, {Stdout: output}}}
	invocation, err := client(t, runner).StartupInvocation(context.Background(), unit, self)
	if err != nil || invocation.JobID() != 17 || invocation.Process() != self || invocation.Unit() != unit {
		t.Fatal("current owned starter refused", err)
	}
	for _, command := range runner.commands {
		if command.Mutation || command.Path != "systemctl" {
			t.Fatal("startup classification mutated the manager")
		}
	}
}

func TestStartupInvocationRefusesForeignOrChangedJob(t *testing.T) {
	self, output := starterFixture()
	unit := unit(t)
	job := "Id=17\nUnit=" + unit.String() + "\nJobType=start\nState=running\n"
	for _, changed := range []string{
		strings.Replace(job, "Id=17", "Id=18", 1),
		strings.Replace(job, unit.String(), "other.service", 1),
		strings.Replace(job, "JobType=start", "JobType=stop", 1),
		strings.Replace(job, "State=running", "State=waiting", 1),
		job + "State=running\n",
	} {
		runner := &recorder{results: []localexec.Result{{Stdout: output}, {Stdout: changed}, {Stdout: output}}}
		if _, err := client(t, runner).StartupInvocation(context.Background(), unit, self); err == nil {
			t.Fatal("foreign manager job earned the current-starter exception")
		}
	}
	runner := &recorder{results: []localexec.Result{{Stdout: output}, {Stdout: job}, {Stdout: strings.Replace(output, "Job=17", "Job=18", 1)}}}
	if _, err := client(t, runner).StartupInvocation(context.Background(), unit, self); err == nil {
		t.Fatal("job replacement after its inspection was accepted")
	}
}

func TestStartupInvocationRefusesForeignProcessOrUnitDrift(t *testing.T) {
	self, output := starterFixture()
	for _, changed := range []string{
		strings.Replace(output, "MainPID=101", "MainPID=102", 1),
		strings.Replace(output, "ExecMainPID=101", "ExecMainPID=102", 1),
		strings.Replace(output, "ControlPID=0", "ControlPID=103", 1),
		strings.Replace(output, self.InvocationID, strings.Repeat("b", 32), 1),
		strings.Replace(output, self.ControlGroup, "/other", 1),
		strings.Replace(output, "activating", "active", 1),
		strings.Replace(output, "NeedDaemonReload=no", "NeedDaemonReload=yes", 1),
		output + "MainPID=101\n",
	} {
		runner := &recorder{results: []localexec.Result{{Stdout: changed}}}
		if _, err := client(t, runner).StartupInvocation(context.Background(), unit(t), self); err == nil {
			t.Fatal("unowned process or unit drift earned startup authority")
		}
	}
}
