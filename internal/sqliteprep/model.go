// Package sqliteprep owns bounded SQLite preparation records and orchestration.
// Records and fake ports establish no filesystem, credential or store authority.
package sqliteprep

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
)

const MaxRecordBytes = 128 << 10
const MaxCaptureBytes = data.MaxSQLiteCaptureBytes

var (
	ErrInvalid  = errors.New("invalid SQLite preparation record")
	ErrLimit    = errors.New("SQLite preparation record limit exceeded")
	ErrRecovery = errors.New("SQLite preparation requires inspection, supersede or abandon")
	token       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	appName     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	// requester is the server-established restricted-channel identity, the
	// same closed form datainit.Requester and the operation journal record.
	requester = regexp.MustCompile(`^deploy:[a-f0-9]{64}$`)
)

type TaskKind string

const (
	PrepareTask TaskKind = "sqlite_prepare"
	AbandonTask TaskKind = "sqlite_prepare_abandon"
)

// Task is the exact real preflight task. InputSHA256 is the canonical Input hash,
// not a caller-selected point, fence owner or credential capability.
type Task struct {
	OperationID string   `json:"operation_id"`
	Kind        TaskKind `json:"kind"`
	Requester   string   `json:"requester"`
	App         string   `json:"app"`
	InputSHA256 string   `json:"input_sha256"`
}

func (t Task) Validate() error {
	if !token.MatchString(t.OperationID) || !requester.MatchString(t.Requester) || !appName.MatchString(t.App) || !data.ValidCatalogHash(t.InputSHA256) || t.Kind != PrepareTask && t.Kind != AbandonTask {
		return ErrInvalid
	}
	return nil
}

type Input struct {
	Kind                       TaskKind        `json:"kind"`
	PreparationID              string          `json:"preparation_id"`
	App                        string          `json:"app"`
	DatabaseID                 data.DatabaseID `json:"database_id"`
	Supersedes                 string          `json:"supersedes,omitempty"`
	DeclarationPlanID          string          `json:"declaration_plan_id,omitempty"`
	DeliveredCredentialVersion uint64          `json:"delivered_credential_version,omitempty"`
}

func (i Input) Validate() error {
	if !ValidSelectors(i.DeclarationPlanID, i.DeliveredCredentialVersion) || i.Kind == AbandonTask && i.DeclarationPlanID != "" {
		return ErrInvalid
	}
	if !data.ValidID(i.PreparationID) || !data.ValidID(string(i.DatabaseID)) || !appName.MatchString(i.App) || i.Kind != PrepareTask && i.Kind != AbandonTask {
		return ErrInvalid
	}
	if i.Supersedes != "" && (i.Kind != PrepareTask || !data.ValidID(i.Supersedes) || i.Supersedes == i.PreparationID) {
		return ErrInvalid
	}
	return nil
}
func ValidSelectors(plan string, version uint64) bool {
	return plan == "" && version == 0 || datainit.ValidID(plan) && version > 0 && version <= 1<<63-1
}

func (i Input) Matches(t Task) bool {
	hash, err := Fingerprint(i)
	return err == nil && t.Validate() == nil && t.Kind == i.Kind && t.App == i.App && t.InputSHA256 == hash
}

type Retention struct {
	EvidenceSHA256 string    `json:"evidence_sha256"`
	ObservedAt     time.Time `json:"observed_at"`
	FreshUntil     time.Time `json:"fresh_until"`
	RetainUntil    time.Time `json:"retain_until"`
}

func (r Retention) Validate() error {
	if !data.ValidCatalogHash(r.EvidenceSHA256) || !utc(r.ObservedAt) || !utc(r.FreshUntil) || !utc(r.RetainUntil) || !r.ObservedAt.Before(r.FreshUntil) || !r.FreshUntil.Before(r.RetainUntil) {
		return ErrInvalid
	}
	return nil
}

// EmptyAllocation references both the real allocation receipt and the settled
// never-written history. A missing file or an empty catalog is not that proof.
type EmptyAllocation struct {
	AllocationSHA256   string                `json:"allocation_sha256"`
	NeverWrittenSHA256 string                `json:"never_written_sha256"`
	DatabaseID         data.DatabaseID       `json:"database_id"`
	IncarnationID      data.AppIncarnationID `json:"incarnation_id"`
	Location           Location              `json:"location"`
}

