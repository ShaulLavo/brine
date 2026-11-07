package archive

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func removalManyDatabases(t *testing.T, count int) Plan {
	t.Helper()
	p, d, _, _ := removalFixture(t)
	template := p.Removal.Points[0]
	p.Candidate.Databases = nil
	p.Removal.Points = nil
	for i := range count {
		database := cloneSQLiteDatabase(d)
		database.Binding.DatabaseID = data.DatabaseID(fmt.Sprintf("%032x", i+1))
		database.Binding.ReplicaBindingID = data.ReplicaBindingID(fmt.Sprintf("%032x", i+33))
		database.Binding.RelativeDirectory, _ = data.RelativeDirectory(p.Candidate.Incarnation, database.Binding.DatabaseID)
		database.Allocation.DatabaseID = database.Binding.DatabaseID
		database.Allocation.DirectoryInode = uint64(i + 10)
		epoch := &database.Epochs[0]
		epoch.DatabaseID, epoch.BindingID = database.Binding.DatabaseID, database.Binding.ReplicaBindingID
		epoch.EpochID = data.ReplicaEpochID(fmt.Sprintf("%032x", i+17))
		epoch.RemotePrefix, _ = data.RemotePrefix(epoch.Destination.BasePrefix, p.Candidate.Incarnation, epoch.DatabaseID, epoch.EpochID)
		ref := template
		ref.DatabaseID, ref.Fence.DatabaseID = database.Binding.DatabaseID, database.Binding.DatabaseID
		ref.Fence.ID = data.FenceID(fmt.Sprintf("%032x", i+49))
		p.Candidate.Databases = append(p.Candidate.Databases, SQLiteDatabaseVariant(database))
		p.Removal.Points = append(p.Removal.Points, ref)
	}
	p.ID, _ = p.Fingerprint()
	return p
}

func TestRemovalPlanSortedUniqueDatabaseFencesAndLimits(t *testing.T) {
	p := removalManyDatabases(t, 16)
	requireRemovalValidity(t, p, true)
	p.Removal.Points[1].Fence.ID = p.Removal.Points[0].Fence.ID
	requireRemovalValidity(t, p, false)
	p = removalManyDatabases(t, 2)
	p.Removal.Points[0], p.Removal.Points[1] = p.Removal.Points[1], p.Removal.Points[0]
	requireRemovalValidity(t, p, false)
	p = removalManyDatabases(t, 2)
	p.Removal.Points = p.Removal.Points[:1]
	requireRemovalValidity(t, p, false)
	p = removalManyDatabases(t, 17)
	requireRemovalValidity(t, p, false)
}

func TestRemovalPlanRejectsGenuineNativeAndMixedCandidates(t *testing.T) {
	native, _ := nativeArchiveFixture(t)
	p, _, _, _ := removalFixture(t)
	native.Removal = p.Removal
	requireRemovalValidity(t, native, false)
	p.Candidate.Databases = append(p.Candidate.Databases, native.Candidate.Databases[0])
	requireRemovalValidity(t, p, false)
}

func TestRemovalPlanFingerprintBindsAllRemovalFields(t *testing.T) {
	cases := map[string]func(*Removal){
		"bounds":       func(r *Removal) { r.Bounds.MaxBackupAgeSeconds++ },
		"expiry":       func(r *Removal) { r.ExpiresAt = r.ExpiresAt.Add(-time.Second) },
		"prepared":     func(r *Removal) { r.Points[0].PreparedSHA256 = strings.Repeat("1", 64) },
		"source":       func(r *Removal) { r.Points[0].SourcePointID = strings.Repeat("1", 32) },
		"fence owner":  func(r *Removal) { r.Points[0].Fence.OperationID = "different-preparation" },
		"credentials":  func(r *Removal) { r.Points[0].CredentialVersion++ },
		"destination":  func(r *Removal) { r.Points[0].DestinationSHA256 = strings.Repeat("1", 64) },
		"retention":    func(r *Removal) { r.Points[0].RetentionSHA256 = strings.Repeat("1", 64) },
		"location":     func(r *Removal) { r.Points[0].LocationSHA256 = strings.Repeat("1", 64) },
		"cutoff":       func(r *Removal) { r.Points[0].Cutoff = r.Points[0].Cutoff.Add(-time.Second) },
		"point expiry": func(r *Removal) { r.Points[0].ExpiresAt = r.Points[0].ExpiresAt.Add(time.Second) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p, _, _, _ := removalFixture(t)
			mutate(p.Removal)
			requireChangedRemovalFingerprint(t, p)
		})
	}
}

func requireChangedRemovalFingerprint(t *testing.T, p Plan) {
	t.Helper()
	id, err := p.Fingerprint()
	if err != nil || id == p.ID {
		t.Fatalf("removal change was not fingerprinted: %v", err)
	}
}

