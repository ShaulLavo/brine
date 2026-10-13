package transport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

type reviewBudgetRunner struct {
	deadline       time.Time
	calls          int
	parentDeadline time.Time
}

func (r *reviewBudgetRunner) RunInput(ctx context.Context, command localexec.Command) (localexec.Output, error) {
	r.calls++
	r.deadline, _ = ctx.Deadline()
	request, err := dispatch.DecodeRequest(command.Stdin)
	if err != nil {
		return localexec.Output{}, err
	}
	if r.parentDeadline.IsZero() {
		want := dispatch.CallTimeout(request.Op)
		if time.Until(r.deadline) < want-time.Second || time.Until(r.deadline) > want {
			return localexec.Output{}, errors.New("client and server review budgets differ")
		}
	} else if !r.deadline.Equal(r.parentDeadline) {
		return localexec.Output{}, errors.New("parent deadline was extended")
	}
	view := datapublish.ReviewView{PlanID: "sha256:" + strings.Repeat("a", 64), App: "game", PointID: strings.Repeat("a", 32), SourceKind: datapublish.CreateSource, WasmSHA256: strings.Repeat("b", 64), ProgramWireHash: strings.Repeat("c", 64), SchemaSHA256: strings.Repeat("d", 64), Requirements: []spacetimedb.SchemaRequirement{spacetimedb.ReviewGuestCode, spacetimedb.VerifyNewTableData}, Recovery: datapublish.NoPriorReleaseReview(), ExpiresAt: time.Now().UTC().Add(time.Minute)}
	raw, err := json.Marshal(result.Success("brine host module_publish_plan", view))
	return localexec.Output{Stdout: raw}, err
}

func TestTransportReviewBudgetKeepsParentLimitAndNeverRetries(t *testing.T) {
	input, _ := json.Marshal(dispatch.ModulePlanArgs{App: "game", PreparedPointID: strings.Repeat("a", 32), TargetDeploymentPlanID: "sha256:" + strings.Repeat("b", 64), Wasm: []byte{0, 'a', 's', 'm', 1, 0, 0, 0}, RecoveryReleaseIDs: []string{}})
	for _, shortParent := range []bool{false, true} {
		parent := context.Background()
		cancel := func() {}
		runner := &reviewBudgetRunner{}
		if shortParent {
			parent, cancel = context.WithTimeout(parent, time.Second)
			runner.parentDeadline, _ = parent.Deadline()
		}
		known := t.TempDir()
		if err := os.Chmod(known, 0700); err != nil { //nolint:gosec // Private test known-host pin directory.
			t.Fatal(err)
		}
		client := Client{KnownHostsDir: known, Runner: runner, LookPath: func(string) (string, error) { return "/fixture/ssh", nil }}
		response, err := client.Call(parent, validTarget(), dispatch.Request{SchemaVersion: 1, Op: "module_publish_plan", RequestID: "review-budget", Args: input})
		cancel()
		if err != nil || !response.OK || runner.calls != 1 || runner.deadline.IsZero() {
			t.Fatal("review transport did not make one bounded call", err)
		}
	}
}
