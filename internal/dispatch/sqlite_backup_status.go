package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/sqlitestatus"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type SQLiteBackupStatusArgs struct {
	App      string `json:"app"`
	Database string `json:"database"`
}

func (a SQLiteBackupStatusArgs) Valid() bool {
	return ValidApp(a.App) && (a.Database == "" || ValidApp(a.Database))
}

type SQLiteBackupStatusReader interface {
	SQLiteBackupStatus(context.Context, SQLiteBackupStatusArgs) (sqlitestatus.Status, error)
}

func decodeSQLiteBackupStatus(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "app", "database")
	if err != nil {
		return nil, err
	}
	app, e1 := strictjson.Value[string](f["app"])
	database, e2 := strictjson.Value[string](f["database"])
	a := SQLiteBackupStatusArgs{App: app, Database: database}
	if e1 != nil || e2 != nil || !a.Valid() {
		return nil, strictjson.ErrObject
	}
	return a, nil
}
