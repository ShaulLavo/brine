package data

import (
	"encoding/json"
	"testing"
)

func locationPresent(t *testing.T, seal EngineTreeSeal, parent EngineDirectoryIdentity) EngineLocation {
	t.Helper()
	seal.Parent = parent
	location, err := PresentEngineLocation(seal)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func locationAbsent(t *testing.T, root, parent EngineDirectoryIdentity) EngineLocation {
	t.Helper()
	location, err := AbsentEngineLocation(root, parent)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func transitionLayout(t *testing.T, transition EngineTransition, state EngineLayoutState) EngineLayoutObservation {
	t.Helper()
	c := transition.Common()
	oldLive := locationPresent(t, c.OldTree, c.OldTree.Parent)
	newStage := locationPresent(t, c.TargetTree, c.TargetTree.Parent)
	oldPreserved := locationPresent(t, c.OldTree, c.PreservedParent)
	liveAbsent := locationAbsent(t, c.OldTree.Root, c.OldTree.Parent)
	stageAbsent := locationAbsent(t, c.TargetTree.Root, c.TargetTree.Parent)
	switch state {
	case EngineLayoutPrepared:
		return EngineLayoutObservation{Live: oldLive, Staged: newStage, Preserved: locationAbsent(t, c.OldTree.Root, c.PreservedParent)}
	case EngineLayoutPreserved:
		return EngineLayoutObservation{Live: liveAbsent, Staged: newStage, Preserved: oldPreserved}
	case EngineLayoutPromoted:
		return EngineLayoutObservation{Live: locationPresent(t, c.TargetTree, c.OldTree.Parent), Staged: stageAbsent, Preserved: oldPreserved}
	default:
		t.Fatal("unexpected fixture layout", state)
		return EngineLayoutObservation{}
	}
}

func TestAllOldNewAbsentUnknownForeignLayoutCombinations(t *testing.T) {
	transition := transitionFixture(t)
	c := transition.Common()
	foreign := c.TargetTree
	foreign.Directory.Inode = 90
	live := []EngineLocation{
		locationPresent(t, c.OldTree, c.OldTree.Parent), locationPresent(t, c.TargetTree, c.OldTree.Parent),
		locationAbsent(t, c.OldTree.Root, c.OldTree.Parent), UnknownEngineLocation(), locationPresent(t, foreign, c.OldTree.Parent),
	}
	staged := []EngineLocation{
		locationPresent(t, c.TargetTree, c.TargetTree.Parent), locationPresent(t, c.OldTree, c.TargetTree.Parent),
		locationAbsent(t, c.TargetTree.Root, c.TargetTree.Parent), UnknownEngineLocation(), locationPresent(t, foreign, c.TargetTree.Parent),
	}
	preserved := []EngineLocation{
		locationPresent(t, c.OldTree, c.PreservedParent), locationPresent(t, c.TargetTree, c.PreservedParent),
		locationAbsent(t, c.OldTree.Root, c.PreservedParent), UnknownEngineLocation(), locationPresent(t, foreign, c.PreservedParent),
	}
	for index := 0; index < 125; index++ {
		a, b, d := index/25, index/5%5, index%5
		observation := EngineLayoutObservation{Live: live[a], Staged: staged[b], Preserved: preserved[d]}
		wanted := expectedTransitionLayout(a, b, d)
		if got := ClassifyEngineTransitionLayout(transition, observation); got != wanted {
			t.Fatalf("layout %d/%d/%d: %s, want %s", a, b, d, got, wanted)
		}
	}
}

func expectedTransitionLayout(live, stage, preserved int) EngineLayoutState {
	if live == 0 && stage == 0 && preserved == 2 {
		return EngineLayoutPrepared
	}
	if live == 2 && stage == 0 && preserved == 0 {
		return EngineLayoutPreserved
	}
	if live == 1 && stage == 2 && preserved == 0 {
		return EngineLayoutPromoted
	}
	return EngineLayoutConflict
}

func TestNamespaceParentAndContentsReplacementRefuseAdoption(t *testing.T) {
	transition := transitionFixture(t)
	for _, mutate := range []func(*EngineTreeSeal){
		func(s *EngineTreeSeal) { s.Root.Inode++ },
		func(s *EngineTreeSeal) { s.Parent.Inode++ },
		func(s *EngineTreeSeal) { s.ContentsSHA256 = transition.Common().TargetTree.ContentsSHA256 },
	} {
		observation := transitionLayout(t, transition, EngineLayoutPreserved)
		seal, _ := observation.Preserved.Tree()
		mutate(&seal)
		changed, err := PresentEngineLocation(seal)
		if err != nil {
			t.Fatal(err)
		}
		observation.Preserved = changed
		if ClassifyEngineTransitionLayout(transition, observation) != EngineLayoutConflict {
			t.Fatal("replaced namespace or contents adopted")
		}
	}
}

func TestLayoutCodecKeepsAbsenceDistinctFromUnknown(t *testing.T) {
	transition := transitionFixture(t)
	observation := transitionLayout(t, transition, EngineLayoutPrepared)
	raw, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	var decoded EngineLayoutObservation
	if json.Unmarshal(raw, &decoded) != nil || ClassifyEngineTransitionLayout(transition, decoded) != EngineLayoutPrepared {
		t.Fatal("known layout changed through codec")
	}
	decoded.Preserved = UnknownEngineLocation()
	if ClassifyEngineTransitionLayout(transition, decoded) != EngineLayoutConflict {
		t.Fatal("unknown destination treated as absent")
	}
	for _, raw := range []string{
		`{"kind":"absent"}`, `{"kind":"absent","root":null,"parent":{}}`,
		`{"kind":"unknown","root":{"device":1,"inode":1}}`, `{"kind":"unknown","kind":"unknown"}`,
		`{"kind":"present","tree":null}`, `{"Kind":"unknown"}`,
	} {
		var location EngineLocation
		if json.Unmarshal([]byte(raw), &location) == nil {
			t.Fatal("malformed location accepted", raw)
		}
	}
}
