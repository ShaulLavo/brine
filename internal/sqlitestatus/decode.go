package sqlitestatus

import (
	"bytes"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

// Decode checks the complete machine contract, including nested closed objects.
func Decode(raw []byte) (Status, error) {
	var s Status
	if len(raw) > 64<<10 {
		return s, strictjson.ErrObject
	}
	f, err := strictjson.Object(raw, "engine", "app", "database", "database_id", "binding_id", "epoch_id", "observed_at", "state", "committed", "service", "remote", "remote_watermark", "credential", "lifecycle", "held_fences", "last_verified", "local_position", "position_distance", "loss", "reasons")
	if err != nil {
		return s, strictjson.ErrObject
	}
	for name, fields := range map[string][]string{
		"service": {"state", "observed_at"}, "remote": {"state", "observed_at"}, "remote_watermark": {"state", "txid", "observed_at"},
		"credential": {"state", "version", "received_at", "expires_at", "remaining_seconds", "renewal_needed"}, "lifecycle": {"state", "verified_at", "fresh_until"},
	} {
		if _, err := strictjson.Object(f[name], fields...); err != nil {
			return s, err
		}
	}
	if !bytes.Equal(bytes.TrimSpace(f["last_verified"]), []byte("null")) {
		if _, err := strictjson.Object(f["last_verified"], "operation_id", "source", "verified_at", "invariant_check"); err != nil {
			return s, err
		}
	}
	var fences []json.RawMessage
	if json.Unmarshal(f["held_fences"], &fences) != nil {
		return s, strictjson.ErrObject
	}
	for _, fence := range fences {
		if _, err := strictjson.Object(fence, "id", "owner_operation"); err != nil {
			return s, err
		}
	}
	if json.Unmarshal(raw, &s) != nil || !s.Valid() {
		return Status{}, strictjson.ErrObject
	}
	return s, nil
}
