package store

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/restic"
)

func TestNativeInheritedRepositoryRefusesARepeatedEpochBeforeFollowingMetadata(t *testing.T) {
	point := restic.TransportPoint{Scope: restic.Scope{EpochID: "0123456789abcdef0123456789abcdef"}}
	if _, err := readNativeRepositoryMaterialLineage(context.Background(), nil, point, map[string]bool{point.Scope.EpochID: true}); err == nil {
		t.Fatal("cyclic repository ancestry was followed")
	}
}
