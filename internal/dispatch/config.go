package dispatch

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/secrets"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type ConfigArgs struct {
	App   string      `json:"app"`
	Edits []apps.Edit `json:"edits"`
}
type LifecycleArgs struct {
	App    string          `json:"app"`
	Action plan.ChangeKind `json:"action"`
}
type SecretArgs struct {
	App       string `json:"app"`
	Reference string `json:"reference"`
	Value     []byte `json:"value"`
}
type ConfigurationOperations interface {
	ConfigSet(context.Context, string, []apps.Edit) (apps.ConfigPlan, error)
	Lifecycle(context.Context, string, plan.ChangeKind) (apps.ConfigPlan, error)
}
type SecretOperations interface {
	Set(context.Context, string, string, string, []byte) (secrets.Stored, error)
}

func decodeConfig(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "app", "edits")
	if err != nil {
		return nil, err
	}
	app, err := strictjson.Value[string](f["app"])
	if err != nil || !ValidApp(app) {
		return nil, strictjson.ErrObject
	}
	edits, err := strictjson.Value[[]json.RawMessage](f["edits"])
	if err != nil || len(edits) == 0 || len(edits) > 128 {
		return nil, strictjson.ErrObject
	}
	out := []apps.Edit{}
	for _, raw := range edits {
		fields, e := strictjson.Object(raw, "key", "value", "action")
		if e != nil {
			return nil, e
		}
		key, e := strictjson.Value[string](fields["key"])
		if e != nil || len(key) == 0 || len(key) > 256 {
			return nil, strictjson.ErrObject
		}
		value, e := strictjson.Value[string](fields["value"])
		if e != nil || len(value) > 32<<10 {
			return nil, strictjson.ErrObject
		}
		action, e := strictjson.Value[string](fields["action"])
		if e != nil {
			return nil, e
		}
		out = append(out, apps.Edit{Key: key, Value: value, Action: action})
	}
	return ConfigArgs{App: app, Edits: out}, nil
}
func decodeLifecycle(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "app", "action")
	if err != nil {
		return nil, err
	}
	app, err := strictjson.Value[string](f["app"])
	if err != nil || !ValidApp(app) {
		return nil, strictjson.ErrObject
	}
	action, err := strictjson.Value[plan.ChangeKind](f["action"])
	if err != nil || action != plan.RestartApp && action != plan.StopApp && action != plan.StartApp && action != plan.RemoveApp {
		return nil, strictjson.ErrObject
	}
	return LifecycleArgs{App: app, Action: action}, nil
}
func decodeSecret(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "app", "reference", "value")
	if err != nil {
		return nil, err
	}
	app, err := strictjson.Value[string](f["app"])
	if err != nil {
		return nil, err
	}
	ref, err := strictjson.Value[string](f["reference"])
	if err != nil || !ops.ValidIntent(ops.Intent{Kind: ops.SecretSet, App: app, SecretRef: ref}) {
		return nil, strictjson.ErrObject
	}
	encoded, err := strictjson.Value[string](f["value"])
	if err != nil {
		return nil, err
	}
	value, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(value) == 0 || len(value) > secrets.ValueLimit || base64.StdEncoding.EncodeToString(value) != encoded {
		return nil, strictjson.ErrObject
	}
	return SecretArgs{App: app, Reference: ref, Value: value}, nil
}
func decodeStored(raw json.RawMessage) (secrets.Stored, error) {
	f, err := strictjson.Object(raw, "operation_id", "version_name", "bound")
	if err != nil {
		return secrets.Stored{}, err
	}
	id, err := strictjson.Value[string](f["operation_id"])
	if err != nil || !releaseID.MatchString(id) {
		return secrets.Stored{}, strictjson.ErrObject
	}
	name, err := strictjson.Value[string](f["version_name"])
	if err != nil || !ops.ValidSecretVersionName(name) {
		return secrets.Stored{}, strictjson.ErrObject
	}
	bound, err := strictjson.Value[bool](f["bound"])
	if err != nil || bound {
		return secrets.Stored{}, strictjson.ErrObject
	}
	return secrets.Stored{OperationID: id, VersionName: name}, nil
}
