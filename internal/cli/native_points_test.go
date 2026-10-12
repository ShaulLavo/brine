package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestNativePointCLIInspectsExactReferenceWithoutGrantingEffects(t *testing.T) {
	for _, mode := range []string{"", "--json", "--jsonl"} {
		checkNativePointCLI(t, mode)
	}
}

func checkNativePointCLI(t *testing.T, mode string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	pointID := strings.Repeat("a", 32)
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		var input dispatch.NativePointArgs
		if request.Op != "native_point" || json.Unmarshal(request.Args, &input) != nil || input.PointID != pointID || input.Kind != "verified" {
			t.Fatal("CLI changed inspection into an effect or arbitrary selector")
		}
		now := time.Now().UTC()
		return result.Success("brine host native_point", nativecapture.PointInspection{Kind: input.Kind, PointID: pointID, PointSHA256: strings.Repeat("b", 64), OperationID: "capture-operation", BindingID: strings.Repeat("c", 32), EpochID: strings.Repeat("d", 32), CheckpointSHA256: strings.Repeat("e", 64), SourceSHA256: strings.Repeat("f", 64), ObservedAt: now, RetainUntil: now.Add(30 * 24 * time.Hour), Admission: "reference_only"}), nil
	})
	args := []string{"restore", "point", pointID, "--target", "fixture"}
	if mode != "" {
		args = append(args, mode)
	}
	if err := Execute(deps, args); err != nil {
		t.Fatal(err)
	}
	if (mode != "" && !strings.Contains(out.String(), "reference_only")) || (mode == "" && !strings.Contains(out.String(), "grants no effect authority")) || !strings.Contains(out.String(), pointID) || strings.Contains(out.String(), "accepted") {
		t.Fatal("reference inspection granted effects")
	}
}
