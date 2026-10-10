package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type BackupCredentialOperations interface {
	Plan(context.Context, string, *time.Time) (backupcredentials.Plan, error)
	Set(context.Context, string, string, backupcredentials.Packet) (backupcredentials.Receipt, error)
}
type BackupCredentialPlanArgs struct {
	App       string     `json:"app"`
	ExpiresAt *time.Time `json:"expires_at"`
}
type BackupCredentialSetArgs struct {
	App    string
	PlanID string
	Packet backupcredentials.Packet
}

func decodeBackupCredentialPlan(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "app", "expires_at")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	app, err := strictjson.Value[string](f["app"])
	if err != nil || !ValidApp(app) {
		return nil, strictjson.ErrObject
	}
	var expiry *time.Time
	if string(f["expires_at"]) != "null" {
		value, err := strictjson.Value[string](f["expires_at"])
		if err != nil || len(value) == 0 || value[len(value)-1] != 'Z' {
			return nil, strictjson.ErrObject
		}
		t, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil, strictjson.ErrObject
		}
		expiry = &t
	}
	return BackupCredentialPlanArgs{App: app, ExpiresAt: expiry}, nil
}
func decodeBackupCredentialSet(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "app", "plan_id", "packet")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	app, err := strictjson.Value[string](f["app"])
	if err != nil || !ValidApp(app) {
		return nil, strictjson.ErrObject
	}
	planID, err := strictjson.Value[string](f["plan_id"])
	if err != nil || !backupcredentials.ValidPlanID(planID) {
		return nil, strictjson.ErrObject
	}
	packet, err := backupcredentials.DecodePacket(f["packet"])
	if err != nil {
		return nil, strictjson.ErrObject
	}
	return BackupCredentialSetArgs{App: app, PlanID: planID, Packet: packet}, nil
}

// EncodeBackupCredentialSet is the private stdin wire boundary, not a log record.
func EncodeBackupCredentialSet(app, planID string, packet []byte) (json.RawMessage, error) {
	raw, err := json.Marshal(struct {
		App    string          `json:"app"`
		PlanID string          `json:"plan_id"`
		Packet json.RawMessage `json:"packet"`
	}{app, planID, packet})
	if err != nil {
		return nil, strictjson.ErrObject
	}
	if _, err := decodeBackupCredentialSet(raw); err != nil {
		clear(raw)
		return nil, strictjson.ErrObject
	}
	return raw, nil
}

func credentialFailure(err error) error {
	switch {
	case errors.Is(err, backupcredentials.ErrStale):
		return result.New(result.Conflict, nil)
	case errors.Is(err, backupcredentials.ErrStorage):
		return result.New(result.RecoveryRequired, nil)
	case errors.Is(err, backupcredentials.ErrInvalid):
		return result.New(result.InvalidUsage, nil)
	default:
		return result.New(result.PolicyRefused, nil)
	}
}
