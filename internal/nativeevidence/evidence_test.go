package nativeevidence

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func validRecord() record {
	return record{
		Format: Format, Outcome: "observed", Upstream: UpstreamCommit,
		Database: strings.Repeat("01", 32), Owner: strings.Repeat("02", 32), Kind: "wasm",
		ProgramAlgo: "keccak-256", ProgramDigest: strings.Repeat("03", 32), ProgramWire: strings.Repeat("03", 32), ProgramSHA: strings.Repeat("04", 32), ProgramBytes: 16,
		Offset: 1, Framing: "complete-v1", Algorithm: Algorithm, State: strings.Repeat("05", 32),
		Snapshot: &Snapshot{Database: strings.Repeat("01", 32), Replica: 1, Offset: 0},
		Tables:   []Table{{1, "st_table", "system", 2, 100, strings.Repeat("06", 32)}, {6, "st_module", "system", 1, 200, strings.Repeat("07", 32)}},
		Input:    Totals{3, 1000, 1000, 2}, Limits: MaximumLimits(),
	}
}

func encoded(t *testing.T, value record) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decoded(t *testing.T, value record) Evidence {
	t.Helper()
	evidence, err := Decode(encoded(t, value), value.Limits)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func TestEvidenceOwnsArraysAndSnapshotFacts(t *testing.T) {
	value := validRecord()
	raw := encoded(t, value)
	evidence, err := Decode(raw, value.Limits)
	if err != nil {
		t.Fatal(err)
	}
	for index := range raw {
		raw[index] = 0
	}
	tables := evidence.Tables()
	tables[0].Name = "forged"
	snapshot, ok := evidence.Snapshot()
	if !ok {
		t.Fatal("missing snapshot")
	}
	snapshot.Replica = 999
	if evidence.Tables()[0].Name != "st_table" {
		t.Fatal("table alias escaped")
	}
	current, _ := evidence.Snapshot()
	if current.Replica != 1 {
		t.Fatal("snapshot alias escaped")
	}
	if (Evidence{}).SamePersistedState(Evidence{}) {
		t.Fatal("zero evidence compared equal")
	}
}

func TestComparisonIncludesProgramIdentityHeadAndCatalog(t *testing.T) {
	baseline := decoded(t, validRecord())
	mutations := []func(*record){
		func(value *record) { value.Owner = strings.Repeat("08", 32) },
		func(value *record) { value.Kind = "js" },
		func(value *record) { value.ProgramSHA = strings.Repeat("08", 32) },
		func(value *record) { value.Offset++ },
		func(value *record) { value.Snapshot.Replica++ },
		func(value *record) { value.Snapshot = nil },
		func(value *record) { value.Tables[0].Blake3 = strings.Repeat("08", 32) },
		func(value *record) { value.Tables[0].Rows++ },
		func(value *record) { value.Tables[0].Type = "user" },
	}
	for index, change := range mutations {
		value := validRecord()
		change(&value)
		if baseline.SamePersistedState(decoded(t, value)) {
			t.Fatalf("comparison ignored mutation %d", index)
		}
	}
	compressed := validRecord()
	compressed.Input.StoredBytes = 500
	if !baseline.SamePersistedState(decoded(t, compressed)) {
		t.Fatal("physical compression changed state equality")
	}
	logs := validRecord()
	logs.Snapshot = nil
	if !decoded(t, logs).SamePersistedState(decoded(t, logs)) {
		t.Fatal("equal log states differ")
	}
}

func TestDecoderRefusesInvalidFormsAndInconsistentLimits(t *testing.T) {
	mutations := []func(*record){
		func(value *record) { value.Format = "old" },
		func(value *record) { value.Outcome = "refused" },
		func(value *record) { value.Upstream = strings.Repeat("0", 40) },
		func(value *record) { value.ProgramAlgo = "blake3" },
		func(value *record) { value.ProgramWire = strings.Repeat("09", 32) },
		func(value *record) { value.ProgramSHA = strings.Repeat("AB", 32) },
		func(value *record) { value.State = "short" },
		func(value *record) { value.Framing = "prefix" },
		func(value *record) { value.Snapshot.Database = strings.Repeat("09", 32) },
		func(value *record) { value.Snapshot.Offset = 2 },
		func(value *record) { value.Tables = nil },
		func(value *record) { value.Tables[1].Rows = 2 },
		func(value *record) { value.Tables[0].Rows = math.MaxUint64 },
		func(value *record) { value.Tables[0].BSATNBytes = math.MaxUint64 },
		func(value *record) { value.Tables[0].Type = "unknown" },
		func(value *record) { value.Input.Files = 0 },
		func(value *record) { value.Input.StoredBytes = math.MaxUint64 },
		func(value *record) { value.Limits.Files-- },
	}
	for index, change := range mutations {
		value := validRecord()
		change(&value)
		if _, err := Decode(encoded(t, value), MaximumLimits()); err == nil {
			t.Fatalf("accepted invalid case %d", index)
		}
	}
	value := validRecord()
	requested := MaximumLimits()
	requested.OutputBytes = 256
	if _, err := Decode(encoded(t, value), requested); err == nil {
		t.Fatal("ignored requested output ceiling")
	}
}

func TestNestedUnknownDuplicateNullAndAliasesRefuse(t *testing.T) {
	raw := string(encoded(t, validRecord()))
	negatives := []string{
		strings.Replace(raw, `"format":`, `"Format":`, 1),
		strings.Replace(raw, `"format":`, `"format":"duplicate","format":`, 1),
		strings.Replace(raw, `"table_id":1`, `"table_id":1,"table_id":1`, 1),
		raw + "{}",
		strings.Replace(raw, `"files":8192`, `"files":8192,"unknown":1`, 1),
		strings.Replace(raw, `"name":"st_table"`, `"name":null`, 1),
		strings.Replace(raw, `"table_type":"system",`, ``, 1),
		strings.Replace(raw, `"table_type":"system"`, `"Table_type":"system"`, 1),
		strings.Replace(raw, `"table_type":"system"`, `"table_type":null`, 1),
	}
	for index, value := range negatives {
		if _, err := Decode([]byte(value), MaximumLimits()); err == nil {
			t.Fatalf("accepted wire case %d", index)
		}
	}
}

func TestZeroLogCommitsRequireCompleteSelectedSnapshotFraming(t *testing.T) {
	value := validRecord()
	value.Framing = "snapshot-only-v1"
	value.Input.Commits = 0
	value.Offset = value.Snapshot.Offset
	baseline := decoded(t, value)
	if baseline.InputTotals().Commits != 0 || baseline.Offset() != value.Snapshot.Offset {
		t.Fatal("snapshot-only evidence fabricated log facts")
	}
	mutations := []func(*record){
		func(value *record) { value.Snapshot = nil },
		func(value *record) { value.Input.Commits = 1 },
		func(value *record) { value.Offset++ },
		func(value *record) { value.Framing = "complete-v1" },
	}
	for index, change := range mutations {
		invalid := validRecord()
		invalid.Framing = "snapshot-only-v1"
		invalid.Input.Commits = 0
		invalid.Offset = invalid.Snapshot.Offset
		change(&invalid)
		if _, err := Decode(encoded(t, invalid), invalid.Limits); err == nil {
			t.Fatalf("accepted unsupported zero-log case %d", index)
		}
	}
}

func TestExplicitReplicaZeroIsValidButAbsentReplicaStillRefuses(t *testing.T) {
	value := validRecord()
	value.Snapshot.Replica = 0
	full := decoded(t, value)
	snapshot, present := full.Snapshot()
	if !present || snapshot.Replica != 0 {
		t.Fatal("explicit persisted replica zero lost")
	}
	value.Framing = "snapshot-only-v1"
	value.Input.Commits = 0
	value.Offset = value.Snapshot.Offset
	decoded(t, value)
	raw := strings.Replace(string(encoded(t, value)), `,"observed_replica_id":0`, "", 1)
	if _, err := Decode([]byte(raw), value.Limits); err == nil {
		t.Fatal("missing replica selector became zero")
	}
}
