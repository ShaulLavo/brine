package dispatch

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type AppStatusArgs struct {
	App string `json:"app"`
}
type RollbackArgs struct {
	App       string `json:"app"`
	ReleaseID string `json:"release_id"`
}
type AppOperations interface {
	Status(context.Context, string) (apps.Report, error)
	Rollback(context.Context, string, string) (apps.RollbackPlan, error)
}

var appName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var releaseID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)

func ValidApp(s string) bool     { return appName.MatchString(s) }
func ValidRelease(s string) bool { return releaseID.MatchString(s) }
func decodeAppStatus(raw json.RawMessage) (any, error) {
	f, e := strictjson.Object(raw, "app")
	if e != nil {
		return nil, e
	}
	app, e := strictjson.Value[string](f["app"])
	if e != nil || app != "" && !ValidApp(app) {
		return nil, strictjson.ErrObject
	}
	return AppStatusArgs{App: app}, nil
}
func decodeRollback(raw json.RawMessage) (any, error) {
	f, e := strictjson.Object(raw, "app", "release_id")
	if e != nil {
		return nil, e
	}
	app, e := strictjson.Value[string](f["app"])
	if e != nil || !ValidApp(app) {
		return nil, strictjson.ErrObject
	}
	id, e := strictjson.Value[string](f["release_id"])
	if e != nil || id != "" && !ValidRelease(id) {
		return nil, strictjson.ErrObject
	}
	return RollbackArgs{App: app, ReleaseID: id}, nil
}
