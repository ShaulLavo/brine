//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/store"
)

func publicationPreparationFixture(t *testing.T) (*store.EnginePreparation, data.ReplicaBindingID) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	state, err := store.OpenContext(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	binding, _ := engineSourceFixture(t)
	destination := data.Destination{Reference: "backup", Endpoint: "https://storage.example", Region: "region-1", Bucket: "backups", BasePrefix: "brine", CredentialRef: "backup"}
	epoch := data.ReplicaEpochID(strings.Repeat("d", 32))
	prefix, err := data.RemotePrefix(destination.BasePrefix, binding.IncarnationID, binding.DatabaseID, epoch)
	if err != nil {
		t.Fatal(err)
	}
	request := store.EngineReservation{App: "game", PolicyHash: "sha256:" + strings.Repeat("a", 64), Database: binding, Archive: data.ArchiveBinding{BindingID: binding.BindingID, DatabaseID: binding.DatabaseID, Destination: destination, EpochID: epoch, RemotePrefix: prefix}}
	if _, err = state.ReserveEngine(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err = data.PrepareEngineLocks(ctx, root, binding.BindingID); err != nil {
		t.Fatal(err)
	}
	fence, err := state.HoldDataFence(ctx, binding.DatabaseID, "prepare-module")
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := state.AcquireEnginePreparation(ctx, binding.BindingID, fence.ID, "prepare-module")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = preparation.Close() })
	return preparation, binding.BindingID
}

func publicationArtifactFixture(t *testing.T) datapublish.Artifact {
	t.Helper()
	raw, err := os.ReadFile("../spec/testdata/managed-module.wasm")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := datapublish.NewArtifact(raw)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func TestPublicationStagingRequiresActualOwnedPreparation(t *testing.T) {
	artifact := publicationArtifactFixture(t)
	if _, err := stagePublicationArtifact(context.Background(), nil, data.ReplicaBindingID(strings.Repeat("c", 32)), artifact); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("caller-created root replaced protected preparation", err)
	}
	preparation, binding := publicationPreparationFixture(t)
	if _, err := stagePublicationArtifact(context.Background(), preparation, data.ReplicaBindingID(strings.Repeat("e", 32)), artifact); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("cross-binding preparation staged bytes", err)
	}
	staged, err := stagePublicationArtifact(context.Background(), preparation, binding, artifact)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = staged.discard() })
	if err = staged.unchanged(context.Background()); err != nil {
		t.Fatal("fresh immutable artifact refused", err)
	}
	path := staged.path()
	if path == "" || err != nil {
		t.Fatal("owned artifact path missing")
	}
	if err = preparation.Close(); err != nil {
		t.Fatal(err)
	}
	if staged.path() != "" || !errors.Is(staged.unchanged(context.Background()), datapublish.ErrRecord) {
		t.Fatal("released preparation retained extraction authority")
	}
	if err = staged.discard(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("owned artifact was not removed", err)
	}
}

func TestPublicationStagingRejectsContentAndInodeSubstitution(t *testing.T) {
	for _, attack := range []string{"bytes", "symlink", "replacement", "hardlink"} {
		t.Run(attack, func(t *testing.T) { testPublicationArtifactSubstitution(t, attack) })
	}
}

func testPublicationArtifactSubstitution(t *testing.T, attack string) {
	t.Helper()
	preparation, binding := publicationPreparationFixture(t)
	staged, err := stagePublicationArtifact(context.Background(), preparation, binding, publicationArtifactFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = staged.discard() })
	path := staged.path()
	switch attack {
	case "bytes":
		file, err := os.OpenFile(path, os.O_WRONLY, 0600) //nolint:gosec // Test mutates the exact owned artifact path to verify refusal.
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.WriteAt([]byte{'x'}, 0)
		if err = errors.Join(writeErr, file.Close()); err != nil {
			t.Fatal(err)
		}
	case "symlink", "replacement":
		if err = os.Rename(path, path+".original"); err != nil {
			t.Fatal(err)
		}
		if attack == "symlink" {
			err = os.Symlink(path+".original", path)
		} else {
			err = os.WriteFile(path, staged.artifact.Bytes(), 0600)
		}
	case "hardlink":
		err = os.Link(path, path+".alias")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = staged.unchanged(context.Background()); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("substituted reviewed artifact admitted", attack, err)
	}
	if attack == "bytes" {
		return
	}
	if err = staged.discard(); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("foreign replacement acquired cleanup authority", err)
	}
	if _, err = os.Lstat(path); err != nil {
		t.Fatal("cleanup removed unrelated replacement", err)
	}
}
