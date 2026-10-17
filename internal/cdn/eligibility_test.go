package cdn

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
)

type eligibilityCase struct {
	Name     string            `json:"name"`
	Method   string            `json:"method"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Eligible bool              `json:"eligible"`
}

func TestSharedWorkerEligibilityCases(t *testing.T) {
	data, err := os.ReadFile("testdata/eligibility.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Policy struct {
			Hostname   string   `json:"hostname"`
			Prefix     string   `json:"prefix"`
			Extensions []string `json:"extensions"`
			TTL        uint32   `json:"ttl_seconds"`
		} `json:"policy"`
		Cases []eligibilityCase `json:"cases"`
	}
	if json.Unmarshal(data, &fixture) != nil || len(fixture.Cases) == 0 {
		t.Fatal("invalid shared eligibility fixture")
	}
	policy := CachePolicy{Prefix: fixture.Policy.Prefix, Extensions: fixture.Policy.Extensions, TTLSeconds: fixture.Policy.TTL}
	if policy.Validate() != nil {
		t.Fatal("invalid fixture policy")
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) { checkEligibilityCase(t, policy, fixture.Policy.Hostname, test) })
	}
}

func checkEligibilityCase(t *testing.T, policy CachePolicy, hostname string, test eligibilityCase) {
	t.Helper()
	request := httptest.NewRequest(test.Method, test.URL, nil)
	for name, value := range test.Headers {
		request.Header.Set(name, value)
	}
	_, eligible := policy.AssetDigest(request, hostname)
	if eligible != test.Eligible {
		t.Fatalf("eligible = %v, want %v", eligible, test.Eligible)
	}
}
