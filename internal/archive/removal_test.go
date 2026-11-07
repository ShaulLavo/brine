package archive

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func removalFixture(t *testing.T) (Plan, SQLiteDatabase, SQLiteCoverage, time.Time) {
	t.Helper()
	p, _, value, verified := initializedCoverage(t)
	c, _ := value.SQLite()
	p.CreatedAt = verified.Add(time.Second)
	c.Receipt.OperationID = "preparation-successor"
	ref := PreparedRef{DatabaseID: c.DatabaseID, PreparationID: strings.Repeat("1", 32), PreparedSHA256: strings.Repeat("a", 64), SourcePointID: c.Point.ID, CredentialVersion: 2, DestinationSHA256: strings.Repeat("b", 64), RetentionSHA256: strings.Repeat("c", 64), LocationSHA256: strings.Repeat("d", 64), Cutoff: c.WritersQuiescedAt, ExpiresAt: p.CreatedAt.Add(8 * time.Minute)}
	ref.Fence = data.QuiescenceFence{ID: data.FenceID(strings.Repeat("2", 32)), DatabaseID: ref.DatabaseID, IncarnationID: p.Candidate.Incarnation, OperationID: "original-preparation", State: data.FenceHeld}
	p.Removal = &Removal{Bounds: data.InitializationBounds{MaxBackupAgeSeconds: 900, MaxRestoreTestAgeSeconds: 300, RecoveryWindowSeconds: 900}, Points: []PreparedRef{ref}, ExpiresAt: ref.ExpiresAt}
	p.ID, _ = p.Fingerprint()
	c.Prepared = &PreparedCoverage{Ref: ref, ClaimedAt: p.CreatedAt.Add(time.Second)}
	d, _ := p.Candidate.Databases[0].SQLite()
	return p, d, c, c.Prepared.ClaimedAt
}

func requireRemovalValidity(t *testing.T, p Plan, want bool) {
	t.Helper()
	p.ID, _ = p.Fingerprint()
	if p.Valid() != want {
		t.Fatalf("Plan.Valid() = %v, want %v", p.Valid(), want)
	}
}

func TestRemovalPlanStructuralRules(t *testing.T) {
	cases := map[string]func(*Plan){
		"written missing ref":    func(p *Plan) { p.Removal.Points = nil },
		"foreign database":       func(p *Plan) { p.Removal.Points[0].DatabaseID = data.DatabaseID(strings.Repeat("3", 32)) },
		"crossed fence database": func(p *Plan) { p.Removal.Points[0].Fence.DatabaseID = data.DatabaseID(strings.Repeat("3", 32)) },
		"crossed incarnation": func(p *Plan) {
			p.Removal.Points[0].Fence.IncarnationID = data.AppIncarnationID(strings.Repeat("3", 32))
		},
		"released fence":       func(p *Plan) { p.Removal.Points[0].Fence.State = data.FenceReleased },
		"invalid owner":        func(p *Plan) { p.Removal.Points[0].Fence.OperationID = "../bad" },
		"zero credentials":     func(p *Plan) { p.Removal.Points[0].CredentialVersion = 0 },
		"invalid digest":       func(p *Plan) { p.Removal.Points[0].PreparedSHA256 = "sha256:" + strings.Repeat("a", 64) },
		"after point expiry":   func(p *Plan) { p.Removal.ExpiresAt = p.Removal.Points[0].ExpiresAt.Add(time.Nanosecond) },
		"expired at planning":  func(p *Plan) { p.Removal.ExpiresAt = p.CreatedAt },
		"past fifteen minutes": func(p *Plan) { p.Removal.ExpiresAt = p.CreatedAt.Add(15*time.Minute + time.Nanosecond) },
		"zero cutoff":          func(p *Plan) { p.Removal.Points[0].Cutoff = time.Time{} },
		"future cutoff":        func(p *Plan) { p.Removal.Points[0].Cutoff = p.CreatedAt.Add(time.Second) },
		"missing bounds":       func(p *Plan) { p.Removal.Bounds = data.InitializationBounds{} },
		"zero recovery window": func(p *Plan) { p.Removal.Bounds.RecoveryWindowSeconds = 0 },
		"too many refs":        func(p *Plan) { p.Removal.Points = make([]PreparedRef, 17) },
		"duplicate ref":        func(p *Plan) { p.Removal.Points = append(p.Removal.Points, p.Removal.Points[0]) },
	}
	p, _, _, _ := removalFixture(t)
	requireRemovalValidity(t, p, true)
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) { p, _, _, _ := removalFixture(t); mutate(&p); requireRemovalValidity(t, p, false) })
	}
}

func TestRemovalPlanNeverWrittenAndNative(t *testing.T) {
	p, _, _, _ := removalFixture(t)
	p.Candidate.Databases[0].sqlite.Written = false
	requireRemovalValidity(t, p, true)
	p.Removal.Points = nil
	requireRemovalValidity(t, p, true)
	p.Candidate.Databases[0] = NativeDatabaseVariant(NativeDatabase{})
	requireRemovalValidity(t, p, false)
}

func requirePreparedAdmission(t *testing.T, p Plan, d SQLiteDatabase, c SQLiteCoverage, now time.Time, want bool) {
	t.Helper()
	if got := c.Admits(p, d, "archive-operation", now); got != want {
		t.Fatalf("prepared coverage admits = %v, want %v", got, want)
	}
}

