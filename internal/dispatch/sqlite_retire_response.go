package dispatch

import (
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

func decodeSQLiteRetireInspection(raw []byte) (archive.Inspection, error) {
	var i archive.Inspection
	fields, err := strictjson.OptionalObject(raw, []string{"disposition", "runtime", "trees", "can_reconcile"}, []string{"preparation"})
	if err != nil {
		return i, err
	}
	if _, err = sqliteRetireResponseObject(fields["disposition"], []string{"archive_id", "sequence", "stage", "tree_index"}, []string{"committed_at", "retain_until"}); err != nil {
		return i, err
	}
	if _, err = sqliteRetireResponseObject(fields["runtime"], []string{"complete"}, nil); err != nil {
		return i, err
	}
	if prep, ok := fields["preparation"]; ok {
		if _, err = sqliteRetireResponseObject(prep, []string{"stage", "sequence", "manifest", "can_abandon"}, []string{"closed_reason"}); err != nil {
			return i, err
		}
	}
	var trees []json.RawMessage
	if json.Unmarshal(fields["trees"], &trees) != nil || len(trees) > 16 {
		return i, strictjson.ErrObject
	}
	for _, tree := range trees {
		if _, err = sqliteRetireResponseObject(tree, []string{"index", "location"}, nil); err != nil {
			return i, err
		}
	}
	if json.Unmarshal(raw, &i) != nil || !data.ValidID(string(i.Disposition.ArchiveID)) {
		return i, strictjson.ErrObject
	}
	if _, err = strictjson.Value[bool](fields["can_reconcile"]); err != nil {
		return archive.Inspection{}, err
	}
	if err = validSQLiteRetireInspection(i); err != nil {
		return archive.Inspection{}, err
	}
	return i, nil
}
func validSQLiteRetireInspection(i archive.Inspection) error {
	if !validSQLiteRetireDisposition(i.Disposition) || !validSQLiteRetirePreparation(i) {
		return strictjson.ErrObject
	}
	for n, tree := range i.Trees {
		if tree.Index != n || tree.Location != "source" && tree.Location != "archive" && tree.Location != "unknown" {
			return strictjson.ErrObject
		}
	}
	return nil
}

func validSQLiteRetireDisposition(d archive.Disposition) bool {
	if d.TreeIndex < 0 || d.TreeIndex > 15 {
		return false
	}
	switch d.Stage {
	case "":
		return d.Sequence == 0 && d.TreeIndex == 0 && d.CommittedAt.IsZero() && d.RetainUntil.IsZero()
	case archive.Retired:
		return d.Sequence > 0 && !d.CommittedAt.IsZero() && d.RetainUntil.Equal(d.CommittedAt.Add(30*24*time.Hour))
	case archive.Abandoned:
		return d.Sequence > 0 && !d.CommittedAt.IsZero() && d.RetainUntil.IsZero()
	case archive.CleanupIntent, archive.RuntimeRetired, archive.RenameIntent, archive.Renamed, archive.AbandonIntent:
		return d.Sequence > 0 && d.CommittedAt.IsZero() && d.RetainUntil.IsZero()
	default:
		return false
	}
}

func validSQLiteRetirePreparation(i archive.Inspection) bool {
	terminal := i.Disposition.Stage == archive.Retired || i.Disposition.Stage == archive.Abandoned
	if i.Preparation == nil {
		return terminal && !i.CanReconcile
	}
	if terminal {
		return false
	}
	p := i.Preparation
	stage, ok := archive.StageAt(p.Sequence)
	if !ok || stage != p.Stage || p.Manifest != (stage == archive.Prepared) && p.ClosedReason != "manifest_unavailable" || i.CanReconcile && !p.Manifest {
		return false
	}
	switch p.ClosedReason {
	case "", "runtime_unknown", "tree_conflict", "manifest_unavailable", "continue_unavailable":
	default:
		return false
	}
	pristine := i.Disposition.Stage == "" || i.Disposition.Stage == archive.CleanupIntent || i.Disposition.Stage == archive.AbandonIntent
	return !p.CanAbandon || pristine
}

func sqliteRetireResponseObject(raw []byte, required, optional []string) (map[string]json.RawMessage, error) {
	fields, err := strictjson.OptionalObject(raw, required, optional)
	if err != nil {
		return nil, err
	}
	for _, field := range fields {
		if _, err := strictjson.Value[json.RawMessage](field); err != nil {
			return nil, err
		}
	}
	return fields, nil
}
