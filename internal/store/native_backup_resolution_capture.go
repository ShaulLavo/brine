package store

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/ops"
	"time"
)

func (i *NativeBackupInspection) Capture(ctx context.Context) (*NativeOnlineCapture, error) {
	if i.original.Kind != ops.NativeCapture {
		return nil, ErrConflict
	}
	if err := i.check(ctx, i.state.db); err != nil {
		return nil, err
	}
	var raw []byte
	var point string
	if err := i.state.db.QueryRowContext(ctx, "SELECT id,canonical FROM data_native_capture_inputs WHERE operation_id=? AND kind='online'", i.original.ID).Scan(&point, &raw); err != nil {
		return nil, ErrConflict
	}
	var original struct {
		PointID     string       `json:"point_id"`
		OperationID string       `json:"operation_id"`
		Runtime     EnginePermit `json:"runtime"`
		At          time.Time    `json:"captured_at"`
	}
	if len(raw) > 1<<20 || json.Unmarshal(raw, &original) != nil {
		return nil, ErrConflict
	}
	canonical, _ := json.Marshal(original)
	if !bytes.Equal(raw, canonical) || original.PointID != point || original.OperationID != i.original.ID || original.Runtime.Runtime.BindingID != i.binding || original.At.IsZero() || original.At.Location() != time.UTC {
		return nil, &IntegrityError{}
	}
	return &NativeOnlineCapture{state: i.state, guard: i.guard, operationID: i.original.ID, pointID: point, permit: original.Runtime, capturedAt: original.At, inspection: i}, nil
}
