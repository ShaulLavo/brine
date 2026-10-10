package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type RestoreTestArgs = ops.RestoreTaskInput
type RestoreTestOperations interface {
	Test(context.Context, RestoreTestArgs) (jobs.Accepted, error)
}

func decodeRestoreTest(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "app", "database", "txid", "point")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	app, e1 := strictjson.Value[string](f["app"])
	database, e2 := strictjson.Value[string](f["database"])
	txid, e3 := strictjson.Value[string](f["txid"])
	point, e4 := strictjson.Value[string](f["point"])
	a := RestoreTestArgs{App: app, Database: database, TXID: txid, Point: point}
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || !a.Valid() {
		return nil, strictjson.ErrObject
	}
	return a, nil
}
