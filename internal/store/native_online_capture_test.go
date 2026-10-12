package store

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restic"
)

func TestNativeOnlineCaptureRequiresActualGuardAndTaskBeforeEffects(t *testing.T) {
	var absent *NativeOnlineCapture
	if _, err := absent.Permit(context.Background()); err == nil {
		t.Fatal("missing actual capture guard read permit")
	}
	if absent.AdmitRepository(context.Background(), restic.Scope{}, restic.CreateRepository, "") == nil {
		t.Fatal("online capture created repository")
	}
	if _, err := absent.Complete(context.Background(), restic.AuthenticatedRestore{}, NativePreparedObservation{}); err == nil {
		t.Fatal("missing claim committed historical point")
	}
	if nativeHasHeldFence([]data.QuiescenceFence{{State: data.FenceReleased}}) {
		t.Fatal("historical released fence blocked routine capture")
	}
	if !nativeHasHeldFence([]data.QuiescenceFence{{State: data.FenceHeld}}) {
		t.Fatal("actual held fence admitted routine capture")
	}
}
