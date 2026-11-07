//go:build linux

package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/transport"
)

type updateOperatorFake struct {
	base          *operatorFake
	plan          UpdatePlan
	actions       []string
	failInventory bool
}

func (f *updateOperatorFake) RunInput(ctx context.Context, c localexec.Command) (localexec.Output, error) {
	var request HostRequest
	if len(c.Stdin) > 0 && json.Unmarshal(c.Stdin, &request) == nil && isUpdateAction(request.Action) {
		f.actions = append(f.actions, request.Action)
		if request.Action == "update-plan" {
			raw, err := json.Marshal(f.plan)
			return localexec.Output{Stdout: raw}, err
		}
		if request.Confirmed != f.plan.Digest {
			return localexec.Output{}, errors.New("digest not confirmed")
		}
		raw, err := json.Marshal(UpdateResult{ID: "fixture", Committed: true, Retained: "fixture recovery bytes"})
		return localexec.Output{Stdout: raw}, err
	}
	if f.failInventory && bytes.Contains(c.Stdin, []byte(`"op":"inventory"`)) {
		return localexec.Output{}, errors.New("used host inventory unavailable")
	}
	return f.base.RunInput(ctx, c)
}
func TestUpdateClientReverifiesUsedHostWithoutFinish(t *testing.T) {
	options, base := fakeClientOptions(t)
	if err := privateDir(options.ConfigDir); err != nil {
		t.Fatal(err)
	}
	options.Update = true
	target := transport.Target{Name: options.Name, Destination: "brine@fixture", PinnedHostKey: base.facts.HostKey, IdentityPath: filepath.Join(filepath.Dir(options.DeployKeyPath), "deploy")}
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := clientFile(filepath.Join(options.ConfigDir, "fixture.json"), raw); err != nil {
		t.Fatal(err)
	}
	runner := &updateOperatorFake{base: base, plan: UpdatePlan{Digest: "reviewed", CandidateVersion: "candidate", CandidateSHA256: "hash", Quiescence: "store"}}
	client := Client{Runner: runner, Input: bytes.NewBufferString("fixture\n"), Output: &bytes.Buffer{}, Terminal: true}
	if err := client.runUpdate(context.Background(), options, make([]byte, 32), []string{"fixture", "/tmp/fake/brine host enrollment"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.actions) != 2 || runner.actions[0] != "update-plan" || runner.actions[1] != "update-apply" || base.restrictions != 1 {
		t.Fatal("update did not restrict itself to review, apply and deploy-key checks", runner.actions)
	}
	for _, command := range base.commands {
		if bytes.Contains(command.Stdin, []byte(`"action":"verify"`)) {
			t.Fatal("used-host update called empty-runtime --finish")
		}
	}
}
func TestUpdateOptionsDoNotMixActions(t *testing.T) {
	for _, options := range []Options{{Update: true, Finish: true}, {Update: true, Undo: true}, {Update: true, UpdateAbort: true}, {Quiesce: "invalid"}, {Quiesce: "runner"}} {
		if err := validateUpdateOptions(options); err == nil {
			t.Fatal("invalid options accepted", options)
		}
	}
}
