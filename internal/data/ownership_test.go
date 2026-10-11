package data

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEngineObservationOwnsNestedAllocationProofs(t *testing.T) {
	input := &AllocationReceipt{RootProof: &RootEvidence{Device: 5, OperatorQuota: &QuotaObservation{LimitBytes: 100}}, MappingProof: &MappingEvidence{RunnerUID: 42}}
	definitions := []SQLiteSchemaDefinition{{Marker: "kept"}}
	target := SQLiteObservationTarget(SQLiteDatabaseBinding{}, definitions, input)
	input.RootProof.Device = 7
	input.RootProof.OperatorQuota.LimitBytes = 200
	input.MappingProof.RunnerUID = 99
	definitions[0].Marker = "changed"
	if target.allocation.RootProof.Device != 5 || target.allocation.RootProof.OperatorQuota.LimitBytes != 100 || target.allocation.MappingProof.RunnerUID != 42 || target.definitions[0].Marker != "kept" {
		t.Fatal("observation target retained caller-owned allocation evidence")
	}
}

func TestConcreteSQLiteBindingRejectsEveryRequiredNull(t *testing.T) {
	input := SQLiteDatabaseBinding{DatabaseID: DatabaseID(strings.Repeat("a", 32)), Name: "main", IncarnationID: AppIncarnationID(strings.Repeat("b", 32)), Root: "/srv/data", RelativeDirectory: "apps/fixture/databases/fixture", MountPath: "/data", Filename: "app.db", ReplicaBindingID: ReplicaBindingID(strings.Repeat("c", 32))}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil {
		t.Fatal("invalid fixture")
	}
	for name, original := range fields {
		fields[name] = nil
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		var binding SQLiteDatabaseBinding
		if json.Unmarshal(raw, &binding) == nil {
			t.Fatalf("required binding null accepted: %s", name)
		}
		fields[name] = original
	}
}
