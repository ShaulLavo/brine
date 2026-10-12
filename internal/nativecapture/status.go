package nativecapture

import (
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

type BackupStatus struct {
	Engine                      string     `json:"engine"`
	BindingID                   string     `json:"binding_id"`
	EpochID                     string     `json:"epoch_id"`
	State                       string     `json:"state"`
	ModuleSHA256                string     `json:"module_sha256"`
	SchemaSHA256                string     `json:"schema_sha256"`
	CaptureIntervalSeconds      int        `json:"capture_interval_seconds"`
	VerificationIntervalSeconds int        `json:"verification_interval_seconds"`
	MaintenanceBudgetSeconds    int        `json:"maintenance_budget_seconds"`
	Loss                        string     `json:"loss"`
	PointID                     string     `json:"point_id"`
	PointSHA256                 string     `json:"point_sha256"`
	CapturedAt                  *time.Time `json:"captured_at"`
	VerifiedAt                  *time.Time `json:"verified_at"`
	Fenced                      bool       `json:"fenced"`
	CredentialExpired           bool       `json:"credential_expired"`
	MissedCaptureIntervals      int64      `json:"missed_capture_intervals"`
	VerifiedCaptureAgeSeconds   *int64     `json:"verified_capture_age_seconds"`
}

func (s *BackupStatus) ObserveCadence(now, activatedAt time.Time) error {
	if s == nil || s.CaptureIntervalSeconds <= 0 || now.IsZero() || activatedAt.IsZero() || now.Before(activatedAt) {
		return ErrInvalid
	}
	baseline := activatedAt
	s.VerifiedCaptureAgeSeconds = nil
	if s.CapturedAt != nil {
		if now.Before(*s.CapturedAt) {
			return ErrInvalid
		}
		age := int64(now.Sub(*s.CapturedAt) / time.Second)
		s.VerifiedCaptureAgeSeconds = &age
		if baseline.Before(*s.CapturedAt) {
			baseline = *s.CapturedAt
		}
	}
	s.MissedCaptureIntervals = int64(now.Sub(baseline) / (time.Duration(s.CaptureIntervalSeconds) * time.Second))
	return nil
}

func (s BackupStatus) Valid() bool {
	if s.MissedCaptureIntervals < 0 || s.VerifiedCaptureAgeSeconds != nil && *s.VerifiedCaptureAgeSeconds < 0 {
		return false
	}
	if s.Engine != "spacetimedb" || !idPattern.MatchString(s.BindingID) || !idPattern.MatchString(s.EpochID) || s.Loss != "unknown" {
		return false
	}
	if s.State == "inactive" || s.State == "recovery_required" {
		return s.CaptureIntervalSeconds == 0 && s.VerificationIntervalSeconds == 0 && s.MaintenanceBudgetSeconds == 0 && s.ModuleSHA256 == "" && s.SchemaSHA256 == "" && s.PointID == "" && s.PointSHA256 == "" && s.CapturedAt == nil && s.VerifiedAt == nil && s.VerifiedCaptureAgeSeconds == nil && s.MissedCaptureIntervals == 0 && !s.CredentialExpired
	}
	if s.CaptureIntervalSeconds <= 0 || s.VerificationIntervalSeconds <= 0 || s.MaintenanceBudgetSeconds <= 0 {
		return false
	}
	if s.State != "unverified" && s.State != "verified" && s.State != "degraded" {
		return false
	}
	if s.ModuleSHA256 != "" && !hashPattern.MatchString(s.ModuleSHA256) || s.SchemaSHA256 != "" && !hashPattern.MatchString(s.SchemaSHA256) {
		return false
	}
	if s.PointID == "" {
		return s.PointSHA256 == "" && s.CapturedAt == nil && s.VerifiedAt == nil && s.VerifiedCaptureAgeSeconds == nil && s.State != "verified"
	}
	return idPattern.MatchString(s.PointID) && hashPattern.MatchString(s.PointSHA256) && s.CapturedAt != nil && s.VerifiedAt != nil && s.VerifiedCaptureAgeSeconds != nil && s.CapturedAt.Location() == time.UTC && s.VerifiedAt.Location() == time.UTC && !s.VerifiedAt.Before(*s.CapturedAt)
}
func DecodeBackupStatus(raw []byte) (BackupStatus, error) {
	var value BackupStatus
	if len(raw) > 8192 {
		return value, ErrLimit
	}
	fields := []string{"engine", "binding_id", "epoch_id", "state", "module_sha256", "schema_sha256", "capture_interval_seconds", "verification_interval_seconds", "maintenance_budget_seconds", "loss", "point_id", "point_sha256", "captured_at", "verified_at", "fenced", "credential_expired", "missed_capture_intervals", "verified_capture_age_seconds"}
	if _, err := strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &value) != nil || !value.Valid() {
		return value, ErrInvalid
	}
	return value, nil
}
