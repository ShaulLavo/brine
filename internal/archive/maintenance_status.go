package archive

import (
	"context"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

// CredentialHealth contains metadata only, never environment bytes or values.
type CredentialHealth struct {
	BindingID     data.ReplicaBindingID `json:"binding_id"`
	EpochID       data.ReplicaEpochID   `json:"epoch_id"`
	ReceivedAt    time.Time             `json:"received_at"`
	AgeSeconds    int64                 `json:"age_seconds"`
	ExpiresAt     *time.Time            `json:"expires_at,omitempty"`
	Missing       bool                  `json:"missing"`
	Expired       bool                  `json:"expired"`
	RenewalNeeded bool                  `json:"renewal_needed"`
}

type MaintenanceStatus struct {
	ArchiveID   data.ArchiveID     `json:"archive_id"`
	State       string             `json:"state"`
	ObservedAt  time.Time          `json:"observed_at"`
	RetiredAt   time.Time          `json:"retired_at"`
	RetainUntil time.Time          `json:"retain_until"`
	Reason      string             `json:"reason,omitempty"`
	Proof       *RetentionProof    `json:"last_verification,omitempty"`
	Deletion    *Deletion          `json:"deletion,omitempty"`
	Credentials []CredentialHealth `json:"credentials,omitempty"`
}

// Status never contacts the provider or refreshes evidence. "verified" reports a
// still-valid previous independent restore, not a new reachability assertion.
func (s MaintenanceService) Status(ctx context.Context, id data.ArchiveID) (out MaintenanceStatus, err error) {
	release, err := s.guard(ctx)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, release()) }()
	p, d, manifest, err := s.retained(ctx, id)
	if err != nil {
		return out, err
	}
	out = MaintenanceStatus{ArchiveID: id, State: "unverified", ObservedAt: s.now(), RetiredAt: d.CommittedAt, RetainUntil: d.RetainUntil}
	proof, err := s.Journal.ReadRetentionProof(ctx, id)
	if err != nil {
		return out, err
	}
	if !proof.VerifiedAt.IsZero() {
		out.Proof = &proof
	}
	deletion, err := s.Journal.ReadArchiveDeletion(ctx, id)
	if err != nil {
		return out, err
	}
	if deletion.Sequence != 0 {
		out.Deletion = &deletion
		out.State = "deletion_pending"
		if deletion.Stage == DeletionComplete {
			out.State = "local_deleted"
		}
		return out, nil
	}
	if !out.ObservedAt.Before(d.RetainUntil) {
		out.State = "expired"
		return out, nil
	}
	admission, admissionErr := s.Host.ArchiveMaintenanceAdmission(ctx, p, d)
	out.Credentials, err = s.Host.ArchiveCredentialHealth(ctx, p)
	if err != nil || !credentialHealthMatches(p, out.Credentials, out.ObservedAt) {
		out.State = "degraded"
		out.Reason = "credential_observation_refused"
		return out, nil
	}
	for _, health := range out.Credentials {
		if health.Missing || health.Expired || health.RenewalNeeded {
			out.State = "degraded"
			out.Reason = "credential_renewal_required"
			return out, nil
		}
	}
	if admissionErr != nil || verifyArchiveAllocations(p) != nil {
		out.State = "degraded"
		out.Reason = "current_admission_refused"
		return out, nil
	}
	for _, database := range p.Candidate.Databases {
		if database.Written && !admission.RetentionAdmitted {
			out.State = "degraded"
			out.Reason = "destination_retention_unknown"
			return out, nil
		}
	}
	for _, tree := range p.Trees {
		if data.VerifyArchivedTree(tree, p.Candidate.Incarnation, id) != nil {
			out.State = "degraded"
			out.Reason = "archive_identity_refused"
			return out, nil
		}
	}
	if out.Proof != nil {
		if proof.PolicyHash == admission.PolicyHash && proof.TargetHash == admission.TargetHash && RetentionProofMatches(proof, p, d, manifest, out.ObservedAt) {
			out.State = "verified"
		} else {
			out.State = "degraded"
			out.Reason = "verification_stale"
		}
	}
	return out, nil
}

func credentialHealthMatches(p Plan, health []CredentialHealth, now time.Time) bool {
	type scope struct {
		binding data.ReplicaBindingID
		epoch   data.ReplicaEpochID
	}
	expected := map[scope]bool{}
	for _, database := range p.Candidate.Databases {
		if !database.Written {
			continue
		}
		for _, binding := range database.Epochs {
			expected[scope{binding.BindingID, binding.EpochID}] = true
		}
	}
	if len(expected) != len(health) {
		return false
	}
	for _, item := range health {
		key := scope{item.BindingID, item.EpochID}
		if !expected[key] {
			return false
		}
		delete(expected, key)
		if !item.Missing && (item.ReceivedAt.IsZero() || now.Before(item.ReceivedAt) || item.AgeSeconds < 0) {
			return false
		}
		if item.ExpiresAt != nil && (!now.Before(*item.ExpiresAt) != item.Expired || !now.Add(3*time.Minute).Before(*item.ExpiresAt) && !item.RenewalNeeded) {
			return false
		}
	}
	return len(expected) == 0
}
