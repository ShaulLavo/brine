package dispatch

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"

	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type RestoreTestArgs struct {
	App      string `json:"app"`
	Database string `json:"database"`
	TXID     string `json:"txid"`
	Point    string `json:"point"`
}
type RestoreTestOperations interface {
	Test(context.Context, RestoreTestArgs) (restore.Receipt, error)
}

var restorePointID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func (a RestoreTestArgs) Valid() bool {
	if !ValidApp(a.App) || a.Database != "" && !ValidApp(a.Database) || a.TXID != "" && a.Point != "" {
		return false
	}
	if a.Point != "" && !restorePointID.MatchString(a.Point) {
		return false
	}
	if a.TXID != "" {
		if len(a.TXID) > 16 {
			return false
		}
		for _, c := range a.TXID {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
		n, err := strconv.ParseUint(a.TXID, 16, 64)
		if err != nil || n == 0 {
			return false
		}
	}
	return true
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
