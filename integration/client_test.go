//go:build linux && pi_integration

package integration

import (
	"context"
	"encoding/json"
	"flag"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/transport"
)

var targetConfig = flag.String("fixture-target", "", "private client target file for explicitly authorized read-only verification")

func TestRestrictedClientAfterCleanup(t *testing.T) {
	if *targetConfig == "" {
		t.Skip("requires authorized private target configuration")
	}
	config := must(transport.LoadTarget(*targetConfig))
	client := transport.Client{KnownHostsDir: filepath.Dir(*targetConfig)}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request := dispatch.Request{SchemaVersion: 1, Op: "ping", RequestID: "p02-fixture-final", Args: json.RawMessage(`{}`)}
	reply := must(client.Call(ctx, config, request))
	if !reply.OK {
		t.Fatal("enrollment ping failed")
	}
	reply = must(client.VerifyRestriction(ctx, config, request))
	if !reply.OK {
		t.Fatal("restricted key bypassed dispatcher")
	}
	request.Op = "inventory"
	reply = must(client.Call(ctx, config, request))
	b := must(json.Marshal(reply.Data))
	snapshot := must(target.Decode(b))
	if snapshot.Apps.Value == nil || len(*snapshot.Apps.Value) != 0 {
		t.Fatal("restricted inventory not empty after cleanup")
	}
	if snapshot.CaddyConfig.Value == nil || len(snapshot.CaddyConfig.Value.Files) != 0 {
		t.Fatal("restricted inventory retains fixture route")
	}
	t.Log("pinned transport ping and ignored shell-command probe pass; restricted inventory has an empty app set")
}
