package plan

import (
	"bytes"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestConfiguredUnpublishedPlanRequiresPublicationWithoutEffects(t *testing.T) {
	in := managedInitializedFixture(t, false)
	runtime := in.Managed.Runtime
	runtime.State = data.EngineUnpublished
	runtime.Database, runtime.Publisher = "", ""
	runtime.Names = []string{}
	if err := runtime.Validate(); err != nil {
		t.Fatal(err)
	}
	in.Managed.NativeState = target.Known("stopped")
	in.Managed.OwnedNativeState = target.Known("stopped")
	in.Managed.Fences = []data.QuiescenceFence{{State: data.FenceHeld}}
	before, err := in.Managed.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	planned, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if planned.Kind != Conflict || len(planned.Changes) != 0 || planned.Managed != nil || planned.Runtime != nil || planned.Backup != nil || len(planned.DataAllocations) != 0 || len(planned.DataSchemas) != 0 || len(planned.DataMounts) != 0 || !hasManagedConflict(planned, ManagedPublicationRequired) || hasManagedConflict(planned, ManagedEngineUnsupported) {
		t.Fatalf("unpublished plan misreported or acquired effects: kind=%s conflicts=%v", planned.Kind, planned.Conflicts)
	}
	after, err := in.Managed.CanonicalBytes()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("diagnostic changed protected facts", err)
	}
	runtime.ImageID = "invalid"
	invalid, err := Build(in)
	if err != nil || !hasManagedConflict(invalid, ManagedEngineUnsupported) || hasManagedConflict(invalid, ManagedPublicationRequired) || len(invalid.Changes) != 0 {
		t.Fatal("invalid runtime diagnosed as configured", invalid.Conflicts, err)
	}
}
