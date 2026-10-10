package spec

import "github.com/ShaulLavo/brine/internal/data"

type rawRestoreInvariant struct {
	Database any `toml:"database"`
	Kind     any `toml:"kind"`
	Table    any `toml:"table"`
	Column   any `toml:"column"`
	Count    any `toml:"count"`
	Minimum  any `toml:"minimum"`
	Maximum  any `toml:"maximum"`
}

func validateRestoreInvariants(raw rawApp, app *App) error {
	bad := func() error {
		return refusal("spec.invalid_restore_invariant", "restore_invariants", "invalid typed read-only restore check")
	}
	if len(raw.RestoreInvariants) > 64 {
		return bad()
	}
	for _, raw := range raw.RestoreInvariants {
		if raw == nil {
			return bad()
		}
		database, err := text(raw.Database, "restore_invariants.database")
		if err != nil {
			return err
		}
		found := false
		for _, d := range app.Databases {
			if string(d.Name) == database {
				found = true
			}
		}
		if !found {
			return bad()
		}
		kind, err := text(raw.Kind, "restore_invariants.kind")
		if err != nil {
			return err
		}
		table, err := text(raw.Table, "restore_invariants.table")
		if err != nil {
			return err
		}
		check := data.RestoreInvariant{Database: data.DatabaseName(database), Kind: kind, Table: table}
		if kind != "row_count" {
			check.Column, err = text(raw.Column, "restore_invariants.column")
			if err != nil {
				return err
			}
		} else if raw.Column != nil {
			return bad()
		}
		switch kind {
		case "row_count":
			if raw.Minimum != nil || raw.Maximum != nil {
				return bad()
			}
			check.Count, err = integer(raw.Count, "restore_invariants.count")
		case "non_null":
			if raw.Count != nil || raw.Minimum != nil || raw.Maximum != nil {
				return bad()
			}
		case "integer_range":
			if raw.Count != nil {
				return bad()
			}
			check.Minimum, err = integer(raw.Minimum, "restore_invariants.minimum")
			if err == nil {
				check.Maximum, err = integer(raw.Maximum, "restore_invariants.maximum")
			}
		default:
			return bad()
		}
		if err != nil || check.Validate() != nil {
			return bad()
		}
		app.RestoreInvariants = append(app.RestoreInvariants, check)
	}
	return nil
}
