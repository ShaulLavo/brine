package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
)

// An unfinished online upload excludes planned binding mutations, not ordinary
// writers or same-runtime restarts. It never asserts stopped-source quiescence.
func refusePendingNativeCaptureMutation(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID, operation string) error {
	rows, err := q.QueryContext(ctx, "SELECT o.id,d.canonical FROM data_native_upload_descriptors d JOIN operations o ON o.id=d.operation_id WHERE o.kind='native_capture' AND json_extract(d.canonical,'$.scope.binding_id')=? AND o.state<>'succeeded'", binding)
	if err != nil {
		return err
	}
	type pending struct {
		operation  string
		descriptor []byte
	}
	retained := make([]pending, 0)
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.operation, &item.descriptor); err != nil {
			_ = rows.Close()
			return err
		}
		retained = append(retained, item)
		if len(retained) > 128 {
			_ = rows.Close()
			return ErrConflict
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, item := range retained {
		var descriptor nativeUploadDescriptor
		if len(item.descriptor) > 8<<20 || json.Unmarshal(item.descriptor, &descriptor) != nil || descriptor.Scope.BindingID != string(binding) || descriptor.OperationID != item.operation {
			return &IntegrityError{}
		}
		original, err := nativeResolutionOperation(ctx, q, item.operation)
		if err != nil {
			return err
		}
		if original.State == ops.RecoveryRequired {
			if _, err = readNativeBackupResolvedReceipt(ctx, q, original); err == nil {
				continue
			}
		}
		// Only the original guarded producer may finish its own already-issued work.
		if operation == item.operation && original.State == ops.Preflight {
			continue
		}
		return ErrConflict
	}
	return nil
}
