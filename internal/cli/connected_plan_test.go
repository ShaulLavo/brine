package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

type connectedPlanClient struct {
	t     *testing.T
	calls int
}

func (c *connectedPlanClient) Call(_ context.Context, _ transport.Target, r dispatch.Request) (result.Envelope, error) {
	c.calls++
	if r.Op != "plan" {
		c.t.Fatal("not connected planning")
	}
	var args dispatch.PlanArgs
	if json.Unmarshal(r.Args, &args) != nil || !strings.Contains(args.Spec, "container_port = 3000") {
		c.t.Fatal("spec not sent")
	}
	return result.Success("brine host plan", dispatch.Planned{PlanID: "sha256:" + strings.Repeat("a", 64), Kind: plan.Create, Conflicts: []plan.Diagnostic{}}), nil
}
func TestConnectedPlanClient(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "brine.toml")
	raw, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	client := &connectedPlanClient{t: t}
	deps := Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard, Version: "fixture", OperationClient: client, LoadOperationTarget: func(string, string) (transport.Target, error) { return transport.Target{}, nil }}
	if err := Execute(deps, []string{"plan", path, "--target", "fixture", "--json"}); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatal("plan did not call host")
	}
}
