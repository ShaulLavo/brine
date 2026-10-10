package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type BackupCredentialOperations interface {
	Plan(context.Context, string, string, *time.Time) (backupcredentials.Plan, error)
	Set(context.Context, string, string, string, backupcredentials.Packet) (jobs.Accepted, error)
}
type BackupCredentialPlanArgs struct {
	App       string     `json:"app"`
	Database  string     `json:"database"`
	ExpiresAt *time.Time `json:"expires_at"`
}
type BackupCredentialSetArgs struct {
	App      string
	Database string
	PlanID   string
	Packet   backupcredentials.Packet
}

func decodeBackupCredentialPlan(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "app", "database", "expires_at")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	app, err := strictjson.Value[string](f["app"])
	if err != nil || !ValidApp(app) {
		return nil, strictjson.ErrObject
	}
	database, err := strictjson.Value[string](f["database"])
	if err != nil || database != "" && !ValidApp(database) {
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
	return BackupCredentialPlanArgs{App: app, Database: database, ExpiresAt: expiry}, nil
}
func decodeBackupCredentialSet(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "app", "database", "plan_id", "packet")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	app, err := strictjson.Value[string](f["app"])
	if err != nil || !ValidApp(app) {
		return nil, strictjson.ErrObject
	}
	database, err := strictjson.Value[string](f["database"])
	if err != nil || database != "" && !ValidApp(database) {
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
	return BackupCredentialSetArgs{App: app, Database: database, PlanID: planID, Packet: packet}, nil
}

// EncodeBackupCredentialSet is the private stdin wire boundary, not a log record.
func EncodeBackupCredentialSet(app, database, planID string, packet []byte) (json.RawMessage, error) {
	raw, err := json.Marshal(struct {
		App      string          `json:"app"`
		Database string          `json:"database"`
		PlanID   string          `json:"plan_id"`
		Packet   json.RawMessage `json:"packet"`
	}{app, database, planID, packet})
	if err != nil {
		return nil, strictjson.ErrObject
	}
	decoded, err := decodeBackupCredentialSet(raw)
	if err != nil {
		clear(raw)
		return nil, strictjson.ErrObject
	}
	validated := decoded.(BackupCredentialSetArgs)
	validated.Packet.Clear()
	return raw, nil
}

func credentialFailure(err error) error {
	if err == nil {
		return nil
	}
	var typed *result.Error
	if errors.As(err, &typed) {
		return result.Classify(err)
	}
	switch {
	case errors.Is(err, backupcredentials.ErrAdmissionRefresh):
		return result.New(result.BackupAdmissionRefreshRequired, nil)
	case errors.Is(err, backupcredentials.ErrStale):
		return result.New(result.Conflict, nil)
	case errors.Is(err, backupcredentials.ErrActivationUnknown), errors.Is(err, backupcredentials.ErrRemoteAccess), errors.Is(err, backupcredentials.ErrStorage):
		return result.New(result.RecoveryRequired, nil)
	case errors.Is(err, backupcredentials.ErrInvalid):
		return result.New(result.InvalidUsage, nil)
	case errors.Is(err, backupcredentials.ErrExpired):
		return result.New(result.PolicyRefused, err)
	default:
		return result.Classify(err)
	}
}
