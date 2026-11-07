//go:build linux

package enroll

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type privateThreadFD struct {
	pid, tid string
	input    io.WriteCloser
	output   *bufio.Reader
}

func startPrivateThreadFD(t *testing.T, binary, path string) privateThreadFD {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
	t.Cleanup(cancel)
	// #nosec G204 -- Runs only the fixed helper compiled into this test's private directory.
	cmd := exec.CommandContext(ctx, binary, path)
	cmd.WaitDelay = 100 * time.Millisecond
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(errors.Join(err, input.Close()))
	}
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	t.Cleanup(func() {
		_ = input.Close()
		if cmd.Process == nil {
			return
		}
		if err := cmd.Wait(); err != nil {
			t.Errorf("child failed: %v %s", err, diagnostic.String())
		}
		assertProofChildGone(t, cmd.Process.Pid)
		t.Log("owned child waited and actual /proc entry absent")
	})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(output)
	tid, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("thread readiness: %v", err)
	}
	return privateThreadFD{pid: strconv.Itoa(cmd.Process.Pid), tid: strings.TrimSuffix(tid, "\n"), input: input, output: reader}
}

func TestAdmissionProofPrivateThreadFD(t *testing.T) {
	binary := buildPrivateThreadHelper(t)
	for _, name := range admissionStoreNames {
		t.Run(name, func(t *testing.T) { checkPrivateThreadFD(t, binary, name) })
	}
}

func checkPrivateThreadFD(t *testing.T, binary, name string) {
	t.Helper()
	p, b, _ := newSyntheticProof(t)
	path := filepath.Join(p.Root, admissionState, name)
	proofWrite(t, path, "owned descriptor control")
	child := startPrivateThreadFD(t, binary, path)
	p.ProcRoot = "/proc"
	store, err := p.storeView(b.RunnerUID)
	if err != nil {
		t.Fatal(err)
	}
	_, leaderFDs, leaderErr := p.scanProcess(t.Context(), child.pid, store, -1, 1024)
	p.ProcRoot = filepath.Join("/proc", child.pid, "task")
	_, _, threadErr := p.scanTask(t.Context(), child.tid, store, -1, 1024)
	t.Logf("leader result: descriptors=%d error=%v; owned private thread result: %v", leaderFDs, leaderErr, threadErr)
	if threadErr == nil {
		t.Fatal("control invalid: actual thread holder not detected")
	}
	if _, err := io.WriteString(child.input, "c"); err != nil {
		t.Fatal(err)
	}
	ack, err := child.output.ReadString('\n')
	if err != nil || ack != "closed\n" {
		t.Fatalf("close acknowledgment: %q %v", ack, err)
	}
	_, closedFDs, closedErr := p.scanTask(t.Context(), child.tid, store, -1, 1024)
	t.Logf("same private thread after close: descriptors=%d error=%v", closedFDs, closedErr)
	if closedErr != nil {
		t.Fatal("closed-holder control failed", closedErr)
	}
	if leaderErr == nil {
		t.Error("process census omitted a private task descriptor table")
	}
	p.ProcRoot = "/proc"
	if _, _, err := p.scanProcess(t.Context(), child.pid, store, -1, 1024); err != nil {
		t.Fatal("closed-holder group refused", err)
	}
}

func TestAdmissionProofExecutorCaptureAndAcceptanceBounds(t *testing.T) {
	for _, size := range []int{20000, localexec.CommandOutputLimit + 1} {
		out, err := (AdmissionProofs{Executor: localexec.ExecRunner{}}).command(t.Context(), "/usr/bin/head", "-c", strconv.Itoa(size), "/dev/zero")
		if err == nil || len(out.Stdout) > localexec.CommandOutputLimit {
			t.Fatal("oversized observation accepted or capture unbounded", len(out.Stdout), err)
		}
		if size > localexec.CommandOutputLimit && !out.Truncated {
			t.Fatal("capture overflow was not reported")
		}
	}
}

func buildPrivateThreadHelper(t *testing.T) string {
	t.Helper()
	compiler, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("private descriptor-table control requires a C compiler")
	}
	binary := filepath.Join(t.TempDir(), "private-thread")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// #nosec G204 -- Fixed C fixture and private output; compiler is resolved from PATH.
	output, err := exec.CommandContext(ctx, compiler, "-Wall", "-Wextra", "-Werror", "-pthread", "testdata/admission-proof-private-thread.c", "-o", binary).CombinedOutput()
	if err != nil {
		t.Fatalf("compile private-table helper: %v %s", err, output)
	}
	return binary
}
