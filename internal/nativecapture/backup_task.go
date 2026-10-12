package nativecapture

import (
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

type RecoveryReceipt struct {
	Kind        string `json:"kind"`
	OperationID string `json:"operation_id"`
	BindingID   string `json:"binding_id"`
	EpochID     string `json:"epoch_id"`
	Reference   string `json:"reference"`
	Version     uint64 `json:"version"`
	SHA256      string `json:"sha256"`
	Outcome     string `json:"outcome"`
}

func DecodeRecoveryReceipt(raw []byte) (RecoveryReceipt, error) {
	var receipt RecoveryReceipt
	if len(raw) == 0 || len(raw) > 4096 {
		return receipt, ErrLimit
	}
	if _, err := strictjson.Object(raw, "kind", "operation_id", "binding_id", "epoch_id", "reference", "version", "sha256", "outcome"); err != nil {
		return receipt, ErrInvalid
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Kind != "native_recovery_set" || !taskOperationID.MatchString(receipt.OperationID) || !idPattern.MatchString(receipt.BindingID) || !idPattern.MatchString(receipt.EpochID) || !validReference(receipt.Reference) || receipt.Version == 0 || !hashPattern.MatchString(receipt.SHA256) || receipt.Outcome != "stored" {
		return receipt, ErrInvalid
	}
	return receipt, nil
}

type BackupPolicyReceipt struct {
	Kind         string `json:"kind"`
	OperationID  string `json:"operation_id"`
	BindingID    string `json:"binding_id"`
	EpochID      string `json:"epoch_id"`
	PlanID       string `json:"plan_id"`
	Revision     uint64 `json:"revision"`
	PolicySHA256 string `json:"policy_sha256"`
	RepositoryID string `json:"repository_id"`
	Outcome      string `json:"outcome"`
}

func DecodeBackupPolicyReceipt(raw []byte) (BackupPolicyReceipt, error) {
	var receipt BackupPolicyReceipt
	if len(raw) == 0 || len(raw) > 4096 {
		return receipt, ErrLimit
	}
	if _, err := strictjson.Object(raw, "kind", "operation_id", "binding_id", "epoch_id", "plan_id", "revision", "policy_sha256", "repository_id", "outcome"); err != nil {
		return receipt, ErrInvalid
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Kind != "native_backup_activate" || !taskOperationID.MatchString(receipt.OperationID) || !idPattern.MatchString(receipt.BindingID) || !idPattern.MatchString(receipt.EpochID) || !idPattern.MatchString(receipt.PlanID) || receipt.Revision == 0 || !hashPattern.MatchString(receipt.PolicySHA256) || !hashPattern.MatchString(receipt.RepositoryID) || receipt.Outcome != "active" {
		return receipt, ErrInvalid
	}
	return receipt, nil
}
