//go:build linux

package host

import (
	"context"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/store"
)

type incomingMetadataFixture struct {
	metadata store.NativeIncomingRepositoryMetadata
}

func (f incomingMetadataFixture) Metadata(context.Context) (store.NativeIncomingRepositoryMetadata, error) {
	return f.metadata, nil
}

func TestIncomingRestoreTransportEndsBeforeItsRepositoryScope(t *testing.T) {
	parent, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	metadata := store.NativeIncomingRepositoryMetadata{}
	metadata.Material.Scope.ExpiresAt = time.Now().UTC().Add(10 * time.Minute)
	transport, err := prepareNativeIncomingTransport(parent, incomingMetadataFixture{metadata})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.cancel()
	deadline, exists := transport.ctx.Deadline()
	if !exists || !deadline.Before(metadata.Material.Scope.ExpiresAt) {
		t.Fatal("restore initialization outlives its repository admission")
	}
}
