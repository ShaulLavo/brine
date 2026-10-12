package data

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func transitionFixture(t *testing.T) EngineTransition {
	t.Helper()
	common := transitionCommonFixture(t)
	value, err := NewEnginePublicationTransition(common, EnginePublicationTarget{
		CandidateSHA256: strings.Repeat("8", 64), Receipt: transitionEvidence(common.Scope, common.NewEpochID, EnginePublicationEvidence, 8),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func transitionCommonFixture(t *testing.T) EngineTransitionCommon {
	t.Helper()
	scope := EngineTransitionScope{
		ID: strings.Repeat("a", 32), ApplyOperationID: "apply-1", PlanID: "sha256:" + strings.Repeat("1", 64),
		Requester: "deploy:" + strings.Repeat("9", 64), PolicyHash: "sha256:" + strings.Repeat("2", 64),
		TargetHash: "sha256:" + strings.Repeat("3", 64), DesiredHash: "sha256:" + strings.Repeat("4", 64),
		IncarnationID: AppIncarnationID(strings.Repeat("c", 32)), DatabaseID: DatabaseID(strings.Repeat("d", 32)),
		BindingID: ReplicaBindingID(strings.Repeat("b", 32)), ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	oldEpoch := ReplicaEpochID(strings.Repeat("e", 32))
	newEpoch := ReplicaEpochID(strings.Repeat("f", 32))
	base, err := CommittedEngineBase(EngineHeadRef{BindingID: scope.BindingID, EpochID: oldEpoch, Version: 2, RuntimeSHA256: strings.Repeat("5", 64)})
	if err != nil {
		t.Fatal(err)
	}
	root := EngineDirectoryIdentity{Device: 1, Inode: 10}
	stageParent := EngineDirectoryIdentity{Device: 1, Inode: 40}
	return EngineTransitionCommon{
		Engine: EngineSpacetimeDB, Scope: scope, Base: base, NewEpochID: newEpoch,
		FenceID: FenceID(strings.Repeat("6", 32)), FenceOwnerOperationID: "prepare-1",
		PreparedPoint:   transitionEvidence(scope, oldEpoch, EnginePreparedPointEvidence, 1),
		SourceWitness:   transitionEvidence(scope, oldEpoch, EngineSourceWitnessEvidence, 2),
		OldTree:         EngineTreeSeal{Root: root, Parent: EngineDirectoryIdentity{Device: 1, Inode: 20}, Directory: EngineDirectoryIdentity{Device: 1, Inode: 30}, ContentsSHA256: strings.Repeat("6", 64)},
		TargetTree:      EngineTreeSeal{Root: root, Parent: stageParent, Directory: EngineDirectoryIdentity{Device: 1, Inode: 50}, ContentsSHA256: strings.Repeat("7", 64)},
		PreservedParent: stageParent, TargetRuntimeSHA256: strings.Repeat("8", 64),
		TargetVerification:   transitionEvidence(scope, newEpoch, EngineTargetEvidence, 3),
		TargetCheckpoint:     transitionEvidence(scope, newEpoch, EngineCheckpointEvidence, 4),
		TargetMaterial:       transitionEvidence(scope, newEpoch, EngineMaterialEvidence, 5),
		RecoveryRelease:      transitionEvidence(scope, newEpoch, EngineRecoveryEvidence, 6),
		RetainedDependencies: transitionEvidence(scope, newEpoch, EngineDependenciesEvidence, 7),
	}
}

func transitionEvidence(scope EngineTransitionScope, epoch ReplicaEpochID, kind EngineEvidenceKind, ordinal int) EngineEvidenceRef {
	return EngineEvidenceRef{Kind: kind, ID: fmt.Sprintf("%032x", ordinal), SHA256: fmt.Sprintf("%064x", ordinal), DatabaseID: scope.DatabaseID, BindingID: scope.BindingID, EpochID: epoch}
}

func TestTransitionRecordsOwnBytesAndSeparatePurpose(t *testing.T) {
	publication := transitionFixture(t)
	bytesBefore := publication.Bytes()
	decoded, err := DecodeEngineTransition(bytesBefore)
	if err != nil || decoded.SHA256() != publication.SHA256() || !bytes.Equal(decoded.Bytes(), bytesBefore) {
		t.Fatal("publication round trip changed evidence", err)
	}
	bytesBefore[0] = 'x'
	common := publication.Common()
	common.Scope.Requester = "changed"
	if publication.Bytes()[0] != '{' || publication.Common().Scope.Requester == "changed" {
		t.Fatal("caller mutated stored input")
	}
	selected := transitionEvidence(common.Scope, common.Base.epoch(), EngineRestorePointEvidence, 9)
	common.Scope = publication.Common().Scope
	restored, err := NewEngineRestoreTransition(common, EngineRestoreTarget{SelectedPoint: selected})
	if err != nil || restored.Purpose() != "restore" {
		t.Fatal(err)
	}
	if _, exists := restored.Publication(); exists || restored.SHA256() == publication.SHA256() {
		t.Fatal("publication and restore collided")
	}
	if _, err = DecodeEngineTransition(restored.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestUnpublishedIsExplicitAndNeverARestoreBase(t *testing.T) {
	common := transitionCommonFixture(t)
	base, err := UnpublishedEngineBase(EngineUnpublishedBase{
		Head:       common.Base.Head(),
		Allocation: transitionEvidence(common.Scope, common.Base.epoch(), EngineAllocationEvidence, 10),
	})
	if err != nil {
		t.Fatal(err)
	}
	common.Base = base
	publication, err := NewEnginePublicationTransition(common, EnginePublicationTarget{
		CandidateSHA256: strings.Repeat("8", 64), Receipt: transitionEvidence(common.Scope, common.NewEpochID, EnginePublicationEvidence, 8),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeEngineTransition(publication.Bytes()); err != nil {
		t.Fatal(err)
	}
	selected := transitionEvidence(common.Scope, common.Base.epoch(), EngineRestorePointEvidence, 9)
	if _, err = NewEngineRestoreTransition(common, EngineRestoreTarget{SelectedPoint: selected}); err == nil {
		t.Fatal("unpublished state became initialized restore authority")
	}
	if _, err = CommittedEngineBase(EngineHeadRef{BindingID: common.Scope.BindingID, EpochID: common.Base.epoch(), RuntimeSHA256: strings.Repeat("8", 64)}); err == nil {
		t.Fatal("missing head inferred unpublished")
	}
	if _, err = CommittedEngineBase(EngineHeadRef{BindingID: common.Scope.BindingID, EpochID: common.Base.epoch(), Version: math.MaxUint64, RuntimeSHA256: strings.Repeat("8", 64)}); err == nil {
		t.Fatal("unstoreable runtime version accepted")
	}
}

func TestTransitionRejectsCrossedEvidenceAndResourceIdentity(t *testing.T) {
	for _, mutate := range []func(*EngineTransitionCommon){
		func(c *EngineTransitionCommon) { c.Engine = EngineSQLite },
		func(c *EngineTransitionCommon) { c.NewEpochID = c.Base.epoch() },
		func(c *EngineTransitionCommon) { c.TargetVerification = c.PreparedPoint },
		func(c *EngineTransitionCommon) { c.SourceWitness.BindingID = ReplicaBindingID(strings.Repeat("1", 32)) },
		func(c *EngineTransitionCommon) { c.TargetMaterial.EpochID = c.Base.epoch() },
		func(c *EngineTransitionCommon) { c.RecoveryRelease.Kind = EngineTargetEvidence },
		func(c *EngineTransitionCommon) { c.TargetTree.Directory = c.OldTree.Directory },
		func(c *EngineTransitionCommon) { c.TargetTree.Root.Inode++ },
		func(c *EngineTransitionCommon) { c.PreservedParent.Device++ },
		func(c *EngineTransitionCommon) { c.Scope.ApplyOperationID = "../command" },
	} {
		common := transitionCommonFixture(t)
		mutate(&common)
		target := EnginePublicationTarget{CandidateSHA256: strings.Repeat("8", 64), Receipt: transitionEvidence(common.Scope, common.NewEpochID, EnginePublicationEvidence, 8)}
		if _, err := NewEnginePublicationTransition(common, target); err == nil {
			t.Fatal("crossed transition input accepted")
		}
	}
}

func TestTransitionCodecRejectsUnknownAliasesNullsAndMixedArms(t *testing.T) {
	valid := string(transitionFixture(t).Bytes())
	for _, raw := range []string{
		strings.Replace(valid, `"format":`, `"future":true,"format":`, 1),
		strings.Replace(valid, `"purpose":"publish"`, `"purpose":"publish","purpose":"publish"`, 1),
		strings.Replace(valid, `"common":`, `"Common":`, 1),
		strings.Replace(valid, `"purpose":"publish"`, `"restore":{},"purpose":"publish"`, 1),
		strings.Replace(valid, `"engine":"spacetimedb"`, `"engine":null`, 1),
		strings.Replace(valid, `"generation":0`, `"generation":null`, 1),
		strings.Replace(valid, `"inode":10`, `"inode":null`, 1),
		strings.Replace(valid, `"kind":"prepared_point"`, `"kind":"target_verification"`, 1),
		strings.Replace(valid, `"kind":"prepared_point"`, `"kind":null`, 1),
		strings.Replace(valid, `"committed":`, `"unpublished":{},"committed":`, 1),
		valid + `{}`, strings.Repeat(" ", MaxEngineTransitionBytes) + valid,
	} {
		if _, err := DecodeEngineTransition([]byte(raw)); err == nil {
			t.Fatal("malformed wire input accepted", raw[:min(len(raw), 120)])
		}
	}
}
