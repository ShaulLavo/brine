package nativeevidence

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

const ProjectionFormat = "brine-confirmed-pairs-projection-v1"

type ConfirmedPairsProjection struct {
	Format string      `json:"format"`
	Left   PairTable   `json:"left"`
	Right  PairTable   `json:"right"`
	Marker MarkerTable `json:"marker"`
	Note   *NoteTable  `json:"note"`
}

type PairTable struct {
	ID     uint32    `json:"table_id"`
	Name   string    `json:"name"`
	Layout string    `json:"layout"`
	Rows   []PairRow `json:"rows"`
}

type PairRow struct {
	Sequence         uint64 `json:"sequence"`
	Marker           string `json:"marker"`
	CommitTimeMicros int64  `json:"commit_time_micros"`
	Scheduled        bool   `json:"scheduled"`
	Rating           *uint8 `json:"rating"`
}

type MarkerTable struct {
	ID   uint32      `json:"table_id"`
	Name string      `json:"name"`
	Rows []MarkerRow `json:"rows"`
}

type MarkerRow struct {
	ID       uint8  `json:"id"`
	Revision string `json:"revision"`
}

type NoteTable struct {
	ID   uint32    `json:"table_id"`
	Name string    `json:"name"`
	Rows []NoteRow `json:"rows"`
}

type NoteRow struct {
	ID   uint8  `json:"id"`
	Note string `json:"note"`
}

func (e Evidence) ConfirmedPairsProjection() (ConfirmedPairsProjection, bool) {
	if e.value.Projection == nil {
		return ConfirmedPairsProjection{}, false
	}
	return cloneProjection(*e.value.Projection), true
}

func clonePairTable(value PairTable) PairTable {
	value.Rows = slices.Clone(value.Rows)
	for index := range value.Rows {
		if value.Rows[index].Rating == nil {
			continue
		}
		rating := *value.Rows[index].Rating
		value.Rows[index].Rating = &rating
	}
	return value
}

func cloneProjection(value ConfirmedPairsProjection) ConfirmedPairsProjection {
	value.Left = clonePairTable(value.Left)
	value.Right = clonePairTable(value.Right)
	value.Marker.Rows = slices.Clone(value.Marker.Rows)
	if value.Note != nil {
		note := *value.Note
		note.Rows = slices.Clone(note.Rows)
		value.Note = &note
	}
	return value
}

