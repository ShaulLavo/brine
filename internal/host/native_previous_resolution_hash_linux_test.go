//go:build linux

package host

import (
	"strings"
	"testing"
)

func TestStoppedObservationBindsPreviousResolutionClaims(t *testing.T) {
	observed := nativeBackupObserved{needsCapture: true, sourceSealSHA256: strings.Repeat("1", 64), stageParentSHA256: strings.Repeat("2", 64), initialStagesSHA256: strings.Repeat("3", 64), previousClaimsSHA256: strings.Repeat("4", 64)}
	initial := observed.hash()
	observed.previousClaimsSHA256 = strings.Repeat("5", 64)
	if observed.hash() == initial {
		t.Fatal("fresh stopped observation ignored prior resolver claim/outcome")
	}
}
