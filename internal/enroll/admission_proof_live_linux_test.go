//go:build linux

package enroll

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This helper touches only the explicitly passed test-owned file. Its parent
// registers pipe/process teardown before launch. It changes no host services.
func TestAdmissionProofFDHelper(t *testing.T) {
	index := slices.Index(os.Args, "--admission-fd-helper")
	if index < 0 {
		return
	}
	if len(os.Args) != index+3 {
		t.Fatal("invalid fixture helper arguments")
	}
	name := filepath.Base(os.Args[index+1])
	if !slices.Contains(admissionStoreNames, name) {
		t.Fatal("invalid fixture store name")
	}
	root, err := os.OpenRoot(filepath.Dir(os.Args[index+1]))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()
	f, err := root.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	if os.Args[index+2] == "deleted" {
		if err := root.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	input := bufio.NewScanner(os.Stdin)
	for input.Scan() {
		if input.Text() != "close" {
			t.Fatal("invalid fixture control")
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		f = nil
		if _, err := fmt.Fprintln(os.Stdout, "closed"); err != nil {
			t.Fatal(err)
		}
	}
	if err := input.Err(); err != nil {
		t.Fatal(err)
	}
	if f != nil {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

type proofFDHelper struct {
	pid    string
	input  io.WriteCloser
	output *bufio.Reader
}

func startProofFDHelper(t *testing.T, path string, deleted bool) proofFDHelper {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
	t.Cleanup(cancel)
	mode := "held"
	if deleted {
		mode = "deleted"
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- Re-exec this test binary with fixed flags and a test-owned store path.
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestAdmissionProofFDHelper$", "--", "--admission-fd-helper", path, mode)
	cmd.WaitDelay = 100 * time.Millisecond
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(errors.Join(err, input.Close()))
	}
	var diagnostics strings.Builder
	cmd.Stderr = &diagnostics
	t.Cleanup(func() {
		if err := input.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
		if cmd.Process == nil {
			return
		}
		if err := cmd.Wait(); err != nil {
			t.Errorf("fixture child failed %v, %s", err, diagnostics.String())
		}
		assertProofChildGone(t, cmd.Process.Pid)
	})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	helper := proofFDHelper{pid: strconv.Itoa(cmd.Process.Pid), input: input, output: bufio.NewReader(output)}
	line, err := helper.output.ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("helper not ready %q %v", line, err)
	}
	return helper
}

func assertProofChildGone(t *testing.T, pid int) {
	t.Helper()
	proc, err := os.OpenRoot("/proc")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := proc.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := proc.Stat(strconv.Itoa(pid)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("owned child still present %v", err)
	}
}

func TestAdmissionProofLiveFDHolders(t *testing.T) {
	for _, name := range admissionStoreNames {
		t.Run(name, func(t *testing.T) { testProofLiveName(t, name) })
	}
}

func testProofLiveName(t *testing.T, name string) {
	t.Helper()
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("deleted=%t", deleted), func(t *testing.T) { testProofLiveHolder(t, name, deleted) })
	}
}

func testProofLiveHolder(t *testing.T, name string, deleted bool) {
	t.Helper()
	p, b, _ := newSyntheticProof(t)
	path := filepath.Join(p.Root, admissionState, name)
	proofWrite(t, path, "owned live descriptor fixture")
	helper := startProofFDHelper(t, path, deleted)
	p.ProcRoot = "/proc"
	store, err := p.storeView(b.RunnerUID)
	if err != nil {
		t.Fatal(err)
	}
	before := proofDescriptorCount(t)
	_, _, err = p.scanProcess(t.Context(), helper.pid, store, -1, 1024)
	if err == nil {
		t.Fatal("real kernel holder accepted")
	}
	if _, err := fmt.Fprintln(helper.input, "close"); err != nil {
		t.Fatal(err)
	}
	line, err := helper.output.ReadString('\n')
	if err != nil || line != "closed\n" {
		t.Fatalf("helper close acknowledgement %q %v", line, err)
	}
	_, count, err := p.scanProcess(t.Context(), helper.pid, store, -1, 1024)
	if err != nil || count == 0 {
		t.Fatalf("closed-holder live control, count %d, %v", count, err)
	}
	if after := proofDescriptorCount(t); before != after {
		t.Fatalf("scan leaked descriptors, before %d after %d", before, after)
	}
}

func proofDescriptorCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestAdmissionProofLiveMountInfoRead(t *testing.T) {
	p := (AdmissionProofs{ProcRoot: "/proc", MountInfo: "/proc/self/mountinfo"}).bounded()
	raw, err := p.readMountInfo(t.Context())
	if err != nil || raw == "" || !strings.HasSuffix(raw, "\n") {
		t.Fatal("numeric self mountinfo read failed", err)
	}
}

func TestAdmissionProofLiveVirtualFileLimit(t *testing.T) {
	before := proofDescriptorCount(t)
	_, err := proofRead(t.Context(), filepath.Join("/proc", strconv.Itoa(os.Getpid()), "stat"), 1)
	if err == nil {
		t.Fatal("virtual file with zero stat size bypassed byte limit")
	}
	if proofDescriptorCount(t) != before {
		t.Fatal("oversized virtual file read leaked a descriptor")
	}
}
