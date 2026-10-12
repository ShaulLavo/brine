package nativeevidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func projectionRecord(rating bool, note bool) record {
	value := validRecord()
	row := PairRow{Sequence: 1, Marker: "brine-p09-00000001", CommitTimeMicros: 1700000000000001, Scheduled: false}
	layout := "base-v1"
	if rating {
		r := uint8(7)
		row.Rating = &r
		layout = "rating-v1"
	}
	value.Projection = &ConfirmedPairsProjection{
		Format:    ProjectionFormat,
		Left:      PairTable{10, "recovery_left", layout, []PairRow{row}},
		Right:     PairTable{11, "recovery_right", layout, []PairRow{row}},
		Marker:    MarkerTable{12, "schema_marker", []MarkerRow{{1, "brine-recovery-v1"}}},
		Publisher: PublisherTable{13, "publisher", []PublisherRow{{1, value.Owner}}},
		Sequence:  SequenceTable{14, "sequence_state", []SequenceRow{{1, 1}}},
	}
	value.Tables = append(value.Tables, Table{10, "recovery_left", 1, 64, strings.Repeat("08", 32)}, Table{11, "recovery_right", 1, 64, strings.Repeat("09", 32)}, Table{12, "schema_marker", 1, 32, strings.Repeat("0a", 32)})
	value.Tables = append(value.Tables, Table{13, "publisher", 1, 33, strings.Repeat("0c", 32)}, Table{14, "sequence_state", 1, 9, strings.Repeat("0d", 32)})
	if note {
		value.Projection.Note = &NoteTable{15, "fixture_note", []NoteRow{}}
		value.Tables = append(value.Tables, Table{15, "fixture_note", 0, 0, strings.Repeat("0b", 32)})
	}
	return value
}

func TestProjectionOwnsFactsAndCanonicalWholeEvidence(t *testing.T) {
	value := projectionRecord(true, true)
	raw := encoded(t, value)
	evidence, err := Decode(raw, value.Limits)
	if err != nil {
		t.Fatal(err)
	}
	projection, ok := evidence.ConfirmedPairsProjection()
	if !ok || projection.Left.Rows[0].CommitTimeMicros != 1700000000000001 {
		t.Fatal("lost actual row values")
	}
	projection.Left.Name = "forged"
	projection.Left.Rows[0].Marker = "forged"
	*projection.Left.Rows[0].Rating = 9
	projection.Marker.Rows[0].Revision = "forged"
	projection.Note.Name = "forged"
	projection.Publisher.Rows[0].Identity = strings.Repeat("ff", 32)
	projection.Sequence.Rows[0].Sequence = 99
	canonical := evidence.Bytes()
	want := bytes.Clone(canonical)
	for index := range canonical {
		canonical[index] = 0
	}
	for index := range raw {
		raw[index] = 0
	}
	current, _ := evidence.ConfirmedPairsProjection()
	if current.Left.Name != "recovery_left" || current.Left.Rows[0].Marker != "brine-p09-00000001" || *current.Left.Rows[0].Rating != 7 || current.Marker.Rows[0].Revision != "brine-recovery-v1" || current.Note.Name != "fixture_note" {
		t.Fatal("getter aliases evidence")
	}
	if current.Publisher.Rows[0].Identity != value.Owner || current.Sequence.Rows[0].Sequence != 1 {
		t.Fatal("control row aliases evidence")
	}
	digest := sha256.Sum256(want)
	if evidence.SHA256() != hex.EncodeToString(digest[:]) || !bytes.Equal(evidence.Bytes(), want) {
		t.Fatal("canonical evidence mutated")
	}
	var spaced bytes.Buffer
	if err := json.Indent(&spaced, want, "", "  "); err != nil {
		t.Fatal(err)
	}
	again, err := Decode(spaced.Bytes(), value.Limits)
	if err != nil || again.SHA256() != evidence.SHA256() {
		t.Fatal("canonical digest depends on input whitespace")
	}
	if (Evidence{}).Bytes() != nil || (Evidence{}).SHA256() != "" {
		t.Fatal("zero evidence has fabricated record")
	}
}

