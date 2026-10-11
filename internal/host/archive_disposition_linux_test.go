//go:build linux

package host

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

func TestArchiveAbandonAllowsChangedContextButNeverChangedOwnership(t *testing.T) {
	planned := archive.Plan{TargetHash: "same", PolicyHash: "old", Generation: 1, Candidate: archive.Candidate{App: "hello", Incarnation: "original", ReleaseID: "release", Databases: []archive.Database{{Binding: data.DatabaseBinding{DatabaseID: "database"}, Retention: []data.RetentionEvidence{{VerificationID: "old"}}}}}}
	observed := planned
	observed.PolicyHash = "new"
	observed.Generation = 2
	observed.Candidate.Databases = []archive.Database{{Binding: planned.Candidate.Databases[0].Binding, Retention: nil}}
	if !archiveDispositionFactsMatch(planned, observed, true) {
		t.Fatal("unrelated changes stranded abandonment")
	}
	if archiveDispositionFactsMatch(planned, observed, false) {
		t.Fatal("changed context authorized retirement")
	}
	for _, change := range []func(*archive.Plan){func(p *archive.Plan) { p.Candidate.Incarnation = "recreated" }, func(p *archive.Plan) { p.Candidate.ReleaseID = "new-release" }, func(p *archive.Plan) { p.TargetHash = "other-host" }, func(p *archive.Plan) { p.Candidate.Databases = nil }} {
		changed := observed
		change(&changed)
		if archiveDispositionFactsMatch(planned, changed, true) {
			t.Fatal("changed ownership authorized fence release")
		}
	}
	if len(planned.Candidate.Databases[0].Retention) != 1 || observed.Candidate.Databases[0].Retention != nil {
		t.Fatal("comparison mutated borrowed evidence")
	}
}
