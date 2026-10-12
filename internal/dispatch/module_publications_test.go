package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

func modulePlanInput() ModulePlanArgs {
	return ModulePlanArgs{App: "game", PreparedPointID: strings.Repeat("b", 32), TargetDeploymentPlanID: "sha256:" + strings.Repeat("a", 64), Wasm: []byte{0, 'a', 's', 'm', 1, 0, 0, 0}, RecoveryReleaseIDs: []string{}}
}

func TestModulePublicationProtocolCarriesReferencesAndOwnedBytesOnly(t *testing.T) {
	planID := "sha256:" + strings.Repeat("a", 64)
	inputs := map[string]any{"module_publish_plan": modulePlanInput(), "module_publish_apply": ModuleApplyArgs{App: "game", PlanID: planID, IdempotencyKey: strings.Repeat("c", 32)}, "module_publish_inspect": ModuleInspectArgs{PlanID: planID}, "module_publish_resolve": ModuleResolveArgs{App: "game", PlanID: planID, OriginalOperationID: "original-apply", IdempotencyKey: strings.Repeat("d", 32)}}
	for operation, input := range inputs {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		request := nativeBackupRequest(t, operation, string(raw))
		if _, err = DecodeRequest(request); err != nil {
			t.Fatal(operation, err)
		}
		for _, field := range []string{"requester", "root", "transition_id", "schema", "publisher", "preflight_token", "approval_sha256", "proof"} {
			forged := strings.TrimSuffix(string(raw), "}") + `,"` + field + `":"caller-authority"}`
			if _, err = DecodeRequest(nativeBackupRequest(t, operation, forged)); err == nil {
				t.Fatal(operation, "accepted", field)
			}
		}
	}
	for _, mutate := range []func(*ModulePlanArgs){func(a *ModulePlanArgs) { a.RecoveryReleaseIDs = nil }, func(a *ModulePlanArgs) { a.RecoveryReleaseIDs = []string{"release", "release"} }, func(a *ModulePlanArgs) { a.Wasm = make([]byte, datapublish.ModuleArtifactLimit+1) }, func(a *ModulePlanArgs) { a.TargetDeploymentPlanID = "../private/path" }} {
		input := modulePlanInput()
		mutate(&input)
		raw, _ := json.Marshal(input)
		if _, err := DecodeRequest(nativeBackupRequest(t, "module_publish_plan", string(raw))); err == nil {
			t.Fatal("invalid bounded publication input admitted")
		}
	}
}

type moduleOperationsRecorder struct{ calls int }

func (r *moduleOperationsRecorder) Plan(_ context.Context, args ModulePlanArgs) (datapublish.ReviewView, error) {
	r.calls++
	return datapublish.ReviewView{PlanID: "sha256:" + strings.Repeat("a", 64), App: args.App, PointID: args.PreparedPointID, SourceKind: datapublish.CreateSource, WasmSHA256: strings.Repeat("b", 64), ProgramWireHash: strings.Repeat("c", 64), SchemaSHA256: strings.Repeat("d", 64), Requirements: []spacetimedb.SchemaRequirement{spacetimedb.ReviewGuestCode, spacetimedb.VerifyNewTableData}, Recovery: datapublish.NoPriorReleaseReview(), ExpiresAt: time.Now().UTC().Add(time.Minute)}, nil
}
func (r *moduleOperationsRecorder) Apply(context.Context, ModuleApplyArgs) (jobs.Accepted, error) {
	r.calls++
	return jobs.Accepted{Status: "accepted", OperationID: "module-operation"}, nil
}
func (r *moduleOperationsRecorder) Inspect(_ context.Context, args ModuleInspectArgs) (datapublish.Inspection, error) {
	r.calls++
	return datapublish.Inspection{PlanID: args.PlanID, App: "game", PointID: strings.Repeat("b", 32), State: "reviewed"}, nil
}
func (r *moduleOperationsRecorder) Resolve(context.Context, ModuleResolveArgs) (jobs.Accepted, error) {
	r.calls++
	return jobs.Accepted{Status: "accepted", OperationID: "module-resolution"}, nil
}

func TestModulePlanDispatchRequiresPersonalAuthorizationAndPublicResponseHasNoNativeToken(t *testing.T) {
	input, _ := json.Marshal(modulePlanInput())
	request := nativeBackupRequest(t, "module_publish_plan", string(input))
	recorder := &moduleOperationsRecorder{}
	server := NewServer("fixture", nil)
	server.ModulePublications = recorder
	if _, err := server.Handle(context.Background(), bytes.NewReader(request)); result.Classify(err).Code() != result.DispatchOperationRefused || recorder.calls != 0 {
		t.Fatal("publication reached its composer without authorization", err)
	}
	server = server.WithJobs(nil, func(_ context.Context, class Class) error {
		if class != Mutating {
			t.Fatal("guest extraction became read-only")
		}
		return nil
	})
	response, err := server.Handle(context.Background(), bytes.NewReader(request))
	if err != nil || !response.OK || recorder.calls != 1 {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(response)
	if _, err = DecodeResponse(raw, "module_publish_plan"); err != nil {
		t.Fatal("public reviewed reference could not cross RPC", err)
	}
	for _, private := range []string{"token", "requester", "secret", "root", "descriptor", "publisher"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("public review leaked private native authority", private)
		}
	}
}
