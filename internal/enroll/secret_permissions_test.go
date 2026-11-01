package enroll

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	operatorpolicy "github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
)

func secretPermissionRequest(t *testing.T, kind string) PermissionsRequest {
	t.Helper()
	raw := `{"action":"plan","identity_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","host_key":"fixture-host","app_label":"audit-only","changes":[{"kind":"` + kind + `","secret":{"app":"waffle-relay","reference":"relay-signing"}}]}`
	request, err := DecodePermissionsRequest([]byte(raw))
	if err != nil {
		t.Fatal("typed secret request refused", err)
	}
	return request
}

func TestSecretPermissionsPlanApplyShowReplayAndExactRevoke(t *testing.T) {
	s, m, _ := permissionFixture(t)
	request := secretPermissionRequest(t, "allow_secret")
	response, err := s.run(t.Context(), request)
	if err != nil || response.Plan.NoOp || response.Plan.AppLabel != "audit-only" {
		t.Fatal(response, err)
	}
	raw, err := json.Marshal(result.Success("brine host permissions", response))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodePermissionsResponse(raw, request); err != nil {
		t.Fatal("secret plan could not cross response boundary", err)
	}
	apply := permissionApply(*response.Plan, "a")
	if _, err = s.run(t.Context(), apply); err != nil {
		t.Fatal(err)
	}
	if _, err = s.run(t.Context(), apply); err != nil || m.writes != 1 {
		t.Fatal("secret grant replayed", err, m.writes)
	}
	parsed, err := operatorpolicy.Parse(m.raw)
	if err != nil || parsed.CheckSecret("waffle-relay", "relay-signing") != nil || parsed.CheckSecret("audit-only", "relay-signing") == nil {
		t.Fatal("audit label changed secret authority", err)
	}
	shown, err := s.run(t.Context(), PermissionsRequest{Action: "show"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(result.Success("brine host permissions", shown))
	if !bytes.Contains(raw, []byte(`"allowed_secrets":{"waffle-relay":["relay-signing"]}`)) {
		t.Fatal("show omitted exact secret grant", string(raw))
	}
	if _, err = decodePermissionsResponse(raw, PermissionsRequest{Action: "show"}); err != nil {
		t.Fatal("secret view refused", err)
	}
	revocation, err := s.run(t.Context(), secretPermissionRequest(t, "revoke_secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.run(t.Context(), permissionApply(*revocation.Plan, "b")); err != nil || m.writes != 2 {
		t.Fatal("secret revocation failed", err)
	}
	parsed, err = operatorpolicy.Parse(m.raw)
	if err != nil || parsed.CheckSecret("waffle-relay", "relay-signing") == nil {
		t.Fatal("revoked secret still authorized", err)
	}
	if _, err = s.run(t.Context(), apply); err != nil || m.writes != 2 {
		t.Fatal("replaying the old grant overwrote its later revocation", err)
	}
}

func TestSecretPermissionsCodecRejectsOpenMixedMalformedAndRepeatedChanges(t *testing.T) {
	request := secretPermissionRequest(t, "allow_secret")
	raw, _ := json.Marshal(request)
	valid := string(raw)
	for _, pair := range [][2]string{{`"secret":{`, `"secret":null,"extra":{`}, {`"secret":{`, `"SECRET":{`}, {`"secret":{`, `"domain":"demo.example","secret":{`}, {`"secret":{`, `"registry":null,"secret":{`}, {`"secret":{`, `"cdn_binding":null,"secret":{`}, {`"app":"waffle-relay"`, `"app":"waffle-relay","app":"other"`}, {`"reference":"relay-signing"`, `"reference":null`}, {`"reference":"relay-signing"`, `"reference":"../ref"`}, {`"app":"waffle-relay"`, `"app":"*"`}, {`"secret":{`, `"secret":{"value":"do-not-echo",`}} {
		bad := strings.Replace(valid, pair[0], pair[1], 1)
		if _, err := DecodePermissionsRequest([]byte(bad)); err == nil {
			t.Fatal("malformed secret request accepted", pair)
		}
	}
	request.Changes = append(request.Changes, request.Changes[0])
	raw, _ = json.Marshal(request)
	if _, err := DecodePermissionsRequest(raw); err == nil {
		t.Fatal("repeated secret change accepted by codec")
	}
	request.Changes[1].Kind = "revoke_secret"
	raw, _ = json.Marshal(request)
	if _, err := DecodePermissionsRequest(raw); err == nil {
		t.Fatal("contradictory secret changes accepted by codec")
	}
}

func TestSecretPermissionsNoOpPreservesPolicyBytes(t *testing.T) {
	for _, kind := range []string{"allow_secret", "revoke_secret"} {
		t.Run(kind, func(t *testing.T) { checkSecretPermissionNoOp(t, kind) })
	}
}

func checkSecretPermissionNoOp(t *testing.T, kind string) {
	t.Helper()
	s, m, _ := permissionFixture(t)
	request := secretPermissionRequest(t, kind)
	if kind == "allow_secret" {
		m.raw = bytes.Replace(m.raw, []byte("[allowed_secrets]"), []byte("[allowed_secrets]\nwaffle-relay = ['relay-signing']"), 1)
	}
	m.raw = append(m.raw, []byte("\n# preserve formatting and comments\n")...)
	before := bytes.Clone(m.raw)
	response, err := s.run(t.Context(), request)
	if err != nil || !response.Plan.NoOp || response.Plan.BeforeHash != response.Plan.AfterHash {
		t.Fatal("expected no-op secret plan", response, err)
	}
	m.failComplete = true
	apply := permissionApply(*response.Plan, "a")
	if _, err = s.run(t.Context(), apply); err == nil {
		t.Fatal("missing injected completion failure")
	}
	status, err := s.run(t.Context(), PermissionsRequest{Action: "status", IdempotencyKey: apply.IdempotencyKey})
	if err != nil || status.Operation.State != "succeeded" {
		t.Fatal("no-op status failed", status, err)
	}
	if _, err = s.run(t.Context(), apply); err != nil || m.writes != 0 || !bytes.Equal(before, m.raw) {
		t.Fatal("no-op secret operation rewrote policy bytes", err, m.writes)
	}
}

func TestSecretPermissionsStaleAndPendingGuards(t *testing.T) {
	s, m, _ := permissionFixture(t)
	request := secretPermissionRequest(t, "allow_secret")
	response, err := s.run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(m.raw)
	m.raw = append(m.raw, []byte("\n# independent operator edit\n")...)
	if _, err = s.run(t.Context(), permissionApply(*response.Plan, "a")); result.Classify(err).Code() != result.Conflict || m.writes != 0 {
		t.Fatal("stale secret plan was not fenced", err)
	}
	m.raw = original
	m.failWriteAfter = true
	apply := permissionApply(*response.Plan, "a")
	if _, err = s.run(t.Context(), apply); err == nil {
		t.Fatal("missing lost write acknowledgement")
	}
	next, err := s.run(t.Context(), secretPermissionRequest(t, "revoke_secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.run(t.Context(), permissionApply(*next.Plan, "b")); result.Classify(err).Code() != result.RecoveryRequired || m.writes != 1 {
		t.Fatal("pending grant failed to fence revocation", err)
	}
	status, err := s.run(t.Context(), PermissionsRequest{Action: "status", IdempotencyKey: apply.IdempotencyKey})
	if err != nil || status.Operation.State != "succeeded" {
		t.Fatal("grant readback failed", status, err)
	}
	if _, err = s.run(t.Context(), apply); err != nil || m.writes != 1 {
		t.Fatal("grant recovery repeated write", err)
	}
	if _, err = s.run(t.Context(), permissionApply(*next.Plan, "a")); result.Classify(err).Code() != result.Conflict {
		t.Fatal("same key accepted another secret plan", err)
	}
	if _, err = s.run(t.Context(), permissionApply(*next.Plan, "b")); err != nil || m.writes != 2 {
		t.Fatal("readback did not release pending fence", err)
	}
}

func TestSecretPermissionsViewCodecRejectsMalformedMaps(t *testing.T) {
	s, _, _ := permissionFixture(t)
	response, err := s.run(t.Context(), secretPermissionRequest(t, "allow_secret"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result.Success("brine host permissions", response))
	valid := string(raw)
	for _, pair := range [][2]string{{`"allowed_secrets":{}`, `"allowed_secrets":null`}, {`"allowed_secrets":{}`, `"AllowedSecrets":{}`}, {`"waffle-relay":["relay-signing"]`, `"waffle-relay":null`}, {`"waffle-relay":["relay-signing"]`, `"waffle-relay":[null]`}, {`"waffle-relay":["relay-signing"]`, `"waffle-relay":["relay-signing","relay-signing"]`}, {`"waffle-relay":["relay-signing"]`, `"waffle-relay":["../ref"]`}, {`"waffle-relay":["relay-signing"]`, `"../app":["relay-signing"]`}, {`"waffle-relay":["relay-signing"]`, `"waffle-relay":["relay-signing"],"waffle-relay":[]`}} {
		bad := strings.Replace(valid, pair[0], pair[1], 1)
		if bad == valid {
			t.Fatal("fixture did not change")
		}
		if _, err := decodePermissionsResponse([]byte(bad), PermissionsRequest{Action: "plan"}); err == nil {
			t.Fatal("malformed secret view accepted", pair)
		}
	}
}
