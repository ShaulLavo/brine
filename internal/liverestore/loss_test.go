package liverestore

import "testing"

func TestLiveRestoreLossDoesNotTreatAnUnobservedCurrentOffsetAsZero(t *testing.T) {
	spec := planFixture(t)
	spec.Safety.CommittedOffset, spec.Safety.CommittedOffsetKnown = 0, false
	p, err := NewPlan(spec)
	if err != nil || p.Loss().Kind != "unknown" || p.Loss().CommittedOffsetsLost != 0 {
		t.Fatal("missing current observation became a loss number", err)
	}
	spec.Safety.CommittedOffset = 21
	if _, err = NewPlan(spec); err == nil {
		t.Fatal("unknown current offset carried invented numeric evidence")
	}
}
