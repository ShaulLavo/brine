package store

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestManagedDesiredRunningAuthorityIsExactAndIndependent(t *testing.T) {
	binding := data.ReplicaBindingID(strings.Repeat("a", 32))
	epoch := data.ReplicaEpochID(strings.Repeat("b", 32))
	runtime := strings.Repeat("c", 64)
	wanted := ManagedDesiredState{BindingID: binding, EpochID: epoch, RuntimeSHA256: runtime, Revision: 1, PlanID: "sha256:" + strings.Repeat("d", 64), OperationID: "operation", EngineRunning: true}
	if !wanted.Valid() || !wanted.Current(binding, epoch, runtime) || wanted.FrontendRunning || wanted.IngressServing {
		t.Fatal("separate engine intent did not remain exact")
	}
	if wanted.Current(binding, data.ReplicaEpochID(strings.Repeat("e", 32)), runtime) || wanted.Current(binding, epoch, strings.Repeat("f", 64)) {
		t.Fatal("historical running stamp granted incoming data transition")
	}
	empty := ManagedDesiredState{}
	if empty.Current(binding, epoch, runtime) {
		t.Fatal("missing desiredstate granted live authority")
	}
	wanted.EngineRunning = false
	wanted.FrontendRunning = true
	if wanted.Valid() {
		t.Fatal("frontend can start with desired engine stopped")
	}
	wanted.FrontendRunning = false
	wanted.IngressServing = true
	if wanted.Valid() {
		t.Fatal("ingress can serve with desired engine stopped")
	}
	wanted.IngressServing = false
	wanted.RuntimeSHA256 = ""
	if !wanted.Valid() {
		t.Fatal("configured unpublished paused state refused")
	}
	wanted.EngineRunning = true
	if wanted.Valid() {
		t.Fatal("unpublished paused state gained start permission")
	}
}
