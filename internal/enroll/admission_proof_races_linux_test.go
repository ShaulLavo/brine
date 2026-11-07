//go:build linux

package enroll

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

// Mutations happen synchronously at cancellation checkpoints, without sleeps,
// probabilistic timing, production test hooks, or claims about live proc races.
type proofMutationContext struct {
	context.Context
	calls  int
	at     int
	mutate func()
}

func (c *proofMutationContext) Err() error {
	c.calls++
	if c.calls == c.at {
		c.mutate()
	}
	return c.Context.Err()
}

type proofEnumerationRace struct {
	name string
	at   int
	fd   bool
}

func TestAdmissionProofSyntheticEnumerationRaces(t *testing.T) {
	for _, tc := range []proofEnumerationRace{{"process after enumeration", 2, false}, {"process after first scan", 28, false}, {"fd after enumeration", 14, true}, {"fd after fd recheck", 17, true}} {
		t.Run(tc.name, func(t *testing.T) { testSyntheticEnumerationRace(t, tc) })
	}
}

func testSyntheticEnumerationRace(t *testing.T, tc proofEnumerationRace) {
	t.Helper()
	p, b, _ := newSyntheticProof(t)
	base := syntheticProcess(t, p, "123", b.RunnerUID+1)
	other := filepath.Join(p.Root, "other")
	proofWrite(t, other, "")
	ctx := &proofMutationContext{Context: t.Context(), at: tc.at, mutate: func() {
		if tc.fd {
			proofLink(t, other, filepath.Join(base, "fd/9"))
			return
		}
		syntheticProcess(t, p, "456", b.RunnerUID+1)
	}}
	proof, err := p.proveStoreQuiescent(ctx, b)
	if err == nil || proof != (AdmissionQuiescenceProof{}) {
		t.Fatalf("race accepted at %d calls, %v", ctx.calls, err)
	}
}

func TestAdmissionProofSyntheticFenceRaces(t *testing.T) {
	for _, part := range []string{"linger", "PAM", "mount", "cgroup", "store", "UID process"} {
		t.Run(part, func(t *testing.T) { testSyntheticFenceRace(t, part) })
	}
}

func testSyntheticFenceRace(t *testing.T, part string) {
	t.Helper()
	p, b, e := newSyntheticProof(t)
	n := 0
	e.override = func(c localexec.Command) (localexec.Result, error, bool) {
		if c.Path != "getent" || c.Args[0] != "passwd" {
			return localexec.Result{}, nil, false
		}
		n++
		if n == 2 {
			mutateProofFence(t, p, b, part)
		}
		return localexec.Result{}, nil, false
	}
	requireProofRefusal(t, p, b, true)
}

func mutateProofFence(t *testing.T, p AdmissionProofs, b AdmissionProofContext, part string) {
	t.Helper()
	switch part {
	case "linger":
		proofWrite(t, filepath.Join(p.Root, "var/lib/systemd/linger/brine"), "")
	case "PAM":
		proofWrite(t, filepath.Join(p.Root, "etc/pam.d/runuser"), "account required pam_unix.so\n")
	case "mount":
		proofWrite(t, p.MountInfo, "1 0 0:1 / /home/brine rw - tmpfs tmpfs rw\n")
	case "cgroup":
		proofWrite(t, fixtureCgroup(p, b)+"/new/cgroup.procs", "")
	case "store":
		proofWrite(t, filepath.Join(p.Root, admissionState, "control.db"), "")
	case "UID process":
		syntheticProcess(t, p, "123", b.RunnerUID)
	}
}

func TestAdmissionProofCancellationAndCleanup(t *testing.T) {
	for _, at := range []int{1, 3, 5, 7, 9, 12, 16, 20, 30} {
		t.Run(strconv.Itoa(at), func(t *testing.T) { testProofCancellation(t, at) })
	}
}

func testProofCancellation(t *testing.T, at int) {
	t.Helper()
	p, b, _ := newSyntheticProof(t)
	base := syntheticProcess(t, p, "123", b.RunnerUID+1)
	proofWrite(t, filepath.Join(p.Root, "other"), "")
	proofLink(t, filepath.Join(p.Root, "other"), filepath.Join(base, "fd/9"))
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &proofMutationContext{Context: parent, at: at, mutate: cancel}
	before := proofDescriptorCount(t)
	proof, err := p.proveStoreQuiescent(ctx, b)
	if !errors.Is(err, context.Canceled) || proof != (AdmissionQuiescenceProof{}) {
		t.Fatalf("cancellation lost, calls %d, proof %+v, %v", ctx.calls, proof, err)
	}
	if after := proofDescriptorCount(t); before != after {
		t.Fatalf("canceled scan leaked %d descriptors", after-before)
	}
}

