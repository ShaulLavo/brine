//go:build linux

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/nativecapture"
)

func publicationCheckpointFixture(t *testing.T) (*os.Root, nativecapture.Checkpoint) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	config := []byte("fixed-private-configuration")
	if err := os.WriteFile(filepath.Join(directory, "config.toml"), config, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	hash := sha256.Sum256(config)
	configuration := hex.EncodeToString(hash[:])
	digest := strings.Repeat("1", 64)
	checkpoint, err := nativecapture.NewCheckpoint(nativecapture.CheckpointRecord{Format: nativecapture.Format, Scope: nativecapture.CheckpointScope{BindingID: strings.Repeat("1", 32), EpochID: strings.Repeat("2", 32), ReplicaID: 7}, GenerationID: strings.Repeat("3", 32), DatabaseIdentity: digest, PublisherIdentity: digest, Names: []string{"fixture"}, RuntimeModuleHash: digest, ArtifactSHA256: digest, SchemaVersion: "stdb-v10-semantic-json-v1", SchemaSHA256: digest, ConfigurationSHA256: configuration,
		SigningRecovery: nativecapture.SigningRecovery{Kind: "external_immutable", Reference: "fixture-signing", Version: 1, SHA256: digest}, Members: []nativecapture.CheckpointMember{{Path: "config.toml", Bytes: uint64(len(config)), SHA256: configuration}}}, nativecapture.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return root, checkpoint
}

func TestPublicationCheckpointReadsActualRetainedMembers(t *testing.T) {
	root, checkpoint := publicationCheckpointFixture(t)
	ctx := context.Background()
	if err := checkPublicationCheckpointMembers(ctx, root, checkpoint); err != nil {
		t.Fatal("unchanged actual file refused", err)
	}
	if err := root.WriteFile("config.toml", []byte("different-private-configura"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkPublicationCheckpointMembers(ctx, root, checkpoint); !errors.Is(err, ErrConflict) {
		t.Fatal("changed checkpoint member admitted", err)
	}
}

func TestPublicationCheckpointRefusesLinksAndUnheldRoots(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root, checkpoint := publicationCheckpointFixture(t)
			if err := root.Rename("config.toml", "same-bytes"); err != nil {
				t.Fatal(err)
			}
			var err error
			if kind == "symlink" {
				err = root.Symlink("same-bytes", "config.toml")
			} else {
				err = root.Link("same-bytes", "config.toml")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = checkPublicationCheckpointMembers(context.Background(), root, checkpoint); !errors.Is(err, ErrConflict) {
				t.Fatal("linked member admitted", err)
			}
		})
	}
	if err := checkPublicationCheckpointMembers(context.Background(), nil, nativecapture.Checkpoint{}); !errors.Is(err, ErrConflict) {
		t.Fatal("zero checkpoint manufactured rooted authority", err)
	}
}
