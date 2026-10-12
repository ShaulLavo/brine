package store

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/restic"
)

func TestNativeBackupActivationRequiresActualClaimBeforeTransportAndCommit(t *testing.T) {
	var absent *NativeBackupActivation
	if _, err := absent.Metadata(context.Background()); err == nil {
		t.Fatal("missing claim disclosed backup material")
	}
	if absent.AdmitRepository(context.Background(), restic.Scope{}, restic.CreateRepository, "") == nil {
		t.Fatal("missing claim created encrypted repository")
	}
	if absent.BeginInitialization(context.Background()) == nil {
		t.Fatal("missing claim issued initialization")
	}
	if absent.RecordInitialized(context.Background(), restic.InitializedRepository{}) == nil {
		t.Fatal("missing claim stored config identity")
	}
	if absent.BeginUnits(context.Background()) == nil {
		t.Fatal("missing claim installed backup units")
	}
	if _, err := absent.Commit(context.Background(), NativeBackupActivationObservation{}); err == nil {
		t.Fatal("missing claim activated backup policy")
	}
}
