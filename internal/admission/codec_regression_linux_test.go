//go:build linux

package admission

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestOversizedPendingLeavesAdmissionOpen(t *testing.T) {
	d, path := fixture(t)
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustClose(t, root) })
	before, err := root.ReadFile("state.json")
	if err != nil {
		t.Fatal(err)
	}
	pending := pendingFixture()
	pending.Since = "2026-11-06T12:00:00." + strings.Repeat("0", MaxStateBytes) + "Z"
	drain, err := d.CloseAdmission(t.Context(), pending)
	if drain != nil {
		t.Cleanup(func() { mustClose(t, drain) })
	}
	if err == nil || drain != nil {
		t.Error("oversized pending acquired a drain")
	}
	after, err := root.ReadFile("state.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid pending replaced the durable state")
	}
	state, err := d.Read(t.Context())
	if err != nil || !state.Open {
		t.Fatal("original open state became unreadable", err)
	}
}
