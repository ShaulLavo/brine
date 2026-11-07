package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/readiness"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

type doctorReadinessFixture struct{ missing bool }

func (f doctorReadinessFixture) Read(ctx context.Context) (readiness.Report, error) {
	report, err := (readiness.Reader{}).Read(ctx)
	if f.missing {
		report.Facts.Control = readiness.Known(readiness.Control{State: "missing", QuickCheck: readiness.Unknown[bool]("store_unavailable")})
		readiness.Evaluate(&report)
	}
	return report, err
}
func TestDoctorTargetOutputParity(t *testing.T) {
	for _, mode := range []string{"human", "json", "jsonl"} {
		t.Run(mode, func(t *testing.T) { testDoctorTargetOutput(t, mode) })
	}
}
func testDoctorTargetOutput(t *testing.T, mode string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	configureDoctorFixture(t, &deps)
	server := dispatch.NewServer("fixture", nil)
	server.Readiness = doctorReadinessFixture{missing: true}
	wire := &diagnoseTransport{server: server}
	deps.OperationClient = wire
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	args := []string{"doctor", "--target", "fixture", "--config-dir", t.TempDir()}
	if mode != "human" {
		args = append(args, "--"+mode)
	}
	if err := Execute(deps, args); err != nil {
		t.Fatal(err)
	}
	if len(wire.requests) != 1 || wire.requests[0].Op != "readiness" || string(wire.requests[0].Args) != "{}" {
		t.Fatal(wire.requests)
	}
	if mode != "human" {
		assertDoctorMachineParity(t, out.Bytes(), mode)
	}
	if diagnostics.Len() != 0 {
		t.Fatal(diagnostics.String())
	}
	golden := filepath.Join("testdata", "doctor-target."+mode)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, out.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(golden) // #nosec G304 -- Fixed testdata directory and a closed mode table.
	if err != nil || !bytes.Equal(out.Bytes(), expected) {
		t.Fatalf("%s differs: %v\n%s", golden, err, out.String())
	}
}
func assertDoctorMachineParity(t *testing.T, raw []byte, mode string) {
	t.Helper()
	var response struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || !response.OK {
		t.Fatal(string(raw), err)
	}
	data := response.Data
	if mode == "jsonl" {
		var event struct {
			Event  string          `json:"event"`
			Report json.RawMessage `json:"report"`
		}
		if json.Unmarshal(data, &event) != nil || event.Event != "complete" {
			t.Fatal(string(data))
		}
		data = event.Report
	}
	var got remoteDoctorReport
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	expected, _ := (doctorReadinessFixture{missing: true}).Read(t.Context())
	if got.Host.Ready != expected.Ready || len(got.Host.Findings) != 1 || got.Host.Findings[0].Code != "control_store_missing" || len(got.Client.Checks) != 2 || got.Client.Checks[0].Version != "9.9p2" || got.Client.RemoteHostReadiness {
		t.Fatal(got)
	}
}
func TestDoctorLocalDoesNotCollectReadiness(t *testing.T) {
	for _, mode := range []string{"--json", "--jsonl"} {
		var out, diagnostics bytes.Buffer
		deps := testDependencies(t, &out, &diagnostics)
		configureDoctorFixture(t, &deps)
		deps.OperationClient = rejectDoctorCall{t: t}
		if err := Execute(deps, []string{"doctor", mode}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"remote_host_readiness":false`) || !strings.Contains(out.String(), `"scope":"local_client"`) {
			t.Fatal(out.String())
		}
	}
}

type rejectDoctorCall struct{ t *testing.T }

func (f rejectDoctorCall) Call(context.Context, transport.Target, dispatch.Request) (result.Envelope, error) {
	f.t.Fatal("plain doctor made a remote call")
	return result.Envelope{}, nil
}
func TestDoctorTargetSelectionBoundary(t *testing.T) {
	for _, target := range []string{"", "bad/name", "fixture\nsecret"} {
		var out, diagnostics bytes.Buffer
		deps := testDependencies(t, &out, &diagnostics)
		deps.OperationClient = rejectDoctorCall{t: t}
		if err := Execute(deps, []string{"doctor", "--target", target, "--json"}); result.ExitCode(err) != 2 {
			t.Fatal(target, err)
		}
	}
}
