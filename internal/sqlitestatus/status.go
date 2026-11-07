// Package sqlitestatus composes observations without host or provider authority.
package sqlitestatus

import (
	"regexp"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

var operationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

const RenewalWindow = 20 * time.Minute

// Facts contains only protected identities and independently recorded evidence.
type Facts struct {
	App, Database, DatabaseID, BindingID, EpochID string
	Committed                                     bool
	Credential                                    Credential
	Lifecycle                                     Lifecycle
	Fences                                        []Fence
	LastVerified                                  *Verified
}
type Credential struct {
	State            string     `json:"state"`
	Version          uint64     `json:"version"`
	ReceivedAt       *time.Time `json:"received_at"`
	ExpiresAt        *time.Time `json:"expires_at"`
	RemainingSeconds *int64     `json:"remaining_seconds"`
	RenewalNeeded    bool       `json:"renewal_needed"`
}
type Lifecycle struct {
	State      string     `json:"state"`
	VerifiedAt *time.Time `json:"verified_at"`
	FreshUntil *time.Time `json:"fresh_until"`
}
type Fence struct {
	ID             string `json:"id"`
	OwnerOperation string `json:"owner_operation"`
}
type Verified struct {
	OperationID    string               `json:"operation_id"`
	Source         restore.SQLiteSource `json:"source"`
	VerifiedAt     time.Time            `json:"verified_at"`
	InvariantCheck string               `json:"invariant_check"`
}
type Service struct {
	State      string    `json:"state"`
	ObservedAt time.Time `json:"observed_at"`
}
type Watermark struct {
	State      string    `json:"state"`
	TXID       *uint64   `json:"txid"`
	ObservedAt time.Time `json:"observed_at"`
}
type Observations struct {
	Service   Service
	Remote    restore.RemoteProbe
	Watermark Watermark
}
type Status struct {
	Engine       string              `json:"engine"`
	App          string              `json:"app"`
	Database     string              `json:"database"`
	DatabaseID   string              `json:"database_id"`
	BindingID    string              `json:"binding_id"`
	EpochID      string              `json:"epoch_id"`
	ObservedAt   time.Time           `json:"observed_at"`
	State        string              `json:"state"`
	Committed    bool                `json:"committed"`
	Service      Service             `json:"service"`
	Remote       restore.RemoteProbe `json:"remote"`
	Watermark    Watermark           `json:"remote_watermark"`
	Credential   Credential          `json:"credential"`
	Lifecycle    Lifecycle           `json:"lifecycle"`
	HeldFences   []Fence             `json:"held_fences"`
	LastVerified *Verified           `json:"last_verified"`
	// No qualified local metadata reader exists yet. Null is deliberately unknown.
	LocalPosition    *uint64  `json:"local_position"`
	PositionDistance *uint64  `json:"position_distance"`
	Loss             string   `json:"loss"`
	Reasons          []string `json:"reasons"`
}

func Compose(now time.Time, f Facts, o Observations) Status {
	s := Status{Engine: "sqlite", App: f.App, Database: f.Database, DatabaseID: f.DatabaseID, BindingID: f.BindingID, EpochID: f.EpochID, ObservedAt: now, State: "observed", Committed: f.Committed, Service: o.Service, Remote: o.Remote, Watermark: o.Watermark, Credential: f.Credential, Lifecycle: f.Lifecycle, HeldFences: append([]Fence{}, f.Fences...), LastVerified: f.LastVerified, Loss: "unknown", Reasons: []string{"local_position_unknown", "loss_unknown"}}
	if s.Credential.ExpiresAt != nil {
		remaining := int64(s.Credential.ExpiresAt.Sub(now) / time.Second)
		s.Credential.RemainingSeconds = &remaining
		s.Credential.RenewalNeeded = remaining <= int64(RenewalWindow/time.Second)
		if s.Credential.State == "available" && !now.Before(*s.Credential.ExpiresAt) {
			s.Credential.State = "expired"
		}
	}
	s.Lifecycle = s.Lifecycle.at(now)
	if s.Lifecycle.State == "" {
		s.Lifecycle.State = "unknown"
	}
	if s.LastVerified == nil {
		s.State = "unverified"
		s.Reasons = append(s.Reasons, "no_verified_point")
	}
	s.degrade(!f.Committed, "replica_uncommitted")
	s.degrade(o.Service.State != "active", "service_unavailable")
	s.degrade(o.Remote.State != restore.RemoteReachable, "remote_"+string(o.Remote.State))
	s.degrade(o.Watermark.State != "known", "watermark_"+o.Watermark.State)
	s.degrade(s.Credential.State != "available", "credentials_"+s.Credential.State)
	s.degrade(s.Credential.RenewalNeeded, "credential_renewal_needed")
	s.degrade(s.Lifecycle.State != "fresh", "lifecycle_"+s.Lifecycle.State)
	s.degrade(len(s.HeldFences) > 0, "fenced")
	return s
}
func (s *Status) degrade(condition bool, reason string) {
	if !condition {
		return
	}
	s.State = "degraded"
	s.Reasons = append(s.Reasons, reason)
}
func (s Status) Valid() bool {
	if s.Engine != "sqlite" || !namePattern.MatchString(s.App) || !namePattern.MatchString(s.Database) || !data.ValidID(s.DatabaseID) || !data.ValidID(s.BindingID) || !data.ValidID(s.EpochID) || s.ObservedAt.IsZero() || !slices.Contains([]string{"observed", "unverified", "degraded"}, s.State) || s.LocalPosition != nil || s.PositionDistance != nil || s.Loss != "unknown" || s.HeldFences == nil || s.Reasons == nil {
		return false
	}
	if !validObservations(s) || !validCredential(s.Credential) || !validLifecycle(s.Lifecycle) {
		return false
	}
	for _, f := range s.HeldFences {
		if !data.ValidID(f.ID) || !operationPattern.MatchString(f.OwnerOperation) {
			return false
		}
	}
	for _, reason := range s.Reasons {
		if !validReason(reason) {
			return false
		}
	}
	return validVerified(s.LastVerified, s.BindingID, s.EpochID)
}
func validObservations(s Status) bool {
	return slices.Contains([]string{"active", "inactive", "failed", "unknown"}, s.Service.State) && !s.Service.ObservedAt.IsZero() && slices.Contains([]restore.RemoteState{restore.RemoteReachable, restore.RemoteEmpty, restore.RemoteAuthRefused, restore.RemoteCredentialsRejected, restore.RemoteDestinationMissing, restore.RemoteStorageUnavailable, restore.RemoteNetworkUnavailable, restore.RemoteResponseInvalid, restore.RemoteNotChecked}, s.Remote.State) && !s.Remote.ObservedAt.IsZero() && !s.Watermark.ObservedAt.IsZero() && slices.Contains([]string{"known", "failed", "timed_out", "not_checked"}, s.Watermark.State) && (s.Watermark.State == "known") == (s.Watermark.TXID != nil) && (s.Watermark.TXID == nil || *s.Watermark.TXID > 0)
}
func validCredential(c Credential) bool {
	return slices.Contains([]string{"available", "missing", "expired", "unreadable"}, c.State) && (c.State != "available" && c.State != "expired" || c.Version > 0) && (c.RemainingSeconds == nil || c.ExpiresAt != nil)
}
func validLifecycle(l Lifecycle) bool {
	return slices.Contains([]string{"fresh", "stale", "unsafe", "unknown"}, l.State) && (l.State != "fresh" && l.State != "stale" || l.VerifiedAt != nil && l.FreshUntil != nil)
}
func validVerified(v *Verified, binding, epoch string) bool {
	if v == nil {
		return true
	}
	if !operationPattern.MatchString(v.OperationID) || v.VerifiedAt.IsZero() || !slices.Contains([]string{"passed", "unknown"}, v.InvariantCheck) {
		return false
	}
	sourceBinding, sourceEpoch, err := v.Source.Reference()
	if err != nil || sourceBinding != binding || sourceEpoch != epoch {
		return false
	}
	return v.Source.Snapshot == nil || data.ValidID(v.Source.Snapshot.PointID)
}
func validReason(r string) bool {
	return slices.Contains([]string{"local_position_unknown", "loss_unknown", "no_verified_point", "replica_uncommitted", "service_unavailable", "remote_reachable", "remote_empty", "remote_auth_refused", "remote_credentials_rejected", "remote_destination_missing", "remote_storage_unavailable", "remote_network_unavailable", "remote_response_invalid", "remote_not_checked", "watermark_failed", "watermark_timed_out", "watermark_not_checked", "credentials_missing", "credentials_expired", "credentials_unreadable", "credential_renewal_needed", "lifecycle_stale", "lifecycle_unsafe", "lifecycle_unknown", "fenced"}, r)
}

// Probe latency must not extend a protected lifecycle proof's freshness.
func (l Lifecycle) at(now time.Time) Lifecycle {
	if l.State != "fresh" && l.State != "stale" || l.VerifiedAt == nil || l.FreshUntil == nil {
		return l
	}
	l.State = "fresh"
	if now.Before(*l.VerifiedAt) {
		l.State = "unknown"
		return l
	}
	if now.After(*l.FreshUntil) {
		l.State = "stale"
	}
	return l
}
