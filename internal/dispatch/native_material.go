package dispatch

import (
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"strings"
	"time"
)

type NativeMaterialArgs struct {
	App               string
	PreparationPlanID string
	IdempotencyKey    string
	packet            []byte
}

func (a NativeMaterialArgs) Valid() bool {
	return ValidApp(a.App) && len(a.PreparationPlanID) == 71 && a.PreparationPlanID[:7] == "sha256:" && data.ValidCatalogHash(a.PreparationPlanID[7:]) && data.ValidID(a.IdempotencyKey) && len(a.packet) > 0 && len(a.packet) <= nativematerial.PacketLimit
}
func (a NativeMaterialArgs) Packet() []byte             { return append([]byte(nil), a.packet...) }
func (a NativeMaterialArgs) Clear()                     { clear(a.packet) }
func (NativeMaterialArgs) String() string               { return "[private native material delivery]" }
func (NativeMaterialArgs) GoString() string             { return "[private native material delivery]" }
func (NativeMaterialArgs) MarshalJSON() ([]byte, error) { return []byte(`{"private":true}`), nil }

type NativeMaterialOperations interface {
	DeliverMaterial(context.Context, NativeMaterialArgs) (nativematerial.Receipt, error)
	InspectMaterial(context.Context, NativeMaterialInspectArgs) (nativematerial.Receipt, error)
}
type NativeMaterialInspectArgs struct {
	OperationID string `json:"operation_id"`
}

func decodeNativeMaterialInspect(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "operation_id")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	id, err := strictjson.Value[string](fields["operation_id"])
	if err != nil || !jobs.ValidID(id) {
		return nil, strictjson.ErrObject
	}
	return NativeMaterialInspectArgs{OperationID: id}, nil
}

func decodeNativeMaterial(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "app", "preparation_plan_id", "idempotency_key", "packet")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	app, aErr := strictjson.Value[string](fields["app"])
	plan, pErr := strictjson.Value[string](fields["preparation_plan_id"])
	key, kErr := strictjson.Value[string](fields["idempotency_key"])
	args := NativeMaterialArgs{App: app, PreparationPlanID: plan, IdempotencyKey: key, packet: append([]byte(nil), fields["packet"]...)}
	if aErr != nil || pErr != nil || kErr != nil || !args.Valid() || !nativeMaterialPacketShape(args.packet) {
		args.Clear()
		return nil, strictjson.ErrObject
	}
	return args, nil
}
func EncodeNativeMaterial(app, plan, key string, packet []byte) (json.RawMessage, error) {
	args := NativeMaterialArgs{App: app, PreparationPlanID: plan, IdempotencyKey: key, packet: packet}
	if !args.Valid() || !nativeMaterialPacketShape(packet) {
		return nil, result.New(result.InvalidUsage, nil)
	}
	return json.Marshal(struct {
		App    string          `json:"app"`
		Plan   string          `json:"preparation_plan_id"`
		Key    string          `json:"idempotency_key"`
		Packet json.RawMessage `json:"packet"`
	}{app, plan, key, packet})
}

func nativeMaterialPacketShape(raw []byte) bool {
	if len(raw) == 0 || len(raw) > nativematerial.PacketLimit {
		return false
	}
	fields, err := strictjson.Object(raw, "publisher", "signing_private", "signing_public", "auth_policy", "publisher_expires_at")
	if err != nil {
		return false
	}
	for _, role := range []string{"publisher", "signing_private", "signing_public"} {
		value, err := strictjson.Value[string](fields[role])
		if err != nil || value == "" || strings.ContainsRune(value, 0) {
			return false
		}
	}
	expiry, err := strictjson.Value[time.Time](fields["publisher_expires_at"])
	if err != nil || expiry.IsZero() || expiry.Location() != time.UTC {
		return false
	}
	auth, err := strictjson.Object(fields["auth_policy"], "mode", "require_client_token")
	if err != nil {
		return false
	}
	mode, err := strictjson.Value[string](auth["mode"])
	if err != nil || mode != "supplied_key_jwt" {
		return false
	}
	require, err := strictjson.Value[bool](auth["require_client_token"])
	return err == nil && require
}
