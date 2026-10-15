//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/store"
)

type nativeIncomingMetadata interface {
	Metadata(context.Context) (store.NativeIncomingRepositoryMetadata, error)
}

type nativeIncomingTransport struct {
	ctx      context.Context
	cancel   context.CancelFunc
	metadata store.NativeIncomingRepositoryMetadata
}

func prepareNativeIncomingTransport(ctx context.Context, incoming nativeIncomingMetadata) (nativeIncomingTransport, error) {
	metadata, err := incoming.Metadata(ctx)
	if err != nil {
		return nativeIncomingTransport{}, err
	}
	return nativeIncomingTransport{ctx: ctx, cancel: func() {}, metadata: metadata}, nil
}
