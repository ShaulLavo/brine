package restore

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type latestCLI struct {
	t     *testing.T
	data  []byte
	calls int
}

func (c *latestCLI) Execute(_ context.Context, command Command) (CommandResult, error) {
	c.calls++
	if command.Args[0] == "version" {
		return CommandResult{Stdout: []byte(LitestreamVersion)}, nil
	}
	output := ""
	for i, arg := range command.Args {
		if arg == "-o" {
			output = command.Args[i+1]
		}
	}
	if strings.Contains(strings.Join(command.Args, " "), "-dry-run") {
		raw, _ := json.Marshal(map[string]string{"target_path": output, "replica": "s3", "max_txid": "0000000000000007"})
		return CommandResult{Stdout: raw}, nil
	}
	if err := os.WriteFile(output, c.data, 0600); err != nil {
		c.t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"db_path": output, "replica": "s3", "txid": "0000000000000007", "integrity_check": "full"})
	return CommandResult{Stdout: raw}, nil
}
func TestLatestRestoreResolvesAnExactRemotePoint(t *testing.T) {
	e, req, _ := setup(t, fixture(t, fixtureSQL))
	cli := &latestCLI{t: t, data: fixture(t, fixtureSQL)}
	e.CLI = cli
	req.Source = RestoreSource{Kind: LitestreamLTX, LTX: &LTXSource{BindingID: "b1", Epoch: "e1"}}
	req.Latest = true
	receipt, err := e.Test(context.Background(), req)
	if err != nil || receipt.RecoveredTXID != 7 || receipt.Source.LTX.TXID != 7 || cli.calls != 3 {
		t.Fatal("latest source was not resolved exactly", err, cli.calls)
	}
}
