package store

import (
	"github.com/ShaulLavo/brine/internal/datapublish"
	"testing"
)

func TestInitialPublisherAndPublicationShareClosedGuestStopCodes(t *testing.T) {
	for _, code := range []string{"guest_child_stop_interrupt", "guest_child_stop_context", "guest_child_stop_budget", "guest_child_stop_panic", "guest_child_stop_exit", "guest_child_stop_sigint", "guest_child_stop_sigkill", "guest_child_stop_signal"} {
		if !datapublish.GuestStageFailureCode(code) || !nativeInitialStageCode(code) {
			t.Fatal("initial publisher journal lost fixed guest stop category", code)
		}
	}
	for _, code := range []string{"PRIVATE_NATIVE_ERROR", "guest_child_stop_secret", "stage_publication_record"} {
		if nativeInitialStageCode(code) {
			t.Fatal("unrelated/raw code entered initial publisher journal", code)
		}
	}
}
