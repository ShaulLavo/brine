package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type NativeBackupStatusArgs struct {
	App      string `json:"app"`
	Database string `json:"database"`
}
type NativeBackupStatusOperations interface {
	BackupStatus(context.Context, NativeBackupStatusArgs) (nativecapture.BackupStatus, error)
}

func decodeNativeBackupStatus(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "app", "database")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	app, appErr := strictjson.Value[string](fields["app"])
	database, databaseErr := strictjson.Value[string](fields["database"])
	if appErr != nil || databaseErr != nil || !ValidApp(app) || !ValidApp(database) {
		return nil, strictjson.ErrObject
	}
	return NativeBackupStatusArgs{App: app, Database: database}, nil
}
