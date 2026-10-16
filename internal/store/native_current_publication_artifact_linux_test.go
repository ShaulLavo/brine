//go:build linux

package store

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestRestoredEpochUsesSelectedPublicationArtifact(t *testing.T) {
	f := newNativeTransitionFixture(t)
	c := f.input.Common()
	source := c.Base.Head().EpochID
	var err error
	f.input, err = data.NewEngineRestoreTransition(c, data.EngineRestoreTarget{SelectedPoint: storeTransitionEvidence(c.Scope, source, data.EngineRestorePointEvidence, 9)})
	if err != nil {
		t.Fatal(err)
	}
	cursor := activationCursorFixture(t, f)
	tx, cancel, err := f.store.beginWrite(t.Context())
	defer cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	activated, err := f.store.activateEngineTransitionTx(t.Context(), tx, f.preparation, cursor, f.runtime, f.allocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := nativePublicationArtifactEpoch(t.Context(), f.store.db, c.Scope.BindingID, c.NewEpochID); err == nil {
		t.Error("unfinished restore supplied historical artifact authority")
	}
	finish := advanceStoreTransition(t, f, activated, data.EngineTransitionFinishIntent, data.EngineLayoutPromoted)
	tx, cancel2, err := f.store.beginWrite(t.Context())
	defer cancel2()
	if err != nil {
		t.Fatal(err)
	}
	proof := storeTransitionEvidence(c.Scope, c.NewEpochID, data.EngineCompletionEvidence, 10)
	if _, err := f.store.finishEngineTransitionTx(t.Context(), tx, f.preparation, finish, proof, false); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	epoch, err := nativePublicationArtifactEpoch(t.Context(), f.store.db, c.Scope.BindingID, c.NewEpochID)
	if err != nil || epoch != source {
		t.Fatal("completed restore lost selected publication epoch", epoch, err)
	}
}
