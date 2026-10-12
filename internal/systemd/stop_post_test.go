package systemd

import (
	"context"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func stopPostFixture() (StarterProcess, string) {
	self, _ := starterFixture()
	output := "LoadState=loaded\nActiveState=deactivating\nSubState=stop-post\nMainPID=0\nControlPID=101\nControlGroup=" + self.ControlGroup + "\nInvocationID=" + self.InvocationID + "\nJob=\nNeedDaemonReload=no\n"
	return self, output
}

func TestStopPostInvocationRequiresCurrentManagerControlProcess(t *testing.T) {
	self, output := stopPostFixture()
	runner := &recorder{results: []localexec.Result{{Stdout: output}, {Stdout: output}}}
	if err := client(t, runner).StopPostInvocation(t.Context(), unit(t), self); err != nil {
		t.Fatal("automatic no-job cleanup refused", err)
	}
	for _, changed := range []string{
		strings.Replace(output, "ControlPID=101", "ControlPID=102", 1),
		strings.Replace(output, "MainPID=0", "MainPID=101", 1),
		strings.Replace(output, "deactivating", "active", 1),
		strings.Replace(output, "stop-post", "stop-sigterm", 1),
		strings.Replace(output, self.InvocationID, strings.Repeat("b", 32), 1),
		strings.Replace(output, self.ControlGroup, "/other", 1),
		strings.Replace(output, "NeedDaemonReload=no", "NeedDaemonReload=yes", 1),
		output + "ControlPID=101\n",
	} {
		runner := &recorder{results: []localexec.Result{{Stdout: changed}}}
		if client(t, runner).StopPostInvocation(context.Background(), unit(t), self) == nil {
			t.Fatal("foreign cleanup process admitted")
		}
	}
}

func TestStopPostInvocationChecksCurrentOptionalJob(t *testing.T) {
	self, output := stopPostFixture()
	output = strings.Replace(output, "Job=\n", "Job=17\n", 1)
	for _, kind := range []string{"start", "stop", "restart"} {
		job := "Id=17\nUnit=" + unit(t).String() + "\nJobType=" + kind + "\nState=running\n"
		runner := &recorder{results: []localexec.Result{{Stdout: output}, {Stdout: job}, {Stdout: output}}}
		if err := client(t, runner).StopPostInvocation(t.Context(), unit(t), self); err != nil {
			t.Fatal("current lifecycle cleanup refused", kind, err)
		}
	}
	job := "Id=17\nUnit=" + unit(t).String() + "\nJobType=stop\nState=running\n"
	for _, changed := range []string{
		strings.Replace(job, "Id=17", "Id=18", 1),
		strings.Replace(job, unit(t).String(), "other.service", 1),
		strings.Replace(job, "stop", "reload", 1),
		strings.Replace(job, "running", "waiting", 1),
	} {
		runner := &recorder{results: []localexec.Result{{Stdout: output}, {Stdout: changed}}}
		if client(t, runner).StopPostInvocation(t.Context(), unit(t), self) == nil {
			t.Fatal("foreign cleanup job admitted")
		}
	}
	runner := &recorder{results: []localexec.Result{{Stdout: output}, {Stdout: job}, {Stdout: strings.Replace(output, "Job=17", "Job=18", 1)}}}
	if client(t, runner).StopPostInvocation(t.Context(), unit(t), self) == nil {
		t.Fatal("replaced cleanup job admitted")
	}
}
