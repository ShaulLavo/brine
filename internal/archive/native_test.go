package archive

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func nativeArchiveFixture(t *testing.T) (Plan, NativeDatabase) {
	t.Helper()
	p := archivePlan(t)
	sqlite, _ := p.Candidate.Databases[0].SQLite()
	b := sqlite.Binding
	native := NativeDatabase{Binding: data.SpacetimeDBBinding{DatabaseID: b.DatabaseID, BindingID: b.ReplicaBindingID, IncarnationID: b.IncarnationID, Name: b.Name, Root: b.Root, RelativeDirectory: b.RelativeDirectory},
		Head: data.EngineHeadRef{BindingID: b.ReplicaBindingID, EpochID: sqlite.Epochs[0].EpochID, Version: 2, RuntimeSHA256: strings.Repeat("a", 64)}, State: data.EngineInitialized, Allocation: sqlite.Allocation,
		Epochs: p.Candidate.Databases[0].ArchiveBindings(), Retention: sqlite.Retention,
		Dependencies:    []data.EngineEvidenceRef{{Kind: data.EngineMaterialEvidence, ID: strings.Repeat("f", 32), SHA256: strings.Repeat("b", 64), DatabaseID: b.DatabaseID, BindingID: b.ReplicaBindingID, EpochID: sqlite.Epochs[0].EpochID}},
		TransitionTrees: []NativeTransitionTree{{TransitionID: strings.Repeat("f", 32), Stage: data.EngineTransitionCompleted, Seal: data.EngineTreeSeal{Root: data.EngineDirectoryIdentity{Device: 1, Inode: 2}, Parent: data.EngineDirectoryIdentity{Device: 1, Inode: 5}, Directory: data.EngineDirectoryIdentity{Device: 1, Inode: 6}, ContentsSHA256: strings.Repeat("c", 64)}}}}
	p.Candidate.Databases[0] = NativeDatabaseVariant(native)
	var err error
	p.ID, err = p.Fingerprint()
	if err != nil || !p.Valid() {
		t.Fatal("native structural archive fixture invalid", err)
	}
	return p, native
}

func TestNativeArchiveReviewRetainsEpochsAndTerminalTransitionTrees(t *testing.T) {
	p, native := nativeArchiveFixture(t)
	if p.Candidate.Databases[0].Engine() != data.EngineSpacetimeDB || len(p.Candidate.Databases[0].ArchiveBindings()) != 1 {
		t.Fatal("native archive arm lost identity")
	}
	native.TransitionTrees[0].Stage = data.EngineTransitionPreserveIntent
	p.Candidate.Databases[0] = NativeDatabaseVariant(native)
	p.ID, _ = p.Fingerprint()
	if p.Valid() {
		t.Fatal("unknown/nonterminal transition tree admitted archive review")
	}
}

func TestNativeArchiveAllowsAbsentAbandonedPublicationTreeOnly(t *testing.T) {
	p, native := nativeArchiveFixture(t)
	tree := &native.TransitionTrees[0]
	tree.Stage, tree.Optional, tree.Seal = data.EngineTransitionAbandoned, true, data.EngineTreeSeal{}
	p.Candidate.Databases[0] = NativeDatabaseVariant(native)
	p.ID, _ = p.Fingerprint()
	if !p.Valid() {
		t.Fatal("abandoned publication that never created a target blocked archive")
	}
	raw, _ := json.Marshal(p)
	var decoded Plan
	if err := json.Unmarshal(raw, &decoded); err != nil || !decoded.Valid() {
		t.Fatal("abandoned publication tree failed archive plan round trip", err)
	}
	tree.Optional = false
	p.Candidate.Databases[0] = NativeDatabaseVariant(native)
	p.ID, _ = p.Fingerprint()
	if p.Valid() {
		t.Fatal("missing required transition tree admitted")
	}
}

func TestNativeArchiveReviewRejectsMissingCurrentEpochAndRecoveryDependency(t *testing.T) {
	p, native := nativeArchiveFixture(t)
	native.Head.EpochID = data.ReplicaEpochID(strings.Repeat("0", 32))
	p.Candidate.Databases[0] = NativeDatabaseVariant(native)
	p.ID, _ = p.Fingerprint()
	if p.Valid() {
		t.Fatal("head without retained recovery epoch admitted")
	}
	p, native = nativeArchiveFixture(t)
	native.Dependencies = nil
	p.Candidate.Databases[0] = NativeDatabaseVariant(native)
	p.ID, _ = p.Fingerprint()
	if p.Valid() {
		t.Fatal("missing retained recovery dependency admitted")
	}
}

func TestNativeArchiveCoverageCannotUseSQLiteOrAnotherHead(t *testing.T) {
	p, native := nativeArchiveFixture(t)
	at := p.CreatedAt.Add(time.Second)
	coverage := NativeCoverage{DatabaseID: native.Binding.DatabaseID, Head: native.Head,
		Point:   data.EngineEvidenceRef{Kind: data.EnginePreparedPointEvidence, ID: strings.Repeat("e", 32), SHA256: strings.Repeat("a", 64), DatabaseID: native.Binding.DatabaseID, BindingID: native.Binding.BindingID, EpochID: native.Head.EpochID},
		FenceID: data.FenceID(strings.Repeat("e", 32)), FenceOwnerOpID: "original-prepare", SourceWitnessSHA256: strings.Repeat("b", 64), CheckpointSHA256: strings.Repeat("c", 64), RestoreReceiptSHA256: strings.Repeat("d", 64), StopProofSHA256: strings.Repeat("e", 64), WritersQuiescedAt: at, VerifiedAt: at, ExpiresAt: at.Add(5 * time.Minute)}
	if !NativeCoverageVariant(coverage).Admits(p, p.Candidate.Databases[0], "retire-apply", at) {
		t.Fatal("matching review scope refused")
	}
	if SQLiteCoverageVariant(SQLiteCoverage{DatabaseID: coverage.DatabaseID}).Admits(p, p.Candidate.Databases[0], "retire-apply", at) {
		t.Fatal("SQLite evidence admitted native recovery")
	}
	coverage.Head.Version++
	if NativeCoverageVariant(coverage).Admits(p, p.Candidate.Databases[0], "retire-apply", at) {
		t.Fatal("different native head admitted exact coverage")
	}
}
