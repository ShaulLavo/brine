//go:build linux

package host

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestNativeAbandonRechecksLivePolicyWithoutAdmissionChange(t *testing.T) {
	raw, err := os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	original, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	loader := &fakePolicy{p: original}
	service := Service{Policy: loader}
	pointPolicy := strings.TrimPrefix(original.Hash(), "sha256:")
	if err := checkNativePointPolicy(t.Context(), service, pointPolicy); err != nil {
		t.Fatal("matching live policy refused", err)
	}
	changed, err := policy.Parse([]byte(strings.Replace(string(raw), "max_maintenance_seconds = 120", "max_maintenance_seconds = 119", 1)))
	if err != nil || changed.Hash() == original.Hash() {
		t.Fatal("policy fixture did not change", err)
	}
	loader.p = changed
	if err := checkNativePointPolicy(t.Context(), service, pointPolicy); err == nil || result.Classify(err).Code() != result.PolicyRefused {
		t.Fatal("changed live policy admitted with original point policy", err)
	}
	loader.p, loader.err = original, errors.New("policy unreadable")
	if err := checkNativePointPolicy(t.Context(), service, pointPolicy); err == nil || result.Classify(err).Code() != result.PolicyRefused {
		t.Fatal("unreadable policy admitted", err)
	}
}
