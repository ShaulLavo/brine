package nativecapture

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBackupStatusReportsMissedVerificationCadenceWithoutClaimingLoss(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	captured := now.Add(-75 * time.Second)
	verified := captured.Add(5 * time.Second)
	status := BackupStatus{Engine: "spacetimedb", BindingID: strings.Repeat("a", 32), EpochID: strings.Repeat("b", 32), State: "degraded", CaptureIntervalSeconds: 30, VerificationIntervalSeconds: 60, MaintenanceBudgetSeconds: 300, Loss: "unknown", PointID: strings.Repeat("c", 32), PointSHA256: strings.Repeat("d", 64), CapturedAt: &captured, VerifiedAt: &verified}
	if err := status.ObserveCadence(now, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if status.MissedCaptureIntervals != 2 || status.VerifiedCaptureAgeSeconds == nil || *status.VerifiedCaptureAgeSeconds != 75 || status.Loss != "unknown" {
		t.Fatal("missed verified captures were hidden or presented as a proven loss window")
	}
	raw, _ := json.Marshal(status)
	decoded, err := DecodeBackupStatus(raw)
	if err != nil || decoded.MissedCaptureIntervals != 2 || *decoded.VerifiedCaptureAgeSeconds != 75 {
		t.Fatal("status measurement lost over machine API", err)
	}
	if err := status.ObserveCadence(captured.Add(-time.Second), now.Add(-time.Hour)); err == nil {
		t.Fatal("future capture produced a wrapped age")
	}
}

func TestBackupStatusCountsFirstMissedCaptureFromActivation(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	status := BackupStatus{CaptureIntervalSeconds: 30, Loss: "unknown"}
	if err := status.ObserveCadence(now, now.Add(-65*time.Second)); err != nil || status.MissedCaptureIntervals != 2 || status.VerifiedCaptureAgeSeconds != nil {
		t.Fatal("never-verified policy hid overdue captures", err)
	}
}

func TestInactiveBackupStatusCannotClaimActiveCadenceOrVerifiedPoint(t *testing.T) {
	for _, state := range []string{"inactive", "recovery_required"} {
		status := BackupStatus{Engine: "spacetimedb", BindingID: strings.Repeat("a", 32), EpochID: strings.Repeat("b", 32), State: state, Loss: "unknown", Fenced: true}
		raw, _ := json.Marshal(status)
		if _, err := DecodeBackupStatus(raw); err != nil {
			t.Fatal("no-policy status refused", err)
		}
		status.CaptureIntervalSeconds = 30
		if status.Valid() {
			t.Fatal("inactive policy claimed active cadence")
		}
		status.CaptureIntervalSeconds = 0
		status.PointID = strings.Repeat("c", 32)
		if status.Valid() {
			t.Fatal("inactive policy claimed a verified point")
		}
	}
}
