//go:build linux

package host

import (
	"context"
	"testing"
)

func TestNativeRestoredSourceRequiresOwnedOriginalAndDownloadedRoot(t *testing.T) {
	if (*nativeRestoredSource)(nil).unchanged(context.Background()) == nil {
		t.Fatal("missing restored source supplied mutation authority")
	}
	if (&nativeRestoredSource{}).unchanged(context.Background()) == nil {
		t.Fatal("syntactic restore context replaced owned source/download provenance")
	}
}