func nullable(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func projectionObject(raw json.RawMessage, nullableField string, fields ...string) (map[string]json.RawMessage, error) {
	values, err := strictjson.Object(raw, fields...)
	if err != nil {
		return nil, err
	}
	for key, value := range values {
		if key != nullableField && nullable(value) {
			return nil, ErrEvidence
		}
	}
	return values, nil
}

func projectionRows(raw json.RawMessage, requested Limits, nullableField string, fields ...string) error {
	var rows []json.RawMessage
	if nullable(raw) || json.Unmarshal(raw, &rows) != nil || uint64(len(rows)) > requested.Rows {
		return ErrEvidence
	}
	for _, row := range rows {
		if _, err := projectionObject(row, nullableField, fields...); err != nil {
			return err
		}
	}
	return nil
}

func pairObject(raw json.RawMessage, requested Limits) error {
	fields, err := projectionObject(raw, "", "table_id", "name", "layout", "rows")
	if err != nil {
		return err
	}
	return projectionRows(fields["rows"], requested, "rating", "sequence", "marker", "commit_time_micros", "scheduled", "rating")
}

func textObject(raw json.RawMessage, requested Limits, text string) error {
	fields, err := projectionObject(raw, "", "table_id", "name", "rows")
	if err != nil {
		return err
	}
	return projectionRows(fields["rows"], requested, "", "id", text)
}

func projectionNested(raw json.RawMessage, requested Limits) error {
	if nullable(raw) {
		return nil
	}
	fields, err := projectionObject(raw, "note", "format", "left", "right", "marker", "note")
	if err != nil || pairObject(fields["left"], requested) != nil || pairObject(fields["right"], requested) != nil || textObject(fields["marker"], requested, "revision") != nil {
		return ErrEvidence
	}
	if !nullable(fields["note"]) {
		return textObject(fields["note"], requested, "note")
	}
	return nil
}

func matchesSummary(tables []Table, id uint32, name string, rows int) bool {
	for _, table := range tables {
		if table.ID == id && table.Name == name && table.Rows == uint64(rows) {
			return true
		}
	}
	return false
}

func validPairTable(value PairTable, name string, tables []Table, limits Limits) bool {
	if value.Name != name || !matchesSummary(tables, value.ID, name, len(value.Rows)) || value.Layout != "base-v1" && value.Layout != "rating-v1" {
		return false
	}
	for _, row := range value.Rows {
		if (row.Rating != nil) != (value.Layout == "rating-v1") || pairBytes(row) > limits.RowBytes {
			return false
		}
	}
	return true
}

func validateProjection(value record, limits Limits) error {
	p := value.Projection
	if p == nil {
		return nil
	}
	if p.Format != ProjectionFormat || !validPairTable(p.Left, "recovery_left", value.Tables, limits) || !validPairTable(p.Right, "recovery_right", value.Tables, limits) || p.Marker.Name != "schema_marker" || !matchesSummary(value.Tables, p.Marker.ID, p.Marker.Name, len(p.Marker.Rows)) {
		return ErrEvidence
	}
	if p.Note != nil && (p.Note.Name != "fixture_note" || !matchesSummary(value.Tables, p.Note.ID, p.Note.Name, len(p.Note.Rows))) {
		return ErrEvidence
	}
	if p.Note == nil && slices.ContainsFunc(value.Tables, func(table Table) bool { return table.Name == "fixture_note" }) {
		return ErrEvidence
	}
	rows := uint64(len(p.Left.Rows) + len(p.Right.Rows) + len(p.Marker.Rows))
	stateBytes := uint64(0)
	if p.Note != nil {
		rows += uint64(len(p.Note.Rows))
	}
	for _, table := range value.Tables {
		if table.Rows > limits.Rows || rows > limits.Rows-table.Rows || table.BSATNBytes > limits.StateBytes || stateBytes > limits.StateBytes-table.BSATNBytes {
			return ErrEvidence
		}
		rows += table.Rows
		stateBytes += table.BSATNBytes
	}
	return projectionBytes(*p, stateBytes, limits)
}

func pairBytes(row PairRow) uint64 {
	size := uint64(len(row.Marker)) + 21
	if row.Rating != nil {
		size++
	}
	return size
}

func chargeProjectionBytes(total *uint64, size uint64, limits Limits) error {
	if size > limits.RowBytes || size > limits.StateBytes || *total > limits.StateBytes-size {
		return ErrEvidence
	}
	*total += size
	return nil
}

func projectionBytes(p ConfirmedPairsProjection, total uint64, limits Limits) error {
	for _, table := range []PairTable{p.Left, p.Right} {
		if err := pairTableBytes(table, &total, limits); err != nil {
			return err
		}
	}
	for _, row := range p.Marker.Rows {
		if err := chargeProjectionBytes(&total, uint64(len(row.Revision))+5, limits); err != nil {
			return err
		}
	}
	if p.Note == nil {
		return nil
	}
	for _, row := range p.Note.Rows {
		if err := chargeProjectionBytes(&total, uint64(len(row.Note))+5, limits); err != nil {
			return err
		}
	}
	return nil
}

func pairTableBytes(table PairTable, total *uint64, limits Limits) error {
	for _, row := range table.Rows {
		if err := chargeProjectionBytes(total, pairBytes(row), limits); err != nil {
			return err
		}
	}
	return nil
}

func sameProjection(left, right *ConfirmedPairsProjection) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	l, errLeft := json.Marshal(left)
	r, errRight := json.Marshal(right)
	return errLeft == nil && errRight == nil && bytes.Equal(l, r)
}
