package plan

import (
	"errors"
	"strings"
	"testing"
)

func TestRemovePreparedIncarnationRequiresArchive(t *testing.T) {
	in := fixture(t, "ready-arm64")
	in.State.DataIncarnations = []DataIncarnation{{App: "hello", ID: strings.Repeat("a", 32)}}
	if _, err := BuildRemove(in); !errors.Is(err, ErrPersistentData) {
		t.Fatalf("prepared persistent allocation was treated as absent: %v", err)
	}
}

func TestRemoveDoesNotMistakeOtherIncarnationForTarget(t *testing.T) {
	in := fixture(t, "ready-arm64")
	in.State.DataIncarnations = []DataIncarnation{{App: "other", ID: strings.Repeat("a", 32)}}
	p, err := BuildRemove(in)
	if err != nil || p.Kind != NoOp || len(p.Changes) != 0 {
		t.Fatalf("unrelated allocation changed removal: %+v %v", p, err)
	}
}

func TestRemoveRejectsMalformedIncarnationState(t *testing.T) {
	for _, incarnations := range [][]DataIncarnation{
		{{App: "hello", ID: "invalid"}},
		{{App: "", ID: strings.Repeat("a", 32)}},
		{{App: "hello", ID: strings.Repeat("a", 32)}, {App: "hello", ID: strings.Repeat("b", 32)}},
		{{App: "first", ID: strings.Repeat("a", 32)}, {App: "second", ID: strings.Repeat("a", 32)}},
	} {
		in := fixture(t, "ready-arm64")
		in.State.DataIncarnations = incarnations
		if p, err := BuildRemove(in); err == nil || errors.Is(err, ErrPersistentData) {
			t.Fatalf("invalid ownership accepted as valid state: %+v %v", p, err)
		}
	}
}
