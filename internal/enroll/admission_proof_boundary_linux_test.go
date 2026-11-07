//go:build linux

package enroll

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestAdmissionProofSyntheticProcMalformed(t *testing.T) {
	for _, name := range []string{"0", "01", "123x", "2147483648"} {
		t.Run(name, func(t *testing.T) {
			p, b, _ := newSyntheticProof(t)
			syntheticProcess(t, p, name, b.RunnerUID+1)
			requireProofRefusal(t, p, b, false)
		})
	}
}

func TestAdmissionProofSyntheticStatTruncation(t *testing.T) {
	p, b, _ := newSyntheticProof(t)
	base := syntheticProcess(t, p, "123", b.RunnerUID+1)
	raw, err := proofRead(t.Context(), filepath.Join(base, "stat"), 16384)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "x", "00", "-0"} {
		proofWrite(t, filepath.Join(base, "stat"), strings.TrimSuffix(raw, "0\n")+suffix+"\n")
		requireProofRefusal(t, p, b, false)
	}
	proofWrite(t, filepath.Join(base, "status"), strings.Repeat("x", 65537))
	proofWrite(t, filepath.Join(base, "stat"), raw)
	requireProofRefusal(t, p, b, false)
}

func TestAdmissionProofSyntheticSymlinkedAncestor(t *testing.T) {
	p, b, _ := newSyntheticProof(t)
	source := filepath.Join(p.Root, "home")
	target := filepath.Join(p.Root, "moved-home")
	if err := os.Rename(source, target); err != nil {
		t.Fatal(err)
	}
	proofLink(t, target, source)
	requireProofRefusal(t, p, b, false)
}

func TestAdmissionProofSyntheticCancelledPublicAPI(t *testing.T) {
	p, b, e := newSyntheticProof(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := proofDescriptorCount(t)
	_, err := p.ProveStoreQuiescent(ctx, b)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("store cancellation lost", err)
	}
	_, err = p.ProveRunnerQuiescent(ctx, b)
	if !errors.Is(err, context.Canceled) || len(e.calls) != 0 {
		t.Fatal("runner cancellation lost", err)
	}
	if proofDescriptorCount(t) != before {
		t.Fatal("canceled public API leaked descriptors")
	}
}

func TestAdmissionProofSyntheticCommandStderr(t *testing.T) {
	p, b, e := newSyntheticProof(t)
	e.override = func(_ localexec.Command) (localexec.Result, error, bool) {
		return localexec.Result{Stdout: "brine:!:20000:0:99999:7::1:\n", Stderr: "fixture warning"}, nil, true
	}
	requireProofRefusal(t, p, b, true)
}

func TestAdmissionProofSyntheticMissingRoots(t *testing.T) {
	p, b, e := newSyntheticProof(t)
	p.ProcRoot = ""
	requireProofRefusal(t, p, b, true)
	if len(e.calls) != 0 {
		t.Fatal("invalid roots reached executor")
	}
}

func TestAdmissionProofSyntheticEmptyNestedCgroups(t *testing.T) {
	p, b, _ := newSyntheticProof(t)
	proofWrite(t, fixtureCgroup(p, b)+"/child/leaf/cgroup.procs", "")
	proofWrite(t, fixtureCgroup(p, b)+"/child/cgroup.procs", "")
	if _, err := p.ProveRunnerQuiescent(t.Context(), b); err != nil {
		t.Fatal("empty recursive fixture", err)
	}
}

func TestAdmissionProofSyntheticPAMMalformedControl(t *testing.T) {
	p, b, _ := newSyntheticProof(t)
	proofWrite(t, filepath.Join(p.Root, "etc/pam.d/runuser"), "auth bogus pam_rootok.so\n")
	requireProofRefusal(t, p, b, true)
}
