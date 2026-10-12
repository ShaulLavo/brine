package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

func checkNativeUntouchedHistory(ctx context.Context, q dataQuerier, current data.EngineRuntime) error {
	if current.State != data.EngineUnpublished {
		return ErrConflict
	}
	var epochs int
	err := q.QueryRowContext(ctx, "SELECT count(*) FROM data_native_epochs WHERE binding_id=?", current.BindingID).Scan(&epochs)
	if err != nil || epochs != 0 {
		return errors.Join(ErrConflict, err)
	}
	rows, err := q.QueryContext(ctx, "SELECT canonical FROM data_engine_runtime_commits WHERE binding_id=? ORDER BY version", current.BindingID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		if err := checkNativeUnpublishedCommit(raw, current); err != nil {
			return err
		}
		count++
	}
	if count == 0 {
		return ErrConflict
	}
	return rows.Err()
}

func checkNativeUnpublishedCommit(raw []byte, current data.EngineRuntime) error {
	var runtime data.EngineRuntime
	if len(raw) == 0 || len(raw) > 65536 || json.Unmarshal(raw, &runtime) != nil || runtime.Validate() != nil {
		return &IntegrityError{}
	}
	canonical, err := json.Marshal(runtime)
	if err != nil || !bytes.Equal(raw, canonical) {
		return &IntegrityError{}
	}
	if runtime.BindingID != current.BindingID || runtime.EpochID != current.EpochID || runtime.State != data.EngineUnpublished {
		return ErrConflict
	}
	return nil
}
