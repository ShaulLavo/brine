package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type NativeTaskInput struct {
	Requester datainit.Requester    `json:"requester"`
	Kind      ops.Kind              `json:"kind"`
	BindingID data.ReplicaBindingID `json:"binding_id"`
	PointID   string                `json:"point_id"`
}

func (i NativeTaskInput) valid() bool {
	if !i.Requester.Valid() || !data.ValidID(string(i.BindingID)) {
		return false
	}
	if i.Kind == ops.NativeRestoreTest {
		return data.ValidID(i.PointID)
	}
	return (i.Kind == ops.NativeCapture || i.Kind == ops.NativePrepare) && i.PointID == ""
}

func (s *Store) SaveNativeTaskInput(ctx context.Context, input NativeTaskInput) (string, error) {
	if s.readOnly || !input.valid() {
		return "", ErrInvalid
	}
	if _, err := s.ReadReservedEngine(ctx, input.BindingID); err != nil {
		return "", err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", ErrInvalid
	}
	id := nativePointHash(raw)
	_, err = s.db.ExecContext(ctx, "INSERT INTO data_native_task_inputs VALUES(?,?,?,?,?) ON CONFLICT(id) DO NOTHING", id, input.Requester.String(), input.Kind, input.BindingID, raw)
	return id, err
}

func readNativeTaskInput(ctx context.Context, q dataQuerier, operation ops.Operation) (NativeTaskInput, error) {
	var input NativeTaskInput
	var requester string
	var kind ops.Kind
	var binding data.ReplicaBindingID
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT requester,kind,binding_id,canonical FROM data_native_task_inputs WHERE id=?", operation.SecretRef).Scan(&requester, &kind, &binding, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return input, ErrNotFound
	}
	if err != nil {
		return input, err
	}
	if len(raw) == 0 || len(raw) > 4096 {
		return input, &IntegrityError{}
	}
	if _, err := strictjson.Object(raw, "requester", "kind", "binding_id", "point_id"); err != nil || json.Unmarshal(raw, &input) != nil || !input.valid() {
		return input, &IntegrityError{}
	}
	canonical, _ := json.Marshal(input)
	if !bytes.Equal(raw, canonical) || nativePointHash(raw) != operation.SecretRef || input.Requester.String() != requester || input.Kind != kind || input.BindingID != binding || requester != operation.Requester || kind != operation.Kind {
		return input, ErrConflict
	}
	return input, nil
}

func (s *Store) ReadNativeTaskInput(ctx context.Context, operationID string) (NativeTaskInput, error) {
	operation, err := s.GetOperation(ctx, operationID)
	if err != nil {
		return NativeTaskInput{}, err
	}
	return readNativeTaskInput(ctx, s.db, operation)
}
