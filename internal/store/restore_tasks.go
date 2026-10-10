package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

func (s *Store) SaveRestoreTaskInput(ctx context.Context, requester string, input ops.RestoreTaskInput) (string, error) {
	if s.readOnly || requester == "" || len(requester) > 256 || !input.Valid() {
		return "", ErrInvalid
	}
	canonical, err := json.Marshal(input)
	if err != nil || len(canonical) > 4096 {
		return "", ErrInvalid
	}
	digest := sha256.Sum256(append([]byte(requester+"\x00"), canonical...))
	id := hex.EncodeToString(digest[:])
	_, err = s.db.ExecContext(ctx, "INSERT INTO restore_task_inputs VALUES(?,?,?) ON CONFLICT(id) DO NOTHING", id, requester, canonical)
	return id, err
}
func (s *Store) LoadRestoreTaskInput(ctx context.Context, id, requester string) (ops.RestoreTaskInput, error) {
	var canonical []byte
	if err := s.db.QueryRowContext(ctx, "SELECT canonical FROM restore_task_inputs WHERE id=? AND requester=?", id, requester).Scan(&canonical); err != nil {
		return ops.RestoreTaskInput{}, err
	}
	if len(canonical) > 4096 {
		return ops.RestoreTaskInput{}, ErrInvalid
	}
	if _, err := strictjson.Object(canonical, "app", "database", "txid", "point"); err != nil {
		return ops.RestoreTaskInput{}, ErrInvalid
	}
	input, err := strictjson.Value[ops.RestoreTaskInput](canonical)
	if err != nil || !input.Valid() {
		return ops.RestoreTaskInput{}, ErrInvalid
	}
	digest := sha256.Sum256(append([]byte(requester+"\x00"), canonical...))
	if hex.EncodeToString(digest[:]) != id {
		return ops.RestoreTaskInput{}, ErrInvalid
	}
	return input, nil
}