func TestRemovalPreparedCodecRejectsInvalidNewFields(t *testing.T) {
	_, _, c, _ := removalFixture(t)
	raw, _ := json.Marshal(SQLiteCoverageVariant(c))
	text := string(raw)
	cases := map[string]string{
		"unknown prepared":   strings.Replace(text, "\"prepared\":{", "\"prepared\":{\"extra\":true,", 1),
		"duplicate prepared": strings.Replace(text, "\"prepared\":{", "\"prepared\":{},\"prepared\":{", 1),
		"alias prepared":     strings.Replace(text, "\"prepared\":", "\"Prepared\":", 1),
		"unknown ref":        strings.Replace(text, "\"ref\":{", "\"ref\":{\"extra\":1,", 1),
		"duplicate ref":      strings.Replace(text, "\"ref\":{", "\"ref\":{},\"ref\":{", 1),
		"null cutoff":        strings.Replace(text, "\"cutoff\":\""+c.Prepared.Ref.Cutoff.Format(time.RFC3339Nano)+"\"", "\"cutoff\":null", 1),
		"null claim":         strings.Replace(text, "\"claimed_at\":\""+c.Prepared.ClaimedAt.Format(time.RFC3339Nano)+"\"", "\"claimed_at\":null", 1),
		"alias ref":          strings.Replace(text, "\"prepared_sha256\":", "\"Prepared_SHA256\":", 1),
		"unknown fence":      strings.Replace(text, "\"fence\":{", "\"fence\":{\"extra\":1,", 1),
		"duplicate fence":    strings.Replace(text, "\"operation_id\":\"original-preparation\"", "\"operation_id\":\"original-preparation\",\"operation_id\":\"other\"", 1),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) { requireCoverageDecodeRefusal(t, []byte(text)) })
	}
	for _, field := range []string{"prepared", "ref", "fence", "credential_version", "source_point_id"} {
		t.Run("null "+field, func(t *testing.T) { requireCoverageDecodeRefusal(t, nullRemovalField(t, raw, field)) })
	}

}

func requireCoverageDecodeRefusal(t *testing.T, raw []byte) {
	t.Helper()
	var c Coverage
	if json.Unmarshal(raw, &c) == nil {
		t.Fatal("invalid prepared coverage decoded", string(raw))
	}
}

func TestRemovalPreparedClaimBoundaryAndOriginalTimes(t *testing.T) {
	p, d, c, claim := removalFixture(t)
	cutoff, recorded, observed := c.Prepared.Ref.Cutoff, c.Point.RecordedAt, c.Receipt.ObservedAt
	requirePreparedAdmission(t, p, d, c, claim, true)
	if !c.Prepared.Ref.Cutoff.Equal(cutoff) || !c.Point.RecordedAt.Equal(recorded) || !c.Receipt.ObservedAt.Equal(observed) {
		t.Fatal("admission changed original times")
	}
	p.Removal.Bounds.MaxBackupAgeSeconds = 4
	p.Removal.Bounds.MaxRestoreTestAgeSeconds = 2
	p.ID, _ = p.Fingerprint()
	requirePreparedAdmission(t, p, d, c, claim, true)
	c.Prepared.ClaimedAt = claim.Add(time.Nanosecond)
	requirePreparedAdmission(t, p, d, c, c.Prepared.ClaimedAt, false)
}

func TestRemovalLegacyCoverageBytes(t *testing.T) {
	at := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	c := SQLiteCoverage{DatabaseID: data.DatabaseID(strings.Repeat("b", 32)), WritersQuiescedAt: at, Observation: data.SQLiteSchemaObservation{State: data.VerifiedEmpty, DatabaseID: data.DatabaseID(strings.Repeat("b", 32)), ObservedAt: at, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}}
	raw, err := json.Marshal(SQLiteCoverageVariant(c))
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"database_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","writers_quiesced_at":"2026-11-07T10:00:00Z","observation":{"state":"verified_empty","database_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","observed_at":"2026-11-07T10:00:00Z","marker":"brine-empty-v1","catalog_sha256":"4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"}}`
	if string(raw) != expected {
		t.Fatal("legacy coverage bytes changed", string(raw))
	}
	var decoded Coverage
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatal("legacy coverage no longer decodes")
	}
	again, _ := json.Marshal(decoded)
	if string(again) != expected {
		t.Fatal("legacy roundtrip changed bytes")
	}
}

func TestRemovalPreparedNeverWrittenPointOnlyKeepsFenceContinuity(t *testing.T) {
	p, d, _, _ := removalFixture(t)
	d.Written = false
	p.Candidate.Databases[0] = SQLiteDatabaseVariant(d)
	p.ID, _ = p.Fingerprint()
	now := p.CreatedAt.Add(time.Second)
	allocation := *d.Allocation
	allocation.AllocatedAt = now
	c := SQLiteCoverage{DatabaseID: d.Binding.DatabaseID, WritersQuiescedAt: now, Empty: &allocation, Observation: data.SQLiteSchemaObservation{DatabaseID: d.Binding.DatabaseID, State: data.AllocatedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: now}}
	requirePreparedAdmission(t, p, d, c, now, true)
	p.Removal.Points = nil
	p.ID, _ = p.Fingerprint()
	requirePreparedAdmission(t, p, d, c, now, true)
}

func nullRemovalField(t *testing.T, raw []byte, field string) []byte {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields[field]; exists {
		fields[field] = json.RawMessage("null")
		result, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	key := "prepared"
	if fields["ref"] != nil {
		key = "ref"
	}
	fields[key] = nullRemovalField(t, fields[key], field)
	result, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
