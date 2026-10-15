package nativematerial

import (
	"encoding/json"
	"math"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type ActivationPlan struct {
	ID                  string             `json:"id"`
	App                 string             `json:"app"`
	Requester           string             `json:"requester"`
	DeliveryOperationID string             `json:"delivery_operation_id"`
	Head                data.EngineHeadRef `json:"head"`
	DesiredRevision     uint64             `json:"desired_revision"`
	PolicyHash          string             `json:"policy_hash"`
	Secrets             data.EngineSecrets `json:"secrets"`
	RestartRequired     bool               `json:"restart_required"`
	CreatedAt           time.Time          `json:"created_at"`
	ExpiresAt           time.Time          `json:"expires_at"`
}

func (p ActivationPlan) Valid() bool {
	return data.ValidID(p.ID) && appPattern.MatchString(p.App) && operationPattern.MatchString(p.DeliveryOperationID) &&
		p.Head.Validate() == nil && p.Head.Version < math.MaxInt64 && p.DesiredRevision > 0 && p.DesiredRevision < math.MaxInt64 && p.Requester != "" &&
		len(p.PolicyHash) == 71 && p.PolicyHash[:7] == "sha256:" && data.ValidCatalogHash(p.PolicyHash[7:]) && p.Secrets.Validate() == nil &&
		!p.CreatedAt.IsZero() && p.CreatedAt.Location() == time.UTC && p.ExpiresAt.Location() == time.UTC && p.ExpiresAt.Equal(p.CreatedAt.Add(15*time.Minute))
}

type ActivationReceipt struct {
	PlanID         string                `json:"plan_id"`
	BindingID      data.ReplicaBindingID `json:"binding_id"`
	EpochID        data.ReplicaEpochID   `json:"epoch_id"`
	RuntimeVersion uint64                `json:"runtime_version"`
	RuntimeSHA256  string                `json:"runtime_sha256"`
}

func (r ActivationReceipt) Valid() bool {
	return data.ValidID(r.PlanID) && (data.EngineHeadRef{BindingID: r.BindingID, EpochID: r.EpochID, Version: r.RuntimeVersion, RuntimeSHA256: r.RuntimeSHA256}).Validate() == nil
}
func DecodeActivationPlan(raw []byte) (ActivationPlan, error) {
	var p ActivationPlan
	fields, err := strictjson.Object(raw, "id", "app", "requester", "delivery_operation_id", "head", "desired_revision", "policy_hash", "secrets", "restart_required", "created_at", "expires_at")
	if err != nil || len(raw) > 16384 || json.Unmarshal(raw, &p) != nil || !p.Valid() {
		return p, data.ErrInvalid
	}
	if _, err = strictjson.Object(fields["head"], "binding_id", "epoch_id", "version", "runtime_sha256"); err != nil {
		return p, data.ErrInvalid
	}
	roles, err := strictjson.Object(fields["secrets"], "publisher", "signing_private", "signing_public", "auth_policy")
	if err != nil {
		return p, data.ErrInvalid
	}
	for _, role := range roles {
		if _, err := strictjson.Object(role, "reference", "version", "sha256", "expires_at"); err != nil {
			return p, data.ErrInvalid
		}
	}
	return p, nil
}
func DecodeActivationReceipt(raw []byte) (ActivationReceipt, error) {
	var r ActivationReceipt
	if _, err := strictjson.Object(raw, "plan_id", "binding_id", "epoch_id", "runtime_version", "runtime_sha256"); err != nil || json.Unmarshal(raw, &r) != nil || !r.Valid() {
		return r, data.ErrInvalid
	}
	return r, nil
}
