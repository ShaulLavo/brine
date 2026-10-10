package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
)

// readReplicaCursor shares bounded canonical decoding, not activation policy.
func readReplicaCursor[T any](row *sql.Row, limit int, valid func(T) bool) (T, error) {
	var zero, record T
	var raw []byte
	err := row.Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	if len(raw) > limit || json.Unmarshal(raw, &record) != nil || !valid(record) {
		return zero, &IntegrityError{}
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(raw, canonical) {
		return zero, &IntegrityError{}
	}
	return record, nil
}
