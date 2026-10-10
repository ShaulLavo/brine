package dispatch

import (
	"strings"
	"testing"
)

func TestDataInitializationRequestsCarryReferencesOnly(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	raw := `{"app":"example","first_release_plan":"` + hash + `","artifact":"` + hash + `"}`
	if _, err := decodeDataInitPlan([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"sql":"CREATE TABLE t(x)"`, `"operator":true`, `"approved":true`, `"statements":[]`, `"requester":"local-operator"`, `"operation_id":"local-operator"`} {
		bad := strings.TrimSuffix(raw, "}") + "," + field + "}"
		if _, err := decodeDataInitPlan([]byte(bad)); err == nil {
			t.Fatal("request widened review authority", field)
		}
	}
	if _, err := decodeDataInitPlan([]byte(strings.Replace(raw, hash, "../../schema.sql", 1))); err == nil {
		t.Fatal("arbitrary artifact path accepted")
	}
	for _, field := range []string{`"operator":true`, `"requester":"local-operator"`, `"operation_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV"`} {
		if _, err := decodeDataInitApply([]byte(`{"app":"example","plan_id":"` + hash + `",` + field + `}`)); err == nil {
			t.Fatal("request selected or reused local authority", field)
		}
	}
}
