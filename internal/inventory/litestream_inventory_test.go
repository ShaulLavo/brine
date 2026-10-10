package inventory

import (
	"context"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func TestGeneralInventoryUsesProtectedLitestreamObservation(t *testing.T) {
	runner := fakeRunner{"uname -m": "aarch64", "litestream version": "0.5.99", LitestreamPath + " version": "0.5.17\n"}
	hashes := 0
	snapshot, err := (Collector{FS: baseFixture(), Runner: runner, IdentityKey: []byte("fixture"), LitestreamHashExecutable: func(_ context.Context, path string) (string, error) {
		if path != LitestreamPath {
			t.Fatal("unprotected hash path")
		}
		hashes++
		return "sha256:" + strings.Repeat("a", 64), nil
	}}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	observed := snapshot.Versions.Litestream
	if observed.Status != target.KnownStatus || observed.Value == nil || *observed.Value != "0.5.17" || hashes != 2 {
		t.Fatal("general inventory did not use protected version and stable hash", observed, hashes)
	}
}
