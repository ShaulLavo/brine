package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/ops"
)

// LatestSecretVersion includes durable assignments from uncertain or failed
// attempts. A later operation must never reuse their names even if Podman has
// not yet exposed an indeterminate creation.
func (s *Store) LatestSecretVersion(ctx context.Context, app, ref string) (uint64, error) {
	if !ops.ValidIntent(ops.Intent{Kind: ops.SecretSet, App: app, SecretRef: ref}) {
		return 0, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.payload FROM events e JOIN operations o ON o.id=e.operation_id WHERE o.kind='secret_set' AND o.app=? AND o.secret_ref=? AND e.kind='secret_version' ORDER BY o.id,e.seq`, app, ref)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var latest uint64
	prefix := "brine." + app + "." + ref + ".v"
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return 0, err
		}
		var p ops.SecretVersionPayload
		if ops.ValidateEvent(ops.Event{Kind: "secret_version", Payload: raw}) != nil || json.Unmarshal(raw, &p) != nil {
			return 0, &IntegrityError{}
		}
		suffix, ok := strings.CutPrefix(p.Name, prefix)
		n, e := strconv.ParseUint(suffix, 10, 64)
		if !ok || e != nil {
			return 0, &IntegrityError{}
		}
		if n > latest {
			latest = n
		}
	}
	return latest, rows.Err()
}
