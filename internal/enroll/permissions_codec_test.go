package enroll

import (
	"encoding/json"
	"strings"
	"testing"

	operatorpolicy "github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestPermissionsRequestCodecRoundTripsClosedVariants(t *testing.T) {
	registry := &operatorpolicy.Registry{Host: "registry.example", RepositoryPrefixes: []string{"demo"}}
	requests := []PermissionsRequest{{Action: "show"}, {Action: "plan", Changes: []operatorpolicy.PermissionAction{{Kind: operatorpolicy.AllowRegistry, Registry: registry}}}, {Action: "plan", AppLabel: "demo", Changes: []operatorpolicy.PermissionAction{{Kind: operatorpolicy.AllowDomain, Domain: "demo.example"}}}, {Action: "apply", PlanID: strings.Repeat("a", 64), IdempotencyKey: strings.Repeat("b", 32)}, {Action: "status", IdempotencyKey: strings.Repeat("b", 32)}}
	for _, request := range requests {
		checkPermissionRequestRoundTrip(t, request)
	}
}

func checkPermissionRequestRoundTrip(t *testing.T, request PermissionsRequest) {
	t.Helper()
	request.IdentityKey = make([]byte, 32)
	request.HostKey = "fixture-host"
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePermissionsRequest(raw)
	if err != nil || decoded.Action != request.Action || decoded.PlanID != request.PlanID || decoded.AppLabel != request.AppLabel {
		t.Fatal("closed variant failed", request.Action, err)
	}
}

func TestPermissionsResponseCodecClosesNestedPlanAndError(t *testing.T) {
	_, _, plan := permissionFixture(t)
	planEnvelope, err := json.Marshal(result.Success("brine host permissions", PermissionsResponse{Plan: &plan}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodePermissionsResponse(planEnvelope, PermissionsRequest{Action: "plan"}); err != nil {
		t.Fatal("valid plan rejected", err)
	}
	valid := string(planEnvelope)
	malformed := []string{
		strings.Replace(valid, `"no_op":false`, `"no_op":null`, 1),
		strings.Replace(valid, `"before_hash":`, `"BEFORE_HASH":`, 1),
		strings.Replace(valid, `"enrollment_id":`, `"enrollment_id":null,"enrollment_id":`, 1),
		strings.Replace(valid, `"domains":[]`, `"domains":null`, 1),
		strings.Replace(valid, `"domain":"demo.example"`, `"domain":"demo.example","DOMAIN":"other.example"`, 1),
		strings.Replace(valid, `"kind":"allow_domain"`, `"kind":"allow_domain","kind":"allow_domain"`, 1),
	}
	for _, raw := range malformed {
		if raw == valid {
			t.Fatal("malformed fixture did not change")
		}
		if _, err = decodePermissionsResponse([]byte(raw), PermissionsRequest{Action: "plan"}); err == nil {
			t.Fatal("nested malformed plan accepted", raw)
		}
	}
	failure, err := json.Marshal(result.Failure("brine host permissions", result.New(result.PolicyRefused, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{strings.Replace(string(failure), `"code":`, `"CODE":`, 1), strings.Replace(string(failure), `"retryable":false`, `"retryable":null`, 1), strings.Replace(string(failure), `"code":`, `"code":"policy_refused","code":`, 1)} {
		if _, err = decodePermissionsResponse([]byte(raw), PermissionsRequest{Action: "show"}); result.Classify(err).Code() != result.TransportInvalidResponse {
			t.Fatal("malformed error accepted", err)
		}
	}
}

func TestPermissionsResponseCodecClosesOperation(t *testing.T) {
	op := PermissionsOperation{ID: strings.Repeat("b", 32), PlanID: strings.Repeat("a", 64), State: "succeeded", BeforeHash: strings.Repeat("c", 64), AfterHash: strings.Repeat("d", 64)}
	raw, err := json.Marshal(result.Success("brine host permissions", PermissionsResponse{Operation: &op}))
	if err != nil {
		t.Fatal(err)
	}
	request := PermissionsRequest{Action: "apply", PlanID: op.PlanID, IdempotencyKey: op.ID}
	if _, err = decodePermissionsResponse(raw, request); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(string(raw), `"state":"succeeded"`, `"state":"intent","state":"succeeded"`, 1), strings.Replace(string(raw), `"plan_id":`, `"PLAN_ID":`, 1), strings.Replace(string(raw), `"before_hash":`, `"before_hash":null,"extra":`, 1)} {
		if _, err = decodePermissionsResponse([]byte(bad), request); err == nil {
			t.Fatal("malformed operation accepted")
		}
	}
}