func (e EmptyAllocation) Validate() error {
	if !data.ValidCatalogHash(e.AllocationSHA256) || !data.ValidCatalogHash(e.NeverWrittenSHA256) || !data.ValidID(string(e.DatabaseID)) || !data.ValidID(string(e.IncarnationID)) || e.Location.Validate() != nil {
		return ErrInvalid
	}
	return nil
}

type Scope struct {
	App              string                `json:"app"`
	DatabaseName     data.DatabaseName     `json:"database_name"`
	IncarnationID    data.AppIncarnationID `json:"incarnation_id"`
	DatabaseID       data.DatabaseID       `json:"database_id"`
	BindingID        data.ReplicaBindingID `json:"binding_id"`
	EpochID          data.ReplicaEpochID   `json:"epoch_id"`
	ReplicaCommitted bool                  `json:"replica_committed"`
	ConfigSHA256     string                `json:"config_sha256"`
	// Delivered version and activated version are distinct for reserved replicas.
	CredentialVersion          uint64                       `json:"credential_version"`
	ActivatedCredentialVersion uint64                       `json:"activated_credential_version"`
	PolicySHA256               string                       `json:"policy_sha256"`
	TargetSHA256               string                       `json:"target_sha256"`
	DeclarationSHA256          string                       `json:"declaration_sha256"`
	RemotePrefix               string                       `json:"remote_prefix"`
	Schema                     data.SQLiteSchemaObservation `json:"schema"`
	Checks                     []data.RestoreInvariant      `json:"checks"`
	Retention                  Retention                    `json:"retention"`
	EmptyAllocation            *EmptyAllocation             `json:"empty_allocation,omitempty"`
}

func (s Scope) Validate() error {
	if !appName.MatchString(s.App) || !appName.MatchString(string(s.DatabaseName)) || !data.ValidID(string(s.IncarnationID)) || !data.ValidID(string(s.DatabaseID)) || !data.ValidID(string(s.BindingID)) || !data.ValidID(string(s.EpochID)) {
		return ErrInvalid
	}
	if s.ReplicaCommitted && !data.ValidCatalogHash(s.ConfigSHA256) || !s.ReplicaCommitted && (s.ConfigSHA256 != "" || s.ActivatedCredentialVersion != 0) {
		return ErrInvalid
	}
	for _, h := range []string{s.PolicySHA256, s.TargetSHA256, s.DeclarationSHA256} {
		if !data.ValidCatalogHash(h) {
			return ErrInvalid
		}
	}
	if s.CredentialVersion == 0 || s.CredentialVersion > 1<<63-1 || s.ActivatedCredentialVersion > 1<<63-1 || s.ReplicaCommitted && s.ActivatedCredentialVersion == 0 || !safePrefix(s.RemotePrefix) || s.Retention.Validate() != nil || !validSchema(s.Schema, s.DatabaseID) || s.Checks == nil || len(s.Checks) > 64 {
		return ErrInvalid
	}
	for _, c := range s.Checks {
		if c.Database != s.DatabaseName || c.Validate() != nil {
			return ErrInvalid
		}
	}
	if s.Schema.State == data.VerifiedEmpty && len(s.Checks) != 0 {
		return ErrInvalid
	}
	if s.Schema.State == data.AllocatedEmpty {
		return s.validateEmpty()
	}
	if s.EmptyAllocation != nil {
		return ErrInvalid
	}
	return nil
}
func (s Scope) validateEmpty() error {
	e := s.EmptyAllocation
	if e == nil || e.Validate() != nil || e.DatabaseID != s.DatabaseID || e.IncarnationID != s.IncarnationID || s.ReplicaCommitted || len(s.Checks) != 0 {
		return ErrInvalid
	}
	return nil
}
func safePrefix(p string) bool {
	if len(p) > 800 || !strings.HasSuffix(p, "/") || strings.IndexFunc(p, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/", r))
	}) >= 0 {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(p, "/"), "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n") {
			return false
		}
	}
	return true
}
func validSchema(s data.SQLiteSchemaObservation, id data.DatabaseID) bool {
	if s.DatabaseID != id || !utc(s.ObservedAt) || s.UnknownReason != "" || !data.ValidCatalogHash(s.CatalogSHA256) {
		return false
	}
	if s.State == data.AllocatedEmpty || s.State == data.VerifiedEmpty {
		return s.Marker == data.EmptyMarker && s.CatalogSHA256 == data.EmptyCatalogSHA256
	}
	return s.State == data.VerifiedSchema && data.ValidMarker(s.Marker) && s.Marker != data.EmptyMarker
}
func sameSchema(a, b data.SQLiteSchemaObservation) bool {
	return a.State == b.State && a.DatabaseID == b.DatabaseID && a.Marker == b.Marker && a.CatalogSHA256 == b.CatalogSHA256 && a.UnknownReason == "" && b.UnknownReason == ""
}
func utc(t time.Time) bool {
	return !t.IsZero() && t.Location() == time.UTC && t.Year() >= 2000 && t.Year() <= 9999
}

