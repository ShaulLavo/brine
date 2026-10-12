package spacetimedb

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/nativeevidence"
)

func registeredRecoverySchema(t testing.TB, variant string) (CanonicalSchema, string) {
	t.Helper()
	root, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	raw, err := root.ReadFile("descriptor-" + variant + ".json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := DecodeDescriptorV10(raw, DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("testdata/descriptor-pins.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins struct {
		Inputs []struct {
			Variant string `json:"variant"`
			Wasm    string `json:"wasm_sha256"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(raw, &pins); err != nil {
		t.Fatal(err)
	}
	for _, input := range pins.Inputs {
		if input.Variant == variant {
			return schema, input.Wasm
		}
	}
	t.Fatal("missing fixture pin")
	return CanonicalSchema{}, ""
}

func passiveRecoveryFixture(t testing.TB, variant string) (map[string]any, CanonicalSchema) {
	t.Helper()
	schema, wasm := registeredRecoverySchema(t, variant)
	rows := []nativeevidence.PairRow{
		{Sequence: 1, Marker: "brine-p09-00000001", CommitTimeMicros: 1791772424950729},
		{Sequence: 2, Marker: "brine-p09-00000002", CommitTimeMicros: 1791772424951082},
		{Sequence: 3, Marker: "brine-p09-00000003", CommitTimeMicros: 1791772425952917, Scheduled: true},
	}
	layout := "base-v1"
	if variant == "default-column" {
		layout = "rating-v1"
		for index := range rows {
			rating := uint8(7)
			rows[index].Rating = &rating
		}
	}
	projection := nativeevidence.ConfirmedPairsProjection{
		Format:    nativeevidence.ProjectionFormat,
		Left:      nativeevidence.PairTable{ID: 10, Name: "recovery_left", Layout: layout, Rows: rows},
		Right:     nativeevidence.PairTable{ID: 11, Name: "recovery_right", Layout: layout, Rows: slices.Clone(rows)},
		Marker:    nativeevidence.MarkerTable{ID: 12, Name: "schema_marker", Rows: []nativeevidence.MarkerRow{{ID: 1, Revision: RecoverySchemaMarker}}},
		Publisher: nativeevidence.PublisherTable{ID: 13, Name: "publisher", Rows: []nativeevidence.PublisherRow{{ID: 1, Identity: publisherHex}}},
		Sequence:  nativeevidence.SequenceTable{ID: 14, Name: "sequence_state", Rows: []nativeevidence.SequenceRow{{ID: 1, Sequence: 3}}},
	}
	tables := []nativeevidence.Table{
		{ID: 1, Name: "st_table", Rows: 2, BSATNBytes: 100, Blake3: strings.Repeat("01", 32)},
		{ID: 6, Name: "st_module", Rows: 1, BSATNBytes: 200, Blake3: strings.Repeat("02", 32)},
		{ID: 10, Name: "recovery_left", Rows: 3, BSATNBytes: 192, Blake3: strings.Repeat("03", 32)},
		{ID: 11, Name: "recovery_right", Rows: 3, BSATNBytes: 192, Blake3: strings.Repeat("04", 32)},
		{ID: 12, Name: "schema_marker", Rows: 1, BSATNBytes: 32, Blake3: strings.Repeat("05", 32)},
		{ID: 13, Name: "publisher", Rows: 1, BSATNBytes: 32, Blake3: strings.Repeat("06", 32)},
		{ID: 14, Name: "sequence_state", Rows: 1, BSATNBytes: 16, Blake3: strings.Repeat("07", 32)},
		{ID: 15, Name: "scheduled_write", Rows: 0, Blake3: strings.Repeat("08", 32)},
		{ID: 16, Name: "writer", Rows: 1, BSATNBytes: 32, Blake3: strings.Repeat("09", 32)},
	}
	if variant == "additive" {
		projection.Note = &nativeevidence.NoteTable{ID: 17, Name: "fixture_note", Rows: []nativeevidence.NoteRow{}}
		tables = append(tables, nativeevidence.Table{ID: 17, Name: "fixture_note", Blake3: strings.Repeat("10", 32)})
	}
	return map[string]any{
		"format": nativeevidence.Format, "outcome": "observed", "upstream_commit": nativeevidence.UpstreamCommit,
		"observed_database_identity": databaseHex, "observed_owner_identity": publisherHex, "current_program_kind": "wasm",
		"current_program_algorithm": "keccak-256", "current_program_digest": strings.Repeat("03", 32),
		"current_program_hash": strings.Repeat("03", 32), "current_program_sha256": wasm, "program_bytes": 16,
		"committed_offset": 8, "framing": "complete-v1", "snapshot": nil, "persisted_connected_clients": 0,
		"evidence_algorithm": nativeevidence.Algorithm, "state_blake3": strings.Repeat("11", 32), "tables": tables,
		"input":  nativeevidence.Totals{Files: 3, StoredBytes: 1000, ExpandedBytes: 1000, Commits: 2},
		"limits": nativeevidence.MaximumLimits(), "confirmed_pairs_projection": projection,
	}, schema
}

func decodePassiveRecoveryFixture(t testing.TB, value map[string]any) nativeevidence.Evidence {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := nativeevidence.Decode(raw, nativeevidence.MaximumLimits())
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func recoveredPairFixture(t testing.TB, variant string) RecoveredPairs {
	t.Helper()
	value, schema := passiveRecoveryFixture(t, variant)
	recovered, err := RecoveryPairsFromPassive(decodePassiveRecoveryFixture(t, value), schema)
	if err != nil {
		t.Fatal(err)
	}
	return recovered
}

func TestPassiveRecoveryPairsProveFiniteValuesAndOwnBytes(t *testing.T) {
	for _, variant := range []string{"v1", "additive", "default-column"} {
		pairs := recoveredPairFixture(t, variant)
		if pairs.RowCount() != 3 || pairs.Offset() != 8 || pairs.SHA256() == "" || pairs.NativeEvidenceSHA256() == "" {
			t.Fatal("missing passive row/native scope")
		}
		raw := pairs.Bytes()
		raw[0] = 'x'
		if pairs.Bytes()[0] != '{' {
			t.Fatal("caller changed passive proof bytes")
		}
	}
}

func TestPassivePairsRefuseMismatchesAndUnqualifiedShapes(t *testing.T) {
	cases := map[string]func(*nativeevidence.ConfirmedPairsProjection){
		"right timestamp":         func(p *nativeevidence.ConfirmedPairsProjection) { p.Right.Rows[0].CommitTimeMicros++ },
		"right scheduled":         func(p *nativeevidence.ConfirmedPairsProjection) { p.Right.Rows[0].Scheduled = true },
		"pair duplicate":          func(p *nativeevidence.ConfirmedPairsProjection) { p.Left.Rows[1] = p.Left.Rows[0] },
		"negative timestamp":      func(p *nativeevidence.ConfirmedPairsProjection) { p.Left.Rows[0].CommitTimeMicros = -1 },
		"wrong marker":            func(p *nativeevidence.ConfirmedPairsProjection) { p.Marker.Rows[0].Revision = "other" },
		"wrong publisher":         func(p *nativeevidence.ConfirmedPairsProjection) { p.Publisher.Rows[0].Identity = databaseHex },
		"wrong publisher id":      func(p *nativeevidence.ConfirmedPairsProjection) { p.Publisher.Rows[0].ID = 2 },
		"sequence ahead of pairs": func(p *nativeevidence.ConfirmedPairsProjection) { p.Sequence.Rows[0].Sequence++ },
		"wrong sequence id":       func(p *nativeevidence.ConfirmedPairsProjection) { p.Sequence.Rows[0].ID = 2 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) { checkPassivePairRefusal(t, mutate) })
	}
	value, schema := passiveRecoveryFixture(t, "default-column")
	projection := value["confirmed_pairs_projection"].(nativeevidence.ConfirmedPairsProjection)
	wrong := uint8(8)
	projection.Left.Rows[0].Rating = &wrong
	value["confirmed_pairs_projection"] = projection
	if _, err := RecoveryPairsFromPassive(decodePassiveRecoveryFixture(t, value), schema); !errors.Is(err, ErrRecoveryPairs) {
		t.Fatal("incorrect default rating admitted")
	}
}

func checkPassivePairRefusal(t *testing.T, mutate func(*nativeevidence.ConfirmedPairsProjection)) {
	t.Helper()
	value, schema := passiveRecoveryFixture(t, "v1")
	projection := value["confirmed_pairs_projection"].(nativeevidence.ConfirmedPairsProjection)
	mutate(&projection)
	value["confirmed_pairs_projection"] = projection
	if _, err := RecoveryPairsFromPassive(decodePassiveRecoveryFixture(t, value), schema); !errors.Is(err, ErrRecoveryPairs) {
		t.Fatal("invalid passive finite state admitted")
	}
}

func TestConfirmedLowerBoundCannotClaimFinalityOrLoseRows(t *testing.T) {
	final := recoveredPairFixture(t, "v1")
	value := confirmedRecordFixture(t)
	value.Program = final.value.Program
	value.Rows = value.Rows[:2]
	lower, err := newConfirmedPairsRecord(value)
	if err != nil || CheckConfirmedLowerBound(lower, final) != nil {
		t.Fatal("final scheduler advance violated confirmed lower bound", err)
	}
	value.Rows[0].CommitTime = "1791772424950730"
	wrong, err := newConfirmedPairsRecord(value)
	if err != nil || !errors.Is(CheckConfirmedLowerBound(wrong, final), ErrRecoveryPairs) {
		t.Fatal("rewritten confirmed row admitted", err)
	}
	if !errors.Is(CheckConfirmedLowerBound(ConfirmedPairsRecord{}, final), ErrRecoveryPairs) || !errors.Is(CheckRecoveredPairsEqual(final, RecoveredPairs{}), ErrRecoveryPairs) {
		t.Fatal("zero facts granted equality")
	}
}

func TestRecoveryPreservationProvesOldColumnsAndQualifiedDefaults(t *testing.T) {
	source := recoveredPairFixture(t, "v1")
	for _, variant := range []string{"additive", "default-column"} {
		staged := recoveredPairFixture(t, variant)
		proof, err := CheckRecoveryPairPreservation(source, staged)
		if err != nil || proof.SHA256() == "" || !slices.Contains(proof.Requirements(), VerifyPreservedData) {
			t.Fatal("preserved finite data refused", variant, err)
		}
		if slices.Contains(proof.Requirements(), ReviewGuestCode) || slices.Contains(proof.Requirements(), QualifySchedulerEffects) {
			t.Fatal("data proof granted guest/effects review")
		}
	}
	value, schema := passiveRecoveryFixture(t, "default-column")
	tables := value["tables"].([]nativeevidence.Table)
	tables[5].Blake3 = strings.Repeat("ff", 32)
	value["tables"] = tables
	staged, err := RecoveryPairsFromPassive(decodePassiveRecoveryFixture(t, value), schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CheckRecoveryPairPreservation(source, staged); !errors.Is(err, ErrRecoveryPairs) {
		t.Fatal("unrelated publisher data change hidden by pair projection")
	}
}

func TestFinitePairEqualityKeepsCompleteStateProofSeparate(t *testing.T) {
	value, schema := passiveRecoveryFixture(t, "v1")
	sourceNative := decodePassiveRecoveryFixture(t, value)
	source, err := RecoveryPairsFromPassive(sourceNative, schema)
	if err != nil {
		t.Fatal(err)
	}
	value["state_blake3"] = strings.Repeat("ff", 32)
	otherNative := decodePassiveRecoveryFixture(t, value)
	other, err := RecoveryPairsFromPassive(otherNative, schema)
	if err != nil || CheckRecoveredPairsEqual(source, other) != nil {
		t.Fatal("unchanged finite pairs refused", err)
	}
	if sourceNative.SamePersistedState(otherNative) {
		t.Fatal("finite marker equality substituted for complete persisted-state equality")
	}
}
