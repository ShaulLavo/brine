package dispatch

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNativeRecoveryWireKeepsSecretPrivateAndSelectorsClosed(t *testing.T) {
	plan, key := "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 32)
	raw, err := EncodeNativeRecoverySet(plan, key, []byte(`{"password":"PLANTED_RECOVERY_MATERIAL"}`))
	if err != nil {
		t.Fatal(err)
	}
	value, err := decodeNativeRecovery(raw)
	if err != nil {
		t.Fatal(err)
	}
	args := value.(NativeRecoveryArgs)
	defer args.Clear()
	if args.DeploymentPlanID != plan || args.IdempotencyKey != key || string(args.Password()) != "PLANTED_RECOVERY_MATERIAL" {
		t.Fatal("private packet changed")
	}
	redacted, _ := json.Marshal(args)
	if strings.Contains(string(redacted), "PLANTED") || strings.Contains(fmt.Sprintf("%v %#v", args, args), "PLANTED") {
		t.Fatal("private delivery metadata leaked password")
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"password":`, `"password":null,"password":`, 1),
		strings.Replace(string(raw), `"password":`, `"source":{},"password":`, 1),
		strings.Replace(string(raw), `"password":"PLANTED_RECOVERY_MATERIAL"`, `"password":"short"`, 1),
		strings.Replace(string(raw), plan, strings.Repeat("c", 64), 1),
	} {
		if _, err := decodeNativeRecovery([]byte(bad)); err == nil {
			t.Fatal("invalid private delivery request admitted")
		}
	}
	class, ok := ClassOf("native_recovery_set")
	if !ok || class != Mutating {
		t.Fatal("recovery delivery did not require mutating authorization")
	}
}
