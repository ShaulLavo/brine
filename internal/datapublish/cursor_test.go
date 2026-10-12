package datapublish

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func cursorFixture(t *testing.T, create bool) Cursor {
	t.Helper()
	plan, err := NewPlan(planFixture(t, create))
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := NewCursor(plan, "apply-fixture", strings.Repeat("a", 64), strings.Repeat("8", 32))
	if err != nil {
		t.Fatal(err)
	}
	return cursor
}

func observationFixture(cursor Cursor, step Step) Observation {
	value := Observation{Scope: cursor.Scope(), Step: step, Outcome: ObservedCompleted, ProofSHA256: strings.Repeat("f", 64), Requirements: []RequirementProof{}}
	if step == RestoreStage || step == CheckPreflight {
		if cursor.plan.source.kind == CreateSource {
			return value
		}
		point := cursor.plan.source.point
		value.NativeDatabase = point.NativeDatabase
		value.NativePublisher = point.NativePublisher
		value.ProgramWireHash = point.ProgramWireHash
		value.SchemaSHA256 = cursor.plan.source.schema.SHA256()
		if step == CheckPreflight {
			value.ProofSHA256 = cursor.plan.preflight.ResponseSHA256
		}
		return value
	}
	value.NativeDatabase = cursor.nativeDatabase
	value.NativePublisher = cursor.nativePublisher
	if value.NativeDatabase == "" {
		value.NativeDatabase = strings.Repeat("d", 64)
		value.NativePublisher = strings.Repeat("b", 64)
	}
	value.ProgramWireHash = cursor.plan.candidate.program
	value.SchemaSHA256 = cursor.plan.candidate.schema.SHA256()
	if step != VerifyStage {
		return value
	}
	for _, requirement := range cursor.plan.requirements {
		value.Requirements = append(value.Requirements, RequirementProof{Requirement: requirement, SHA256: strings.Repeat("c", 64)})
	}
	return value
}

func completeStep(t *testing.T, cursor Cursor, step Step) Cursor {
	t.Helper()
	next, err := cursor.Intend(step)
	if err != nil {
		t.Fatal(err)
	}
	next, err = next.Observe(observationFixture(next, step))
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func readyToPublish(t *testing.T, create bool) Cursor {
	t.Helper()
	cursor := cursorFixture(t, create)
	cursor = completeStep(t, cursor, RestoreStage)
	return completeStep(t, cursor, CheckPreflight)
}

func TestUnknownPublicationCannotRepublishOrSkipDataProof(t *testing.T) {
	ready := readyToPublish(t, false)
	pending, err := ready.Intend(PublishStage)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := pending.MarkUnknown()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeCursor(raw, unknown.plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restored.NextStep(); ok {
		t.Fatal("unknown effect proposed continuation")
	}
	if _, err = restored.Intend(PublishStage); !errors.Is(err, ErrInspection) {
		t.Fatal("unknown publish replayed", err)
	}
	wrong := observationFixture(restored, PublishStage)
	wrong.Scope.PointID = strings.Repeat("0", 32)
	if _, err = restored.Observe(wrong); !errors.Is(err, ErrInspection) {
		t.Fatal("foreign point adopted", err)
	}
	observed, err := restored.Observe(observationFixture(restored, PublishStage))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = observed.Intend(PublishStage); !errors.Is(err, ErrInspection) {
		t.Fatal("observed publish repeated", err)
	}
	verify, err := observed.Intend(VerifyStage)
	if err != nil {
		t.Fatal(err)
	}
	proof := observationFixture(verify, VerifyStage)
	proof.Requirements = proof.Requirements[:len(proof.Requirements)-1]
	if _, err = verify.Observe(proof); !errors.Is(err, ErrInspection) {
		t.Fatal("default projection was omitted", err)
	}
	if _, err = observed.Intend(FinalTransition); !errors.Is(err, ErrInspection) {
		t.Fatal("unverified stage reached transition", err)
	}
}

func TestCursorOwnsProofsAndDefersFinalTransition(t *testing.T) {
	ready := readyToPublish(t, false)
	observed := completeStep(t, ready, PublishStage)
	pending, err := observed.Intend(VerifyStage)
	if err != nil {
		t.Fatal(err)
	}
	proof := observationFixture(pending, VerifyStage)
	verified, err := pending.Observe(proof)
	if err != nil {
		t.Fatal(err)
	}
	proof.Requirements[0].Requirement = "safe"
	raw, err := json.Marshal(verified)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCursor(raw, verified.plan)
	if err != nil {
		t.Fatal("caller changed retained proof references", err)
	}
	if step, ok := decoded.NextStep(); !ok || step != DisposeStage {
		t.Fatal("stage verifier disposal lost")
	}
	if step, ok := pending.NextStep(); ok || step != "" {
		t.Fatal("later cursor mutated earlier pending branch")
	}
	disposed := completeStep(t, decoded, DisposeStage)
	if step, ok := disposed.NextStep(); !ok || step != FinalTransition {
		t.Fatal("transition did not require external receipt")
	}
	finished := completeStep(t, disposed, FinalTransition)
	if finished.State() != Finished {
		t.Fatal("external transition receipt not retained")
	}
	if _, err = finished.Intend(PublishStage); !errors.Is(err, ErrInspection) {
		t.Fatal("finished attempt restarted", err)
	}
}

func TestCreateAdoptsNativeIdentityOnlyAfterPrivatePublication(t *testing.T) {
	start := cursorFixture(t, true)
	if start.NativeDatabase() != "" {
		t.Fatal("fresh bundle invented native database")
	}
	pending, err := start.Intend(RestoreStage)
	if err != nil {
		t.Fatal(err)
	}
	guess := observationFixture(pending, RestoreStage)
	guess.NativeDatabase = strings.Repeat("d", 64)
	if _, err = pending.Observe(guess); !errors.Is(err, ErrInspection) {
		t.Fatal("desired identity adopted before create", err)
	}
	ready := readyToPublish(t, true)
	if ready.NativeDatabase() != "" {
		t.Fatal("preflight invented native history")
	}
	published := completeStep(t, ready, PublishStage)
	if published.NativeDatabase() != strings.Repeat("d", 64) {
		t.Fatal("observed private native identity not retained")
	}
}

func TestKnownUnperformedAttemptStaysConsumed(t *testing.T) {
	ready := readyToPublish(t, false)
	pending, err := ready.Intend(PublishStage)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := pending.MarkUnknown()
	if err != nil {
		t.Fatal(err)
	}
	proof := observationFixture(unknown, PublishStage)
	proof.Outcome = ObservedNotPerformed
	if _, err = unknown.Observe(proof); !errors.Is(err, ErrInspection) {
		t.Fatal("changed destination labelled not performed", err)
	}
	proof.ProgramWireHash = unknown.plan.source.point.ProgramWireHash
	proof.SchemaSHA256 = unknown.plan.source.schema.SHA256()
	failed, err := unknown.Observe(proof)
	if err != nil || failed.State() != AttemptFailed {
		t.Fatal("known no-effect attempt lost", err)
	}
	if _, err = failed.Intend(PublishStage); !errors.Is(err, ErrInspection) {
		t.Fatal("consumed attempt automatically republished", err)
	}
}
