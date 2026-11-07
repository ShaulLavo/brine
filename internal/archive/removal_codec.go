package archive

import (
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

func removalObject(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	fields, err := strictjson.Object(raw, names...)
	if err != nil {
		return nil, ErrRefused
	}
	for _, field := range fields {
		if _, err := strictjson.Value[json.RawMessage](field); err != nil {
			return nil, ErrRefused
		}
	}
	return fields, nil
}

func (r *PreparedRef) UnmarshalJSON(raw []byte) error {
	fields, err := removalObject(raw, "database_id", "preparation_id", "prepared_sha256", "source_point_id", "fence", "credential_version", "destination_sha256", "retention_sha256", "location_sha256", "cutoff", "expires_at")
	if err != nil {
		return err
	}
	if _, err := removalObject(fields["fence"], "id", "incarnation_id", "database_id", "operation_id", "state"); err != nil {
		return err
	}
	type record PreparedRef
	var value record
	if json.Unmarshal(raw, &value) != nil {
		return ErrRefused
	}
	*r = PreparedRef(value)
	return nil
}

func (c *PreparedCoverage) UnmarshalJSON(raw []byte) error {
	if _, err := removalObject(raw, "ref", "claimed_at"); err != nil {
		return err
	}
	type record PreparedCoverage
	var value record
	if json.Unmarshal(raw, &value) != nil {
		return ErrRefused
	}
	*c = PreparedCoverage(value)
	return nil
}

func (c *SQLiteCoverage) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return ErrRefused
	}
	if prepared, exists := fields["prepared"]; exists {
		if _, err := strictjson.Value[json.RawMessage](prepared); err != nil {
			return ErrRefused
		}
	}
	type record SQLiteCoverage
	var value record
	if json.Unmarshal(raw, &value) != nil {
		return ErrRefused
	}
	*c = SQLiteCoverage(value)
	return nil
}