type Intent struct {
	Task                Task                 `json:"task"`
	Input               Input                `json:"input"`
	Scope               Scope                `json:"scope"`
	Fence               data.QuiescenceFence `json:"fence"`
	Location            Location             `json:"location"`
	Mode                Mode                 `json:"mode"`
	Arm                 Arm                  `json:"arm"`
	StopWritersRequired bool                 `json:"stop_writers_required"`
	CoverageOrigin      string               `json:"coverage_origin,omitempty"`
	BaselineFrom        string               `json:"baseline_from,omitempty"`
	ClaimedAt           time.Time            `json:"claimed_at"`
}

func (i Intent) Validate() error {
	if i.Input.DeclarationPlanID != "" && (i.Scope.ReplicaCommitted || i.Input.DeliveredCredentialVersion != i.Scope.CredentialVersion) {
		return ErrInvalid
	}
	if !i.Input.Matches(i.Task) || i.Input.Kind != PrepareTask || i.Scope.Validate() != nil || i.Input.App != i.Scope.App || i.Input.DatabaseID != i.Scope.DatabaseID || !validFence(i.Fence, i.Scope) || i.Location.Validate() != nil || !utc(i.ClaimedAt) || !validModeArm(i.Mode, i.Arm) || i.Arm == LTX && !i.Scope.ReplicaCommitted {
		return ErrInvalid
	}
	if i.Scope.Schema.ObservedAt.After(i.ClaimedAt) || i.Scope.Retention.ObservedAt.After(i.ClaimedAt) || !i.ClaimedAt.Before(i.Scope.Retention.FreshUntil) {
		return ErrInvalid
	}
	if i.Scope.EmptyAllocation != nil && (i.Scope.EmptyAllocation.Location != i.Location || i.Arm != Empty) || i.Arm == Empty && i.Scope.EmptyAllocation == nil {
		return ErrInvalid
	}
	if i.Mode == Fresh {
		if i.Input.Supersedes != "" || i.BaselineFrom != "" || i.CoverageOrigin != "" || !i.StopWritersRequired {
			return ErrInvalid
		}
		return nil
	}
	if !data.ValidID(i.Input.Supersedes) {
		return ErrInvalid
	}
	if i.BaselineFrom != "" && (!data.ValidID(i.BaselineFrom) || i.BaselineFrom == i.Input.PreparationID) {
		return ErrInvalid
	}
	switch i.Mode {
	case SupersedeLive:
		if i.CoverageOrigin != "" || i.BaselineFrom != "" || !i.Scope.ReplicaCommitted {
			return ErrInvalid
		}
	case SupersedeReverify:
		if !data.ValidID(i.CoverageOrigin) || i.CoverageOrigin == i.Input.PreparationID || i.BaselineFrom == "" || i.StopWritersRequired {
			return ErrInvalid
		}
	case SupersedeCapture:
		if i.CoverageOrigin != "" {
			return ErrInvalid
		}
	}
	return nil
}
func validFence(f data.QuiescenceFence, s Scope) bool {
	return data.ValidID(string(f.ID)) && token.MatchString(f.OperationID) && f.DatabaseID == s.DatabaseID && f.IncarnationID == s.IncarnationID && f.State == data.FenceHeld
}
