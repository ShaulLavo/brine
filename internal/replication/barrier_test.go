package replication

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type barrierExec struct {
	result  localexec.Result
	err     error
	command localexec.Command
}

func (e *barrierExec) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	e.command = c
	return e.result, e.err
}
func TestBarrierRequiresExactRemoteWatermark(t *testing.T) {
	b := testBinding()
	e := &barrierExec{result: localexec.Result{Stdout: `{"db_path":"` + b.DBPath + `","txid":17,"replica_txid":18,"duration_ms":42}`}}
	receipt, err := SyncBarrier(context.Background(), e, b, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TXID != 17 || receipt.ReplicaTXID != 18 || receipt.EpochID != b.EpochID {
		t.Fatal("wrong barrier identity")
	}
	want := []string{"sync", "-wait", "-json", "-timeout", "30", "-socket", b.SocketPath, b.DBPath}
	if e.command.Path != Executable || !reflect.DeepEqual(e.command.Args, want) || !e.command.Mutation || e.command.Timeout != 30*time.Second {
		t.Fatalf("wrong barrier invocation %+v", e.command)
	}
}
func TestBarrierNeverAcceptsStatusOrUnknownResult(t *testing.T) {
	b := testBinding()
	for name, result := range map[string]localexec.Result{
		"status":         {Stdout: `{"path":"` + b.DBPath + `","txid":17}`},
		"behind":         {Stdout: `{"db_path":"` + b.DBPath + `","txid":17,"replica_txid":16,"duration_ms":1}`},
		"missing-remote": {Stdout: `{"db_path":"` + b.DBPath + `","txid":17,"duration_ms":1}`},
		"no-ltx":         {Stdout: `{"db_path":"` + b.DBPath + `","txid":0,"replica_txid":0,"duration_ms":1}`},
		"other-db":       {Stdout: `{"db_path":"/other/db","txid":17,"replica_txid":17,"duration_ms":1}`},
		"truncated":      {Stdout: `{}`, Truncated: true},
		"duplicate":      {Stdout: `{"db_path":"` + b.DBPath + `","txid":17,"txid":18,"replica_txid":18,"duration_ms":1}`},
		"failed-exit":    {Stdout: `{}`, ExitCode: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := SyncBarrier(context.Background(), &barrierExec{result: result}, b, time.Second); err == nil {
				t.Fatal("unknown durability accepted")
			}
		})
	}
	if _, err := SyncBarrier(context.Background(), &barrierExec{err: errors.New("timeout")}, b, time.Second); err == nil {
		t.Fatal("failed barrier accepted")
	}
}
