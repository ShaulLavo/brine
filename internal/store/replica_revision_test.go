//go:build linux

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func pendingRevisionFixture(t *testing.T) (*Store, data.ReplicaRevision) {
	t.Helper()
	state, before := committedDataFixture(t)
	after := before
	after.ConfigContent = "dbs: []\n# next admitted cadence\n"
	sum := sha256.Sum256([]byte(after.ConfigContent))
	after.ConfigSHA256 = hex.EncodeToString(sum[:])
	after.ConfigFile = filepath.Join(state.dir, "replication", string(after.BindingID), "configs", after.ConfigSHA256+".yml")
	after.UnitSHA256 = strings.Repeat("d", 64)
	return state, data.ReplicaRevision{ID: "sha256:" + strings.Repeat("f", 64), App: "example", Before: before, After: after, Stage: data.RevisionStored}
}

func TestReplicaRevisionDurableStagesFenceAndExclusion(t *testing.T) {
	ctx := context.Background()
	state, record := pendingRevisionFixture(t)
	fence, err := state.HoldDataFence(ctx, record.Before.DatabaseID, "fence-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = state.WriteReplicaRevision(ctx, "", record); err != nil {
		t.Fatal("storage under fence refused", err)
	}
	if err = state.WriteReplicaRevision(ctx, "", record); err != nil {
		t.Fatal("identical storage not idempotent", err)
	}
	other := record
	other.ID = "sha256:" + strings.Repeat("e", 64)
	if err = state.WriteReplicaRevision(ctx, "", other); err == nil {
		t.Fatal("overlapping pending revision admitted")
	}
	record.Stage = data.RotationPrepared
	if err = state.WriteReplicaRevision(ctx, data.RevisionStored, record); !errors.Is(err, ErrConflict) {
		t.Fatal("held fence admitted activation", err)
	}
	if err = state.ReleaseDataFence(ctx, fence.ID, "fence-owner"); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []data.RotationStage{data.RotationPrepared, data.RotationStopIssued, data.RotationStopped, data.RotationCommitted, data.RotationStartIssued, data.RotationActive} {
		stored, err := state.ReadReplicaRevision(ctx, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		previous := stored.Stage
		record.Stage = stage
		if stage == data.RotationCommitted {
			if err = state.WriteReplicaRevision(ctx, previous, record); !errors.Is(err, ErrConflict) {
				t.Fatal("uncommitted binding advanced cursor", err)
			}
			if err = state.CommitReplicaBinding(ctx, record.After); err != nil {
				t.Fatal(err)
			}
		}
		if err = state.WriteReplicaRevision(ctx, previous, record); err != nil {
			t.Fatal(stage, err)
		}
	}
	readonly, err := OpenReadOnly(ctx, state.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := readonly.Close(); err != nil {
			t.Error(err)
		}
	})
	persisted, err := readonly.ReadReplicaRevision(ctx, record.ID)
	if err != nil || persisted != record {
		t.Fatal("cursor not durable", err)
	}
}

func TestReplicaRevisionAndCredentialRotationExcludeEachOther(t *testing.T) {
	for _, first := range []string{"cadence", "credentials"} {
		t.Run(first, func(t *testing.T) {
			ctx := context.Background()
			state, revision := pendingRevisionFixture(t)
			after := revision.Before
			after.CredentialVersion++
			after.CredentialFile = filepath.Join(state.dir, "credentials/s3/primary/v2.env")
			after.UnitSHA256 = strings.Repeat("e", 64)
			rotation := data.CredentialRotation{PlanID: "sha256:" + strings.Repeat("a", 64), App: revision.App, Before: revision.Before, After: after, Stage: data.RotationPrepared}
			if first == "cadence" {
				if err := state.WriteReplicaRevision(ctx, "", revision); err != nil {
					t.Fatal(err)
				}
				if err := state.WriteCredentialRotation(ctx, "", rotation); !errors.Is(err, ErrConflict) {
					t.Fatal("rotation raced pending cadence", err)
				}
			} else {
				if err := state.WriteCredentialRotation(ctx, "", rotation); err != nil {
					t.Fatal(err)
				}
				if err := state.WriteReplicaRevision(ctx, "", revision); !errors.Is(err, ErrConflict) {
					t.Fatal("cadence raced pending rotation", err)
				}
			}
		})
	}
}

func TestReplicaRevisionRefusesIdentityPathsAndSkippedStages(t *testing.T) {
	ctx := context.Background()
	state, record := pendingRevisionFixture(t)
	for _, change := range []struct {
		name   string
		mutate func(*data.ReplicaRevision)
	}{
		{"epoch", func(r *data.ReplicaRevision) { r.After.EpochID = data.ReplicaEpochID(strings.Repeat("1", 32)) }},
		{"destination", func(r *data.ReplicaRevision) { r.After.Destination.Bucket = "other" }},
		{"credentials", func(r *data.ReplicaRevision) { r.After.CredentialVersion++ }},
		{"mutable_path", func(r *data.ReplicaRevision) {
			r.After.ConfigFile = filepath.Join(state.dir, "replication", string(r.After.BindingID), "litestream.yml")
		}},
		{"hash_drift", func(r *data.ReplicaRevision) { r.After.ConfigContent += "# drift" }},
		{"skip_publish", func(r *data.ReplicaRevision) { r.Stage = data.RotationStopped }},
	} {
		t.Run(change.name, func(t *testing.T) {
			candidate := record
			change.mutate(&candidate)
			if err := state.WriteReplicaRevision(ctx, "", candidate); err == nil {
				t.Fatal("unsafe revision accepted")
			}
		})
	}
}

func TestReplicaRevisionMigrationFromInitializationSchemaEight(t *testing.T) {
	ctx := context.Background()
	state, revision := pendingRevisionFixture(t)
	for _, statement := range []string{"DROP TABLE data_replica_revisions", "UPDATE schema_version SET version=8"} {
		if _, err := state.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := state.db.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil || version != 9 {
		t.Fatal("replica revision migration missing", version, err)
	}
	if err := state.WriteReplicaRevision(ctx, "", revision); err != nil {
		t.Fatal("v8 binding not preserved", err)
	}
	for _, table := range []string{"operation_outcomes", "data_init_plans"} {
		var count int
		if err := state.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			t.Fatal("earlier migration table lost", table, err)
		}
	}
}
