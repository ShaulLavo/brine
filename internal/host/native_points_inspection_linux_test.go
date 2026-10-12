//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestNativePointInspectionDoesNotOpenMutationRuntimeOrCreateEvidence(t *testing.T) {
	opens := 0
	factory := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) {
		opens++
		return nil, result.New(result.InternalError, nil)
	}, func(context.Context) (dispatch.Inventory, error) {
		t.Fatal("inspection requested live inventory")
		return nil, nil
	})
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	state, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	factory.diagnosticStateDir = root
	server, err := factory.Build(context.Background(), "native_point")
	if err != nil || opens != 0 || server.NativePoints == nil || server.Planner != nil || server.Config != nil || server.DataInitialization != nil || server.BackupCredentials != nil {
		t.Fatal("point inspection acquired mutation capabilities", err)
	}
	for _, kind := range []string{"verified", "prepared"} {
		_, err := server.NativePoints.InspectNativePoint(context.Background(), dispatch.NativePointArgs{Kind: kind, PointID: strings.Repeat("a", 32)})
		if result.Classify(err).Code() != result.NativePointNotFound {
			t.Fatal("missing point became evidence", err)
		}
	}
	read, err := store.OpenReadOnly(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	if generation, err := read.Generation(context.Background()); err != nil || generation != 0 {
		t.Fatal("reference read changed control generation", err)
	}
}
