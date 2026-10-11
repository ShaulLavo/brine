//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/policy"
)

func TestReviewedInitializationArtifactExactBytesAndShape(t *testing.T) {
	raw := `{"definition":{"database":"main","marker":"v1","catalog_sha256":"688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"},"statements":["CREATE TABLE t(x TEXT)"]}`
	for _, item := range []struct {
		name, text string
		valid      bool
	}{
		{"reviewed", raw, true},
		{"unknown-authority", strings.TrimSuffix(raw, "}") + `,"approved":true}`, false},
		{"two-artifacts", raw + raw, false},
		{"trailing-corruption", raw + "?", false},
		{"duplicate-field", strings.Replace(raw, `"marker":"v1"`, `"marker":"v1","marker":"v1"`, 1), false},
		{"alias", strings.Replace(raw, `"marker":"v1"`, `"Marker":"v1"`, 1), false},
	} {
		t.Run(item.name, func(t *testing.T) {
			digest := sha256.Sum256([]byte(item.text))
			id := "sha256:" + hex.EncodeToString(digest[:])
			artifact, err := datainit.DecodeArtifact([]byte(item.text), id)
			if item.valid {
				if err != nil {
					t.Fatal(err)
				}
				if err = artifact.Validate(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unreviewed artifact accepted")
			}
		})
	}
	if _, err := datainit.DecodeArtifact([]byte(raw), "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("altered artifact accepted")
	}
}

func TestInitializationRestrictedIdentityNeedsValidPolicyOnly(t *testing.T) {
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{"deploy:" + strings.Repeat("a", 64), "local-operator", "operator", ""} {
		engine := dataInitializationService(Service{Requester: identity, Policy: &fakePolicy{p: pol}}, "/unused")
		err := engine.Authorize(context.Background())
		if (err == nil) != strings.HasPrefix(identity, "deploy:") {
			t.Fatalf("identity %q: %v", identity, err)
		}
	}
}
