package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/result"
)

type materialBudgetRunner struct {
	clientDeadline time.Time
	hostDeadline   time.Time
	calls          int
	delay          time.Duration
}

func (r *materialBudgetRunner) RunInput(ctx context.Context, command localexec.Command) (localexec.Output, error) {
	r.calls++
	r.clientDeadline, _ = ctx.Deadline()
	server := dispatch.NewServer("fixture", nil).WithJobs(nil, func(context.Context, dispatch.Class) error { return nil })
	server.NativeMaterial = r
	response, err := server.Handle(ctx, bytes.NewReader(command.Stdin))
	raw, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return localexec.Output{}, marshalErr
	}
	return localexec.Output{Stdout: raw}, err
}

func (r *materialBudgetRunner) DeliverMaterial(ctx context.Context, _ dispatch.NativeMaterialArgs) (nativematerial.Receipt, error) {
	r.hostDeadline, _ = ctx.Deadline()
	timer := time.NewTimer(r.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nativematerial.Receipt{}, ctx.Err()
	case <-timer.C:
	}
	version := func(ref string) data.EngineSecretVersion {
		return data.EngineSecretVersion{Reference: ref, Version: 1, SHA256: strings.Repeat("a", 64)}
	}
	secrets := data.EngineSecrets{Publisher: version("publisher"), SigningPrivate: version("private"), SigningPublic: version("public"), AuthPolicy: version("auth")}
	secrets.Publisher.ExpiresAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	return nativematerial.Receipt{OperationID: "operation-native-material", App: "game", PreparationPlanID: "sha256:" + strings.Repeat("a", 64), BindingID: data.ReplicaBindingID(strings.Repeat("b", 32)), EpochID: data.ReplicaEpochID(strings.Repeat("c", 32)), Outcome: "stored_unreferenced", Secrets: secrets}, nil
}

func (*materialBudgetRunner) InspectMaterial(context.Context, dispatch.NativeMaterialInspectArgs) (nativematerial.Receipt, error) {
	return nativematerial.Receipt{}, nil
}

func TestTransportMaterialBudgetOutlivesStandardDeadlineAndNeverExtendsParentOrRetries(t *testing.T) {
	input, err := dispatch.EncodeNativeMaterial("game", "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 32), []byte(`{"publisher":"fixture-token","signing_private":"fixture-private","signing_public":"fixture-public","auth_policy":{"mode":"supplied_key_jwt","require_client_token":true},"publisher_expires_at":"2030-01-01T00:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(input)
	for _, shortParent := range []bool{false, true} {
		runner := &materialBudgetRunner{delay: dispatch.RequestTimeout + time.Second}
		parent := t.Context()
		cancel := func() {}
		deadline := time.Time{}
		if shortParent {
			deadline = time.Now().Add(time.Second)
			parent, cancel = context.WithDeadline(parent, deadline)
		}
		known := t.TempDir()
		if err := os.Chmod(known, 0700); err != nil { //nolint:gosec // Private fixture known-host pin directory.
			t.Fatal(err)
		}
		client := Client{KnownHostsDir: known, Runner: runner, LookPath: func(string) (string, error) { return "/fixture/ssh", nil }}
		started := time.Now()
		response, err := client.Call(parent, validTarget(), dispatch.Request{SchemaVersion: dispatch.SchemaVersion, Op: "native_material_set", RequestID: "material-budget", Args: input})
		cancel()
		if runner.calls != 1 {
			t.Fatal("material transport retried an unknown outcome")
		}
		if shortParent {
			assertMaterialParentDeadline(t, runner, response, err, deadline)
			continue
		}
		if err != nil || !response.OK || time.Since(started) <= dispatch.RequestTimeout || runner.clientDeadline.Before(started.Add(dispatch.CallTimeout("native_material_set"))) || runner.hostDeadline.Before(started.Add(dispatch.NativeMaterialWorkTimeout)) || runner.clientDeadline.After(started.Add(dispatch.CallTimeout("native_material_set")+time.Second)) || runner.hostDeadline.After(started.Add(dispatch.NativeMaterialWorkTimeout+time.Second)) {
			t.Fatal("material delivery still expires at the standard request deadline", err)
		}
	}
}

func assertMaterialParentDeadline(t *testing.T, runner *materialBudgetRunner, response result.Envelope, err error, deadline time.Time) {
	t.Helper()
	if err == nil || response.OK || result.Classify(err).Code() != result.TransportFailure || !runner.clientDeadline.Equal(deadline) || !runner.hostDeadline.Equal(deadline) {
		t.Fatal("material transport or host extended parent deadline or lost unknown outcome", err)
	}
}