func TestProjectionIsRequiredNullableAndStrictAtEveryLevel(t *testing.T) {
	raw := string(encoded(t, projectionRecord(true, true)))
	negatives := []string{
		strings.Replace(raw, `"format":"brine-confirmed-pairs-projection-v1"`, `"Format":"brine-confirmed-pairs-projection-v1"`, 1),
		strings.Replace(raw, `"left":`, `"left":null,"left":`, 1),
		strings.Replace(raw, `"sequence":1`, `"sequence":1,"extra":0`, 1),
		strings.Replace(raw, `"rating":7`, `"Rating":7`, 1),
		strings.Replace(raw, `"scheduled":false`, `"scheduled":null`, 1),
		strings.Replace(raw, `"commit_time_micros":1700000000000001`, `"commit_time_micros":"1700000000000001"`, 1),
		strings.Replace(raw, `"id":1,"revision":`, `"id":1,"id":1,"revision":`, 1),
		strings.Replace(raw, `"rows":[]`, `"rows":null`, 1),
	}
	for index, negative := range negatives {
		if _, err := Decode([]byte(negative), MaximumLimits()); err == nil {
			t.Fatalf("accepted malformed projection %d", index)
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded(t, validRecord()), &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "confirmed_pairs_projection")
	missing, _ := json.Marshal(fields)
	if _, err := Decode(missing, MaximumLimits()); err == nil {
		t.Fatal("old output without required projection accepted")
	}
	if _, present := decoded(t, validRecord()).ConfirmedPairsProjection(); present {
		t.Fatal("null became facts")
	}
}

func TestProjectionSummaryShapeAndBudgetMismatchesRefuse(t *testing.T) {
	mutations := []func(*record){
		func(v *record) { v.Projection.Format = "unknown" },
		func(v *record) { v.Projection.Left.ID = 11 },
		func(v *record) { v.Projection.Left.Name = "other" },
		func(v *record) { v.Projection.Left.Layout = "unknown" },
		func(v *record) { v.Projection.Left.Rows = append(v.Projection.Left.Rows, v.Projection.Left.Rows[0]) },
		func(v *record) { v.Projection.Left.Rows[0].Rating = nil },
		func(v *record) { v.Projection.Marker.Name = "other" },
		func(v *record) { v.Projection.Note = nil },
		func(v *record) { v.Projection.Note.ID = 10 },
		func(v *record) { v.Projection.Publisher.Name = "other" },
		func(v *record) { v.Projection.Publisher.Rows[0].Identity = strings.Repeat("FF", 32) },
		func(v *record) { v.Projection.Sequence.ID = 13 },
		func(v *record) { v.Limits.Rows = 7 },
		func(v *record) { v.Limits.StateBytes = 500 },
	}
	for index, mutate := range mutations {
		value := projectionRecord(true, true)
		mutate(&value)
		if _, err := Decode(encoded(t, value), value.Limits); err == nil {
			t.Fatalf("accepted inconsistent projection %d", index)
		}
	}
	base := projectionRecord(false, false)
	r := uint8(7)
	base.Projection.Left.Rows[0].Rating = &r
	if _, err := Decode(encoded(t, base), base.Limits); err == nil {
		t.Fatal("base layout accepted rating")
	}
}

func TestProjectionDecoderDoesNotClaimPairDomainAuthority(t *testing.T) {
	value := projectionRecord(false, false)
	value.Projection.Left.Rows[0].Sequence = 99
	value.Projection.Left.Rows[0].Marker = "actual-other-marker"
	value.Projection.Left.Rows[0].CommitTimeMicros = -1
	evidence := decoded(t, value)
	p, _ := evidence.ConfirmedPairsProjection()
	if p.Left.Rows[0].Sequence != 99 || p.Left.Rows[0].CommitTimeMicros != -1 {
		t.Fatal("decoder replaced observed facts")
	}
	baseline := decoded(t, projectionRecord(false, false))
	if evidence.SamePersistedState(baseline) {
		t.Fatal("full comparison ignored projection")
	}
}

func TestNativeTableNamesAreUniqueBeforeConsumersMapThem(t *testing.T) {
	value := projectionRecord(false, false)
	value.Tables = append(value.Tables, Table{15, "recovery_left", 0, 0, strings.Repeat("0b", 32)})
	if _, err := Decode(encoded(t, value), value.Limits); err == nil {
		t.Fatal("accepted duplicate table name with another table ID")
	}
}