func TestAdmissionProofCommandCancellation(t *testing.T) {
	p, b, e := newSyntheticProof(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.override = func(_ localexec.Command) (localexec.Result, error, bool) {
		cancel()
		return localexec.Result{}, context.Canceled, true
	}
	before := proofDescriptorCount(t)
	proof, err := p.ProveRunnerQuiescent(ctx, b)
	if !errors.Is(err, context.Canceled) || proof != (AdmissionQuiescenceProof{}) {
		t.Fatal("command cancellation lost", err)
	}
	if after := proofDescriptorCount(t); before != after {
		t.Fatal("command cancellation leaked descriptors")
	}
}

func TestAdmissionProofSyntheticBounds(t *testing.T) {
	p, b, _ := newSyntheticProof(t)
	base := syntheticProcess(t, p, "123", b.RunnerUID+1)
	other := filepath.Join(p.Root, "other")
	proofWrite(t, other, "")
	proofLink(t, other, filepath.Join(base, "fd/1"))
	p.limits.entries = 1
	p.limits.fds = 1
	if _, err := p.ProveStoreQuiescent(t.Context(), b); err != nil {
		t.Fatal("exact proc and fd limits refused", err)
	}
	proofLink(t, other, filepath.Join(base, "fd/2"))
	requireProofRefusal(t, p, b, false)
	proofRemove(t, filepath.Join(base, "fd/2"))
	syntheticProcess(t, p, "456", b.RunnerUID+1)
	requireProofRefusal(t, p, b, false)
	p.limits.entries = 10
	p.limits.groups = 1
	p.limits.depth = 0
	if _, err := p.ProveRunnerQuiescent(t.Context(), b); err != nil {
		t.Fatal("exact group/depth limit refused", err)
	}
	proofWrite(t, fixtureCgroup(p, b)+"/child/cgroup.procs", "")
	requireProofRefusal(t, p, b, true)
	p.limits.groups = 2
	requireProofRefusal(t, p, b, true)
	p.limits.depth = 1
	if _, err := p.ProveRunnerQuiescent(t.Context(), b); err != nil {
		t.Fatal("exact recursive group limit refused", err)
	}
}

func TestAdmissionProofContextIsNotAuthority(t *testing.T) {
	p, b, _ := newSyntheticProof(t)
	for _, bad := range []AdmissionProofContext{{}, {EnrollmentID: b.EnrollmentID, TransactionID: b.TransactionID, RunnerUID: 0}, {EnrollmentID: "not an id", TransactionID: b.TransactionID, RunnerUID: b.RunnerUID}, {EnrollmentID: b.EnrollmentID, TransactionID: strings.Repeat("F", 32), RunnerUID: b.RunnerUID}} {
		requireProofRefusal(t, p, bad, false)
	}
	b.FenceExpiryDays = 2
	requireProofRefusal(t, p, b, true)
	b.FenceExpiryDays = 1
	b.RunnerUID++
	requireProofRefusal(t, p, b, false)
	p.Root = ""
	requireProofRefusal(t, p, b, false)
}

func TestAdmissionProofSyntheticReadBounds(t *testing.T) {
	p, _, _ := newSyntheticProof(t)
	file := filepath.Join(p.Root, "bounded")
	for _, size := range []int{16, 17} {
		proofWrite(t, file, strings.Repeat("x", size))
		out, err := proofRead(t.Context(), file, 16)
		if size == 16 && (err != nil || len(out) != 16) {
			t.Fatalf("exact byte limit refused %v", err)
		}
		if size == 17 && err == nil {
			t.Fatal("byte limit exceeded")
		}
	}
	proofRemove(t, file)
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := proofRead(t.Context(), file, 16); err == nil {
		t.Fatal("directory read accepted")
	}
}

func TestAdmissionProofSyntheticTaskBoundsAndChurn(t *testing.T) {
	p, b, _ := newSyntheticProof(t)
	leader := syntheticProcess(t, p, "123", b.RunnerUID+1)
	syntheticPrivateTask(t, p, "123", "456", b.RunnerUID+1)
	p.limits.tasks = 2
	proof, err := p.ProveStoreQuiescent(t.Context(), b)
	if err != nil || proof.Tasks != 2 || proof.Processes != 1 {
		t.Fatal("exact task census refused", proof, err)
	}
	p.limits.tasks = 1
	requireProofRefusal(t, p, b, false)
	p.limits.tasks = 3
	syntheticProcess(t, p, "789", b.RunnerUID+1)
	if _, err = p.ProveStoreQuiescent(t.Context(), b); err != nil {
		t.Fatal("aggregate exact task cap refused", err)
	}
	p.limits.tasks = 2
	requireProofRefusal(t, p, b, false)
	p.limits.tasks = 10
	other := filepath.Join(p.Root, "other")
	proofWrite(t, other, "")
	proofLink(t, other, filepath.Join(leader, "fd/9"))
	task := filepath.Join(p.ProcRoot, "123/task/456")
	proofLink(t, other, filepath.Join(task, "fd/9"))
	p.limits.fds = 2
	if _, err = p.ProveStoreQuiescent(t.Context(), b); err != nil {
		t.Fatal("aggregate exact fd cap refused", err)
	}
	p.limits.fds = 1
	requireProofRefusal(t, p, b, false)
}

func TestAdmissionProofSyntheticTaskEnumerationRaces(t *testing.T) {
	for _, at := range []int{8, 23} {
		t.Run(strconv.Itoa(at), func(t *testing.T) { checkTaskEnumerationRace(t, at) })
	}
}
func checkTaskEnumerationRace(t *testing.T, at int) {
	t.Helper()
	p, b, _ := newSyntheticProof(t)
	syntheticProcess(t, p, "123", b.RunnerUID+1)
	ctx := &proofMutationContext{Context: t.Context(), at: at, mutate: func() { syntheticPrivateTask(t, p, "123", "456", b.RunnerUID+1) }}
	if _, err := p.proveStoreQuiescent(ctx, b); err == nil {
		t.Fatal("task appeared inside stable census", ctx.calls)
	}
}
func syntheticPrivateTask(t *testing.T, p AdmissionProofs, pid, tid string, uid int) {
	t.Helper()
	base := filepath.Join(p.ProcRoot, pid, "task", tid)
	proofMkdir(t, filepath.Join(base, "fd"))
	proofWrite(t, filepath.Join(base, "stat"), tid+" (fixture) S "+strings.Repeat("0 ", 18)+"123 "+strings.TrimSpace(strings.Repeat("0 ", 30))+"\n")
	proofWrite(t, filepath.Join(base, "status"), "Uid: "+strings.Repeat(strconv.Itoa(uid)+" ", 4)+"\n")
}
