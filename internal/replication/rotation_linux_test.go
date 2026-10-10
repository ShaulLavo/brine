//go:build linux

package replication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

func TestRotationReplacesOnlyExpectedReplicaUnit(t *testing.T) {
	publisher, artifact := publisherFixture(t)
	ctx := context.Background()
	if err := publisher.Publish(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	lockBefore, err := os.Stat(artifact.LifetimeLock)
	if err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.Stat(artifact.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(artifact.Service)
	oldHash := hex.EncodeToString(sum[:])
	artifact.Service = append(artifact.Service, []byte("EnvironmentFile=/private/v2.env\n")...)
	if err := publisher.ReplaceService(ctx, artifact, oldHash); err != nil {
		t.Fatal(err)
	}
	if err := publisher.ReplaceService(ctx, artifact, oldHash); err != nil {
		t.Fatal("exact replacement retry failed", err)
	}
	lockAfter, err := os.Stat(artifact.LifetimeLock)
	if err != nil || !os.SameFile(lockBefore, lockAfter) {
		t.Fatal("rotation replaced lifetime lock")
	}
	configAfter, err := os.Stat(artifact.ConfigPath)
	if err != nil || !os.SameFile(configBefore, configAfter) {
		t.Fatal("rotation replaced config")
	}
	artifact.Service = append(artifact.Service, []byte("# unexpected third version\n")...)
	if err := publisher.ReplaceService(ctx, artifact, oldHash); err == nil {
		t.Fatal("wrong prior unit overwritten")
	}
}
