package restic

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestAuthenticatedWorkingCopyKeepsDownloadAndRefusesChangedMembers(t *testing.T) {
	ctx := context.Background()
	source, work := testTree(t)
	bundle, err := Seal(ctx, source, work, testIdentity(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bundle.Close() }()
	parent, err := os.OpenRoot(work)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	download, err := extractEnvelope(ctx, bundle.path, parent, "download", bundle.Manifest(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = download.Close() }()
	working, err := extractEnvelope(ctx, bundle.path, parent, "working", bundle.Manifest(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = working.Close() }()
	identity, err := download.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	restored := AuthenticatedRestore{root: download, parent: parent, name: "download", identity: identity, manifest: bundle.Manifest(), limits: DefaultLimits(), verifiedAt: time.Now().UTC()}
	if !restored.MatchesWorkingCopy(ctx, parent, "working", working) || restored.MatchesWorkingCopy(ctx, parent, "working", download) {
		t.Fatal("copy must match full envelope and have a separate directory identity")
	}
	if err = working.WriteFile("state", []byte("published private state"), 0600); err != nil {
		t.Fatal(err)
	}
	if restored.MatchesWorkingCopy(ctx, parent, "working", working) || !restored.Matches(ctx, parent, "download", restored.Point(), restored.Manifest()) {
		t.Fatal("changed working state accepted, or pristine download was changed")
	}
	if err = working.WriteFile("state", []byte("confirmed fixture state"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = download.WriteFile("state", []byte("changed original"), 0600); err != nil {
		t.Fatal(err)
	}
	if restored.MatchesWorkingCopy(ctx, parent, "working", working) {
		t.Fatal("working copy admitted after original authenticated download changed")
	}
}
