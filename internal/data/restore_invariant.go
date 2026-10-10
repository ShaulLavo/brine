package data

import "regexp"

type RestoreInvariant struct {
	Database DatabaseName `json:"database"`
	Kind     string       `json:"kind"`
	Table    string       `json:"table"`
	Column   string       `json:"column,omitempty"`
	Count    int64        `json:"count,omitempty"`
	Minimum  int64        `json:"minimum,omitempty"`
	Maximum  int64        `json:"maximum,omitempty"`
}

var sqlIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func (i RestoreInvariant) Validate() error {
	if !namePattern.MatchString(string(i.Database)) || !sqlIdentifier.MatchString(i.Table) {
		return ErrInvalid
	}
	switch i.Kind {
	case "row_count":
		if i.Count < 0 || i.Column != "" || i.Minimum != 0 || i.Maximum != 0 {
			return ErrInvalid
		}
	case "non_null":
		if !sqlIdentifier.MatchString(i.Column) || i.Count != 0 || i.Minimum != 0 || i.Maximum != 0 {
			return ErrInvalid
		}
	case "integer_range":
		if !sqlIdentifier.MatchString(i.Column) || i.Count != 0 || i.Minimum > i.Maximum {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
