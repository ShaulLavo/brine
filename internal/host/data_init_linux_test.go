//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
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
			var artifact data.SchemaInitializer
			err := decodeReviewedInitialization([]byte(item.text), id, &artifact)
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
	var artifact data.SchemaInitializer
	if err := decodeReviewedInitialization([]byte(raw), "sha256:"+strings.Repeat("a", 64), &artifact); err == nil {
		t.Fatal("altered artifact accepted")
	}
}

func TestInitializationHostPolicyDefaultOffAndLocalOperator(t *testing.T) {
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name              string
		operator, allowed bool
		prefix            string
	}{
		{"restricted-agent", false, false, ""},
		{"permitted-agent", false, true, "allow_agent_migrations=true\n"},
		{"local-operator", true, true, ""},
	} {
		t.Run(item.name, func(t *testing.T) {
			pol, err := policy.Parse(append([]byte(item.prefix), raw...))
			if err != nil {
				t.Fatal(err)
			}
			engine := dataInitializationService(Service{Requester: "fixture", Policy: &fakePolicy{p: pol}}, "/unused", item.operator)
			err = engine.Authorize(context.Background())
			if (err == nil) != item.allowed {
				t.Fatalf("authority %v", err)
			}
		})
	}
}
