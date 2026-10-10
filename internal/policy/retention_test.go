package policy

import (
	"strings"
	"testing"
)

func TestProtectedRetentionEvidenceBindsExactDestination(t *testing.T) {
	destination := `
[[backup_destinations]]
reference="primary"
endpoint="https://storage.example"
region="region-1"
bucket="backups"
base_prefix="brine"
credential_ref="primary"
`
	retention := `
[[backup_retention]]
destination="primary"
endpoint="https://storage.example"
bucket="backups"
base_prefix="brine"
verification_id="11111111111111111111111111111111"
verified_at="2026-10-10T12:00:00Z"
freshness_seconds=3600
no_object_expiration=true
`
	p, err := Parse(append(fixture(t), []byte(destination+retention)...))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := p.BackupRetention("primary")
	if !ok || !got.NoObjectExpiration || got.FreshnessSeconds != 3600 {
		t.Fatal("retention scope lost")
	}
	for _, change := range []struct{ old, new string }{
		{`destination="primary"`, `destination="other"`},
		{`bucket="backups"`, `bucket="different"`},
		{`base_prefix="brine"`, `base_prefix="other"`},
		{`endpoint="https://storage.example"`, `endpoint="https://different.example"`},
		{`verification_id="11111111111111111111111111111111"`, `verification_id="invalid"`},
		{`verified_at="2026-10-10T12:00:00Z"`, `verified_at="not-a-time"`},
		{`freshness_seconds=3600`, `freshness_seconds=0`},
		{`freshness_seconds=3600`, `freshness_seconds=2592001`},
	} {
		t.Run(change.old, func(t *testing.T) {
			text := strings.Replace(retention, change.old, change.new, 1)
			if _, err := Parse(append(fixture(t), []byte(destination+text)...)); err == nil {
				t.Fatal("invalid protected retention scope accepted")
			}
		})
	}
	if _, err = Parse(append(fixture(t), []byte(destination+retention+retention)...)); err == nil {
		t.Fatal("duplicate retention verification accepted")
	}
}
