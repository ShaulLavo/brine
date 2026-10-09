package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestResolveCLIUsesClosedAcceptanceProtocol(t *testing.T) {
	var out bytes.Buffer
	calls := 0
	deps := Dependencies{Context: context.Background(), Stdin: bytes.NewReader(nil), Stdout: &out, Stderr: io.Discard}
	deps.LoadOperationTarget = func(_ string, name string) (transport.Target, error) { return transport.Target{Name: name}, nil }
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls++
		var args dispatch.ResolveArgs
		if request.Op != "resolve" || json.Unmarshal(request.Args, &args) != nil || args.OperationID != "source-operation" || args.IdempotencyKey != "stable-key" {
			t.Fatalf("request %+v", request)
		}
		if _, err := dispatch.EncodeRequest(request); err != nil {
			t.Fatal(err)
		}
		return result.Success("brine resolve", jobs.Accepted{Status: "accepted", OperationID: "resolution-job"}), nil
	})
	if err := Execute(deps, []string{"resolve", "source-operation", "--target", "fixture", "--idempotency-key", "stable-key", "--json"}); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	if !strings.Contains(out.String(), "resolution-job") {
		t.Fatal(out.String())
	}
	calls = 0
	if err := Execute(deps, []string{"resolve", "../untrusted", "--target", "fixture"}); err == nil || calls != 0 {
		t.Fatal("invalid source reached transport", err, calls)
	}
}
