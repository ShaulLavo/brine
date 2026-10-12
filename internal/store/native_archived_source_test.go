//go:build linux

package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestNativeArchivedSourceRefusesDecodedOrUnownedReadAuthority(t *testing.T) {
	s := openTest(t)
	if _, err := json.Marshal(&NativeArchivedSource{}); err == nil {
		t.Fatal("archive source became serialized read authority")
	}
	if _, err := s.NativeArchivedSource(context.Background(), data.ArchiveID("0123456789abcdef0123456789abcdef"), data.ReplicaBindingID("abcdef0123456789abcdef0123456789"), nil, nil); err == nil {
		t.Fatal("missing process locks admitted archive repository read")
	}
	if _, err := checkNativeArchivedSourceTx(context.Background(), nil, nil); err == nil {
		t.Fatal("missing archive identity admitted read")
	}
}
