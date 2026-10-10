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
	for _, field := range []string{`"sql":"CREATE TABLE t(x)"`, `"operator":true`, `"approved":true`, `"statements":[]`} {
		bad := strings.TrimSuffix(raw, "}") + "," + field + "}"
		if _, err := decodeDataInitPlan([]byte(bad)); err == nil {
			t.Fatal("request widened review authority", field)
		}
	}
	if _, err := decodeDataInitPlan([]byte(strings.Replace(raw, hash, "../../schema.sql", 1))); err == nil {
		t.Fatal("arbitrary artifact path accepted")
	}
	if _, err := decodeDataInitApply([]byte(`{"app":"example","plan_id":"` + hash + `","operator":true}`)); err == nil {
		t.Fatal("request selected operator authority")
	}
}
