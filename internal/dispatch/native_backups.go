package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type NativeCaptureArgs struct {
	App            string `json:"app"`
	Database       string `json:"database"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (a NativeCaptureArgs) Valid() bool {
	return ValidApp(a.App) && ValidApp(a.Database) && data.ValidID(a.IdempotencyKey)
}

type NativePrepareArgs NativeCaptureArgs

func (a NativePrepareArgs) Valid() bool { return NativeCaptureArgs(a).Valid() }

type NativeRestoreTestArgs struct {
	PointID        string `json:"point_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (a NativeRestoreTestArgs) Valid() bool {
	return data.ValidID(a.PointID) && data.ValidID(a.IdempotencyKey)
}

type NativeBackupOperations interface {
	Capture(context.Context, NativeCaptureArgs) (jobs.Accepted, error)
	Prepare(context.Context, NativePrepareArgs) (jobs.Accepted, error)
	TestRestore(context.Context, NativeRestoreTestArgs) (jobs.Accepted, error)
}

func decodeNativeCapture(raw json.RawMessage) (any, error) { return decodeNativeBackup(raw) }

func decodeNativePrepare(raw json.RawMessage) (any, error) {
	args, err := decodeNativeBackup(raw)
	if err != nil {
		return nil, err
	}
	return NativePrepareArgs(args), nil
}

func decodeNativeBackup(raw json.RawMessage) (NativeCaptureArgs, error) {
	fields, err := strictjson.Object(raw, "app", "database", "idempotency_key")
	if err != nil {
		return NativeCaptureArgs{}, strictjson.ErrObject
	}
	app, appErr := strictjson.Value[string](fields["app"])
	database, databaseErr := strictjson.Value[string](fields["database"])
	key, keyErr := strictjson.Value[string](fields["idempotency_key"])
	args := NativeCaptureArgs{App: app, Database: database, IdempotencyKey: key}
	if appErr != nil || databaseErr != nil || keyErr != nil || !args.Valid() {
		return NativeCaptureArgs{}, strictjson.ErrObject
	}
	return args, nil
}

func decodeNativeRestoreTest(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "point_id", "idempotency_key")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	point, pointErr := strictjson.Value[string](fields["point_id"])
	key, keyErr := strictjson.Value[string](fields["idempotency_key"])
	args := NativeRestoreTestArgs{PointID: point, IdempotencyKey: key}
	if pointErr != nil || keyErr != nil || !args.Valid() {
		return nil, strictjson.ErrObject
	}
	return args, nil
}

func (s *Server) submitNativeBackup(ctx context.Context, args any) (jobs.Accepted, error) {
	if s.NativeBackups == nil {
		return jobs.Accepted{}, result.New(result.DependencyMissing, nil)
	}
	switch args := args.(type) {
	case NativeCaptureArgs:
		return s.NativeBackups.Capture(ctx, args)
	case NativePrepareArgs:
		return s.NativeBackups.Prepare(ctx, args)
	case NativeRestoreTestArgs:
		return s.NativeBackups.TestRestore(ctx, args)
	default:
		return jobs.Accepted{}, result.New(result.DispatchInvalidRequest, nil)
	}
}
