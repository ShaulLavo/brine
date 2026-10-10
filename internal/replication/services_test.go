package replication

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestReplicaStoppedRequiresNoServiceProcessesOrManagerJob(t *testing.T) {
	base := "LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\nControlPID=0\nControlGroup=\nJob=\n"
	for name, output := range map[string]string{"stopped": base, "main-pid": strings.Replace(base, "MainPID=0", "MainPID=17", 1), "control-pid": strings.Replace(base, "ControlPID=0", "ControlPID=17", 1), "pending-job": strings.Replace(base, "Job=", "Job=27", 1), "live-group": strings.Replace(base, "ControlGroup=", "ControlGroup=/live/group", 1), "active": strings.Replace(base, "ActiveState=inactive", "ActiveState=active", 1), "duplicate": base + "MainPID=0\n", "missing": strings.Replace(base, "ControlPID=0\n", "", 1)} {
		t.Run(name, func(t *testing.T) {
			e := &barrierExec{result: localexec.Result{Stdout: output}}
			session, err := localexec.NewSession(e, 1000, "/srv/state", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			s := NewServices(session)
			stopped, err := s.ReplicaStopped(context.Background(), "brine-litestream-"+strings.Repeat("2", 32)+".service")
			if name == "stopped" {
				if err != nil || !stopped {
					t.Fatal("complete stopped proof refused")
				}
			} else if err == nil && stopped {
				t.Fatal("incomplete stopped proof accepted")
			}
		})
	}
}
