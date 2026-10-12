package host

import (
	"context"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/restic"
)

func TestNativeTransportContextAlwaysEndsBeforeAdmissionExpiry(t *testing.T) {
	now := time.Now().UTC()
	for _, expiry := range []time.Time{now.Add(30 * time.Second), now.Add(10 * time.Minute), now.Add(time.Hour)} {
		ctx, cancel, err := nativeTransportContext(context.Background(), restic.Scope{ExpiresAt: expiry})
		if err != nil {
			t.Fatal(err)
		}
		deadline, exists := ctx.Deadline()
		cancel()
		if !exists || !deadline.Before(expiry) || deadline.After(now.Add(9*time.Minute+time.Second)) {
			t.Fatal("unbounded transport or outlived private credentials")
		}
	}
	existing, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx, stop, err := nativeTransportContext(existing, restic.Scope{ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	before, _ := existing.Deadline()
	after, _ := ctx.Deadline()
	if !after.Equal(before) {
		t.Fatal("transport widened caller budget")
	}
	if _, _, err := nativeTransportContext(context.Background(), restic.Scope{ExpiresAt: now}); err == nil {
		t.Fatal("expired material admitted transport")
	}
}
