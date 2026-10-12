package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/result"
)

func materialWireFixture(t *testing.T) json.RawMessage {
	t.Helper()
	wire, err := EncodeNativeMaterial("game", "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 32), []byte(`{"publisher":"PLANTED_TOKEN","signing_private":"PLANTED_PRIVATE","signing_public":"PLANTED_PUBLIC","auth_policy":{"mode":"supplied_key_jwt","require_client_token":true},"publisher_expires_at":"2030-01-01T00:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	return wire
}
func TestNativeMaterialWireKeepsFourRolesPrivateAndClosed(t *testing.T) {
	wire := materialWireFixture(t)
	defer clear(wire)
	decoded, err := decodeNativeMaterial(wire)
	if err != nil {
		t.Fatal(err)
	}
	args := decoded.(NativeMaterialArgs)
	defer args.Clear()
	visible, _ := json.Marshal(args)
	if strings.Contains(string(visible), "PLANTED") || strings.Contains(fmt.Sprintf("%v %#v", args, args), "PLANTED") {
		t.Fatal("native packet became metadata")
	}
	packet := args.Packet()
	defer clear(packet)
	if !strings.Contains(string(packet), "PLANTED_PRIVATE") {
		t.Fatal("private role changed")
	}
	for _, bad := range []string{
		strings.Replace(string(wire), `"packet":`, `"packet":null,"packet":`, 1),
		strings.Replace(string(wire), `"publisher":`, `"runtime":{},"publisher":`, 1),
		strings.Replace(string(wire), `"publisher":`, `"publisher":null,"publisher":`, 1),
		strings.Replace(string(wire), `"publisher":"PLANTED_TOKEN"`, `"publisher":null`, 1),
		strings.Replace(string(wire), `"require_client_token":true`, `"require_client_token":false`, 1),
	} {
		if _, err := decodeNativeMaterial([]byte(bad)); err == nil {
			t.Fatal("invalid private role packet admitted")
		}
	}
	class, ok := ClassOf("native_material_set")
	if !ok || class != Mutating {
		t.Fatal("delivery not authorized as mutation")
	}
	class, ok = ClassOf("native_material_status")
	if !ok || class != ReadOnly {
		t.Fatal("metadata inspection mutated host")
	}
}
func TestNativeMaterialClosedResponseRejectsExtraNestedFields(t *testing.T) {
	version := func(ref string) data.EngineSecretVersion {
		return data.EngineSecretVersion{Reference: ref, Version: 1, SHA256: strings.Repeat("a", 64)}
	}
	secrets := data.EngineSecrets{Publisher: version("publisher"), SigningPrivate: version("private"), SigningPublic: version("public"), AuthPolicy: version("auth")}
	secrets.Publisher.ExpiresAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	receipt := nativematerial.Receipt{OperationID: "operation-native-material", App: "game", PreparationPlanID: "sha256:" + strings.Repeat("a", 64), BindingID: data.ReplicaBindingID(strings.Repeat("b", 32)), EpochID: data.ReplicaEpochID(strings.Repeat("c", 32)), Outcome: "stored_unreferenced", Secrets: secrets}
	raw, _ := json.Marshal(result.Success("brine host native_material_set", receipt))
	if _, err := DecodeResponse(raw, "native_material_set"); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(raw), `"reference":"private"`, `"plaintext":"PLANTED_PRIVATE","reference":"private"`, 1)
	if _, err := DecodeResponse([]byte(bad), "native_material_set"); err == nil {
		t.Fatal("private role/body in receipt accepted")
	}
}
func TestNativeMaterialRejectsAuthorizationWithoutPrivateOutput(t *testing.T) {
	wire := materialWireFixture(t)
	defer clear(wire)
	request, _ := EncodeRequest(Request{SchemaVersion: SchemaVersion, Op: "native_material_set", RequestID: "native-material", Args: wire})
	server := NewServer("fixture", nil).WithJobs(nil, func(context.Context, Class) error { return result.New(result.DispatchOperationRefused, nil) })
	response, err := server.Handle(context.Background(), strings.NewReader(string(request)))
	if err == nil || response.OK {
		t.Fatal("private mutation bypassed authorization")
	}
	raw, _ := json.Marshal(response)
	if strings.Contains(string(raw), "PLANTED") {
		t.Fatal("private material leaked in refusal")
	}
}

func TestNativeMaterialInspectionRequiresOneExactSelector(t *testing.T) {
	key, planID := strings.Repeat("b", 32), "sha256:"+strings.Repeat("a", 64)
	raw := []byte(`{"app":"game","preparation_plan_id":"` + planID + `","idempotency_key":"` + key + `"}`)
	decoded, err := decodeNativeMaterialInspect(raw)
	if err != nil || !decoded.(NativeMaterialInspectArgs).Valid() {
		t.Fatal("valid lost-response lookup refused", err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"app":"game"`, `"operation_id":"material-operation","app":"game"`, 1),
		strings.Replace(string(raw), `"app":"game"`, `"app":"game","app":"other"`, 1),
		strings.Replace(string(raw), `"idempotency_key":"`+key+`"`, `"idempotency_key":null`, 1),
		strings.Replace(string(raw), `"app":"game",`, "", 1),
	} {
		if _, err := decodeNativeMaterialInspect([]byte(bad)); err == nil {
			t.Fatal("mixed or incomplete metadata selector accepted")
		}
	}
}
