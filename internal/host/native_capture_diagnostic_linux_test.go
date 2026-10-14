//go:build linux

package host

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/ShaulLavo/brine/internal/result"
)

func TestNativeCapturePostVerificationReportsFirstActualRefusal(t *testing.T) {
	err := checkNativeCapturePostVerification(context.Background(), nil, nil, extractedPublicationCandidate{})
	if result.Classify(err).Code() != result.NativeCaptureSourceRefused {
		t.Fatal("first failed source guard lost", err)
	}
	copied, _ := nativeOnlineCopyFixture(t)
	err = checkNativeCapturePostVerification(context.Background(), copied, &engineNativeHandle{fd: -1}, extractedPublicationCandidate{})
	if result.Classify(err).Code() != result.NativeCaptureLiveRefused {
		t.Fatal("live guard did not follow successful source guard", err)
	}
	process := "/proc/" + strconv.Itoa(os.Getpid())
	raw, readErr := engineProcessFile(process, "stat")
	pid, ticks, parseErr := parseEngineProcessStat(string(raw))
	if readErr != nil || parseErr != nil {
		t.Fatal("current test process identity", readErr, parseErr)
	}
	raw, readErr = engineProcessFile(process, "cgroup")
	group, parseErr := parseEngineCgroup(string(raw))
	if readErr != nil || parseErr != nil {
		t.Fatal("current test process cgroup", readErr, parseErr)
	}
	live := &engineNativeHandle{pid: pid, ticks: ticks, cgroup: group, fd: -1}
	err = checkNativeCapturePostVerification(context.Background(), copied, live, extractedPublicationCandidate{})
	if result.Classify(err).Code() != result.NativeCaptureCandidateRefused {
		t.Fatal("module guard did not follow successful source and process guards", err)
	}

}
