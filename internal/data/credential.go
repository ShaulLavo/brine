package data

import "time"

// CredentialEvidence is reference-only delivery evidence. Values never enter
// inventory or a plan. Scope and immutable version are frozen before approval.
type CredentialEvidence struct {
	BindingID   ReplicaBindingID     `json:"binding_id"`
	EpochID     ReplicaEpochID       `json:"epoch_id"`
	Destination BackupDestinationRef `json:"destination"`
	Reference   string               `json:"reference"`
	Version     uint64               `json:"version"`
	PolicyHash  string               `json:"policy_hash"`
	ReceivedAt  time.Time            `json:"received_at"`
	ExpiresAt   *time.Time           `json:"expires_at,omitempty"`
}

func (e CredentialEvidence) Admits(binding ReplicaBindingID, policyHash string, now time.Time, budget time.Duration) bool {
	return ValidID(string(e.BindingID)) && e.BindingID == binding && ValidID(string(e.EpochID)) && e.Reference != "" && e.Destination != "" && e.Version > 0 && e.PolicyHash == policyHash && !e.ReceivedAt.IsZero() && !now.Before(e.ReceivedAt) && budget >= 0 && (e.ExpiresAt == nil || now.Add(budget).Before(*e.ExpiresAt))
}

// Equal compares immutable receipt metadata, independent of time representations.
func (e CredentialEvidence) Equal(other CredentialEvidence) bool {
	expiryEqual := e.ExpiresAt == nil && other.ExpiresAt == nil || e.ExpiresAt != nil && other.ExpiresAt != nil && e.ExpiresAt.Equal(*other.ExpiresAt)
	return e.BindingID == other.BindingID && e.EpochID == other.EpochID && e.Destination == other.Destination && e.Reference == other.Reference && e.Version == other.Version && e.PolicyHash == other.PolicyHash && e.ReceivedAt.Equal(other.ReceivedAt) && expiryEqual
}
