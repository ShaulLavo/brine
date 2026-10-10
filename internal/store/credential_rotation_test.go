//go:build linux

package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestCredentialRotationDurableCursorAndBindingExclusion(t *testing.T) {
	ctx := context.Background()
	state, before := committedDataFixture(t)
	after := before
	after.CredentialVersion = 2
	after.CredentialFile = filepath.Join(state.dir, "credentials/s3/primary/v2.env")
	after.UnitSHA256 = strings.Repeat("d", 64)
	record := data.CredentialRotation{PlanID: "sha256:" + strings.Repeat("e", 64), App: "example", Before: before, After: after, Stage: data.RotationPrepared}
	if err := state.WriteCredentialRotation(ctx, "", record); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteCredentialRotation(ctx, "", record); err != nil {
		t.Fatal("exact journal retry failed", err)
	}
	other := record
	other.PlanID = "sha256:" + strings.Repeat("f", 64)
	if err := state.WriteCredentialRotation(ctx, "", other); err == nil {
		t.Fatal("overlapping rotation admitted")
	}
	readonly, err := OpenReadOnly(ctx, state.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readonly.Close() }()
	got, err := readonly.ReadCredentialRotation(ctx, record.PlanID)
	if err != nil || got != record {
		t.Fatal("rotation cursor not durable", err)
	}
	record.Stage = data.RotationCommitted
	if err := state.WriteCredentialRotation(ctx, data.RotationStopped, record); !errors.Is(err, ErrConflict) {
		t.Fatal("skipped rotation stage accepted", err)
	}
	record.Stage = data.RotationStopIssued
	if err := state.WriteCredentialRotation(ctx, data.RotationPrepared, record); err != nil {
		t.Fatal(err)
	}
	record.Stage = data.RotationStopped
	if err := state.WriteCredentialRotation(ctx, data.RotationStopIssued, record); err != nil {
		t.Fatal(err)
	}
	if err := state.CommitReplicaBinding(ctx, after); err != nil {
		t.Fatal(err)
	}
	record.Stage = data.RotationCommitted
	if err := state.WriteCredentialRotation(ctx, data.RotationStopped, record); err != nil {
		t.Fatal("committed binding did not reconcile", err)
	}
	record.Stage = data.RotationStartIssued
	if err := state.WriteCredentialRotation(ctx, data.RotationCommitted, record); err != nil {
		t.Fatal(err)
	}
	record.Stage = data.RotationActive
	if err := state.WriteCredentialRotation(ctx, data.RotationStartIssued, record); err != nil {
		t.Fatal(err)
	}
	// A proved-running replica with rejected credentials must permit a fresh
	// corrective rotation without falsely marking remote access verified.
	other = record
	other.PlanID = "sha256:" + strings.Repeat("f", 64)
	other.Before = after
	other.After = after
	other.After.CredentialVersion = 3
	other.After.CredentialFile = filepath.Join(state.dir, "credentials/s3/primary/v3.env")
	other.After.UnitSHA256 = strings.Repeat("a", 64)
	other.Stage = data.RotationPrepared
	if err := state.WriteCredentialRotation(ctx, "", other); err != nil {
		t.Fatal("active cursor blocked corrective rotation", err)
	}
	record.Stage = data.RotationVerified
	if err := state.WriteCredentialRotation(ctx, data.RotationActive, record); err != nil {
		t.Fatal(err)
	}
	record.After.EpochID = data.ReplicaEpochID(strings.Repeat("1", 32))
	if err := state.WriteCredentialRotation(ctx, data.RotationStartIssued, record); !errors.Is(err, ErrInvalid) {
		t.Fatal("rotation changed epoch", err)
	}
}