func TestRemovalPreparedCoverageBindsOriginalEvidence(t *testing.T) {
	cases := map[string]func(*Plan, *SQLiteDatabase, *SQLiteCoverage){
		"plan ref": func(p *Plan, _ *SQLiteDatabase, _ *SQLiteCoverage) {
			p.Removal.Points[0].PreparedSHA256 = strings.Repeat("f", 64)
			p.ID, _ = p.Fingerprint()
		},
		"origin point": func(_ *Plan, _ *SQLiteDatabase, c *SQLiteCoverage) { c.Point.ID = c.Prepared.Ref.PreparationID },
		"coverage cutoff": func(_ *Plan, _ *SQLiteDatabase, c *SQLiteCoverage) {
			c.WritersQuiescedAt = c.WritersQuiescedAt.Add(time.Nanosecond)
		},
		"receipt cutoff": func(_ *Plan, _ *SQLiteDatabase, c *SQLiteCoverage) {
			c.Receipt.Coverage.CoveredThrough = c.Receipt.Coverage.CoveredThrough.Add(time.Nanosecond)
		},
		"relabelled operation": func(_ *Plan, _ *SQLiteDatabase, c *SQLiteCoverage) { c.Receipt.OperationID = "archive-operation" },
		"missing point":        func(_ *Plan, _ *SQLiteDatabase, c *SQLiteCoverage) { c.Point = nil },
		"missing receipt":      func(_ *Plan, _ *SQLiteDatabase, c *SQLiteCoverage) { c.Receipt = nil },
		"empty exception":      func(_ *Plan, d *SQLiteDatabase, c *SQLiteCoverage) { c.Empty = d.Allocation },
		"crossed source": func(_ *Plan, _ *SQLiteDatabase, c *SQLiteCoverage) {
			c.Receipt.Source.Snapshot.SHA256 = strings.Repeat("b", 64)
		},
		"unknown schema": func(_ *Plan, _ *SQLiteDatabase, c *SQLiteCoverage) { c.Observation.UnknownReason = "unknown" },
		"changed schema": func(_ *Plan, _ *SQLiteDatabase, c *SQLiteCoverage) {
			c.Observation.CatalogSHA256 = strings.Repeat("b", 64)
		},
		"unchecked invariants": func(_ *Plan, d *SQLiteDatabase, _ *SQLiteCoverage) {
			d.Invariants = []data.RestoreInvariant{{Database: d.Binding.Name, Kind: "row_count", Table: "commits", Count: 1}}
		},
		"claim before plan": func(p *Plan, _ *SQLiteDatabase, c *SQLiteCoverage) {
			c.Prepared.ClaimedAt = p.CreatedAt.Add(-time.Nanosecond)
		},
		"claim at expiry": func(p *Plan, _ *SQLiteDatabase, c *SQLiteCoverage) { c.Prepared.ClaimedAt = p.Removal.ExpiresAt },
		"stale backup at claim": func(p *Plan, _ *SQLiteDatabase, _ *SQLiteCoverage) {
			p.Removal.Bounds.MaxBackupAgeSeconds = 1
			p.ID, _ = p.Fingerprint()
		},
		"stale verification at claim": func(p *Plan, _ *SQLiteDatabase, _ *SQLiteCoverage) {
			p.Removal.Bounds.MaxRestoreTestAgeSeconds = 1
			p.ID, _ = p.Fingerprint()
		},
	}
	p, d, c, now := removalFixture(t)
	requirePreparedAdmission(t, p, d, c, now, true)
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p, d, c, now := removalFixture(t)
			mutate(&p, &d, &c)
			requirePreparedAdmission(t, p, d, c, now, false)
		})
	}
}

func TestRemovalPreparedCoverageAgeBoundaries(t *testing.T) {
	p, d, c, claim := removalFixture(t)
	requirePreparedAdmission(t, p, d, c, claim.Add(-time.Nanosecond), false)
	requirePreparedAdmission(t, p, d, c, claim.Add(MaxEvidenceAge), true)
	requirePreparedAdmission(t, p, d, c, claim.Add(MaxEvidenceAge+time.Nanosecond), false)
	p.Removal.Bounds.RecoveryWindowSeconds = 10
	p.ID, _ = p.Fingerprint()
	requirePreparedAdmission(t, p, d, c, claim.Add(10*time.Second), true)
	requirePreparedAdmission(t, p, d, c, claim.Add(10*time.Second+time.Nanosecond), false)
}

func TestRemovalPreparedCoverageCannotFallThroughToOwnCoverage(t *testing.T) {
	p, d, value, now := initializedCoverage(t)
	c, _ := value.SQLite()
	c.Prepared = &PreparedCoverage{}
	requirePreparedAdmission(t, p, d.sqlite, c, now, false)
	p, database, prepared, now := removalFixture(t)
	prepared.Prepared = nil
	requirePreparedAdmission(t, p, database, prepared, now, false)
}

func TestRemovalCoverageRoundTripAndClone(t *testing.T) {
	_, _, c, _ := removalFixture(t)
	value := SQLiteCoverageVariant(c)
	c.Prepared.Ref.CredentialVersion++
	copied, _ := value.SQLite()
	if copied.Prepared.Ref.CredentialVersion != 2 {
		t.Fatal("constructor aliases prepared ref")
	}
	copied.Prepared.Ref.CredentialVersion++
	again, _ := value.SQLite()
	if again.Prepared.Ref.CredentialVersion != 2 {
		t.Fatal("accessor aliases prepared ref")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Coverage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	roundtrip, _ := json.Marshal(decoded)
	if string(raw) != string(roundtrip) {
		t.Fatal("prepared bytes changed")
	}
}
