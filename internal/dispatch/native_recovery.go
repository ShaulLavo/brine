package dispatch

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type NativeRecoveryArgs struct {
	DeploymentPlanID string
	IdempotencyKey   string
	password         []byte
}

func (a NativeRecoveryArgs) Password() []byte           { return append([]byte(nil), a.password...) }
func (a NativeRecoveryArgs) Clear()                     { clear(a.password) }
func (NativeRecoveryArgs) String() string               { return "[private native recovery delivery]" }
func (NativeRecoveryArgs) GoString() string             { return "[private native recovery delivery]" }
func (NativeRecoveryArgs) MarshalJSON() ([]byte, error) { return []byte(`{"private":true}`), nil }
func (a NativeRecoveryArgs) Valid() bool {
	return strings.HasPrefix(a.DeploymentPlanID, "sha256:") && data.ValidCatalogHash(strings.TrimPrefix(a.DeploymentPlanID, "sha256:")) && data.ValidID(a.IdempotencyKey) && validNativeRecoveryPassword(a.password)
}
func validNativeRecoveryPassword(password []byte) bool {
	return len(password) >= 16 && len(password) <= 4096 && !strings.ContainsRune(string(password), 0)
}

type NativeRecoveryOperations interface {
	SetRecovery(context.Context, NativeRecoveryArgs) (jobs.Accepted, error)
}

func decodeNativeRecovery(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "deployment_plan_id", "idempotency_key", "password")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	plan, planErr := strictjson.Value[string](fields["deployment_plan_id"])
	key, keyErr := strictjson.Value[string](fields["idempotency_key"])
	password, passwordErr := strictjson.Value[string](fields["password"])
	value := NativeRecoveryArgs{DeploymentPlanID: plan, IdempotencyKey: key, password: []byte(password)}
	if planErr != nil || keyErr != nil || passwordErr != nil || !value.Valid() {
		value.Clear()
		return nil, strictjson.ErrObject
	}
	return value, nil
}

func EncodeNativeRecoverySet(plan, key string, packet []byte) (json.RawMessage, error) {
	fields, err := strictjson.Object(packet, "password")
	if err != nil {
		return nil, result.New(result.InvalidUsage, nil)
	}
	password, err := strictjson.Value[string](fields["password"])
	args := NativeRecoveryArgs{DeploymentPlanID: plan, IdempotencyKey: key, password: []byte(password)}
	defer args.Clear()
	if err != nil || !args.Valid() {
		return nil, result.New(result.InvalidUsage, nil)
	}
	return json.Marshal(struct {
		Plan     string `json:"deployment_plan_id"`
		Key      string `json:"idempotency_key"`
		Material string `json:"password"` // #nosec G117 -- Intentional private transport packet; logging and metadata use the redacted type.
	}{plan, key, password})
}
