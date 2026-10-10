package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestRestoreCLISelectorsAndSecretFreeReceipt(t *testing.T) {
	for _, mode := range []string{"", "--json", "--jsonl"} {
		t.Run(mode, func(t *testing.T) {
			var out, stderr bytes.Buffer
			deps := testDependencies(t, &out, &stderr)
			deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
			deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
				var args dispatch.RestoreTestArgs
				if request.Op != "restore_test" || json.Unmarshal(request.Args, &args) != nil || args.App != "hello" || args.Database != "audit" || args.TXID != "7" {
					t.Fatal("restore selector lost")
				}
				receipt := restore.Receipt{OperationID: "op1", Source: restore.RestoreSource{Kind: restore.LitestreamLTX, LTX: &restore.LTXSource{Recoverability: true, BindingID: "b1", Epoch: "e1", TXID: 7}}, ToolVersion: restore.LitestreamVersion, RequestedTXID: 7, RecoveredTXID: 7, ObservedAt: time.Now().UTC(), Schema: restore.SchemaObservation{State: restore.VerifiedSchema, Marker: "v1", CatalogSHA256: strings.Repeat("a", 64)}, LossWindow: restore.LossWindow{State: restore.LossUnknown, Reason: "No independent last-commit coverage proof; asynchronous replication may lose recent writes."}, IntegrityCheck: "passed", ForeignKeyCheck: "passed", InvariantCheck: "passed", PositionEvidence: "remote_dry_run_and_restore", DatabasePath: "PLANTED_PRIVATE_PATH"}
				return result.Success("brine host restore_test", receipt), nil
			})
			args := []string{"restore", "test", "hello", "--database", "audit", "--txid", "7", "--target", "fixture"}
			if mode != "" {
				args = append(args, mode)
			}
			if err := Execute(deps, args); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String()+stderr.String(), "PLANTED") || !strings.Contains(out.String(), "unknown") {
				t.Fatal("receipt leaked path or hid unknown loss")
			}
		})
	}
}
func TestRestoreCLIRejectsSelectorsBeforeIO(t *testing.T) {
	for _, extra := range [][]string{{"--txid", "0"}, {"--txid", "7", "--point", "p1"}, {"--database", "../live"}, {"--point", "../live"}} {
		var out, stderr bytes.Buffer
		deps := testDependencies(t, &out, &stderr)
		deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
			t.Fatal("invalid restore request did IO")
			return transport.Target{}, nil
		}
		args := append([]string{"restore", "test", "hello", "--target", "fixture"}, extra...)
		if err := Execute(deps, args); err == nil {
			t.Fatal("invalid restore selector accepted")
		}
	}
}
