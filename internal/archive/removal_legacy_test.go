package archive

import (
	"testing"
	"time"
)

func fixedLegacyPlan(t *testing.T) Plan {
	t.Helper()
	p := archivePlan(t)
	p.CreatedAt = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	p.Candidate.Databases[0].sqlite.Allocation.AllocatedAt = p.CreatedAt.Add(-time.Hour)
	p.Candidate.Databases[0].sqlite.Retention[0].VerifiedAt = p.CreatedAt.Add(-time.Minute).Format(time.RFC3339Nano)
	p.ID, _ = p.Fingerprint()
	return p
}

func TestRemovalLegacyFingerprintGolden(t *testing.T) {
	p := fixedLegacyPlan(t)
	if p.ID != "sha256:f675549bba6fb8d4ec11cf7391e3def41bcdda3c8ac0e306941ea342bb1a1010" {
		t.Fatalf("legacy fingerprint %s", p.ID)
	}
}
