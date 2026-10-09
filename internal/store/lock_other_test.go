//go:build !linux

package store

import (
	"context"
	"errors"
	"testing"
)

func TestControlStoreRefusesNonLinuxHost(t *testing.T) {
	if s, err := Open(t.TempDir()); !errors.Is(err, errHostOnly) || s != nil {
		t.Fatalf("host store refusal: %v", err)
	}
	var s Store
	if lock, err := s.AcquireHostLock(context.Background()); !errors.Is(err, errHostOnly) || lock != nil {
		t.Fatalf("host lock refusal: %v", err)
	}
}
