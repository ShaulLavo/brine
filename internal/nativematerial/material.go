package nativematerial

import (
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"regexp"
)

const PacketLimit = 32 << 10

var operationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var appPattern = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type Receipt struct {
	OperationID       string                `json:"operation_id"`
	App               string                `json:"app"`
	PreparationPlanID string                `json:"preparation_plan_id"`
	BindingID         data.ReplicaBindingID `json:"binding_id"`
	EpochID           data.ReplicaEpochID   `json:"epoch_id"`
	Outcome           string                `json:"outcome"`
	Secrets           data.EngineSecrets    `json:"secrets"`
}

func (r Receipt) Valid() bool {
	if !operationPattern.MatchString(r.OperationID) || !data.ValidID(string(r.BindingID)) || !data.ValidID(string(r.EpochID)) || !appPattern.MatchString(r.App) || len(r.PreparationPlanID) != 71 || r.PreparationPlanID[:7] != "sha256:" || !data.ValidCatalogHash(r.PreparationPlanID[7:]) || r.Outcome != "configured_unpublished" && r.Outcome != "stored_unreferenced" && r.Outcome != "recovery_required" {
		return false
	}
	values := []data.EngineSecretVersion{r.Secrets.Publisher, r.Secrets.SigningPrivate, r.Secrets.SigningPublic, r.Secrets.AuthPolicy}
	seen := map[string]bool{}
	for _, v := range values {
		if v.Validate() != nil || seen[v.Reference] {
			return false
		}
		seen[v.Reference] = true
	}
	return !r.Secrets.Publisher.ExpiresAt.IsZero()
}
func DecodeReceipt(raw []byte) (Receipt, error) {
	if len(raw) > 8192 {
		return Receipt{}, data.ErrInvalid
	}
	fields, err := strictjson.Object(raw, "operation_id", "app", "preparation_plan_id", "binding_id", "epoch_id", "outcome", "secrets")
	if err != nil {
		return Receipt{}, data.ErrInvalid
	}
	secrets, err := strictjson.Object(fields["secrets"], "publisher", "signing_private", "signing_public", "auth_policy")
	if err != nil {
		return Receipt{}, data.ErrInvalid
	}
	for _, role := range secrets {
		if _, err := strictjson.Object(role, "reference", "version", "sha256", "expires_at"); err != nil {
			return Receipt{}, data.ErrInvalid
		}
	}

	var r Receipt
	if json.Unmarshal(raw, &r) != nil || !r.Valid() {
		return Receipt{}, data.ErrInvalid
	}
	return r, nil
}
