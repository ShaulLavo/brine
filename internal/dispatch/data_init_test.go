package dispatch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/datainit"
	"strings"
	"testing"
)

func TestDataInitializationPlanCarriesReviewedBytesApplyReferencesOnly(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	raw := reviewedInitRequest(t, "example")
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

func reviewedInitRequest(t *testing.T, app string) string {
	t.Helper()
	raw := []byte(`{"definition":{"database":"main","marker":"v1","catalog_sha256":"688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"},"statements":["CREATE TABLE t(x TEXT)"]}`)
	sum := sha256.Sum256(raw)
	encoded, err := json.Marshal(DataInitPlanArgs{App: app, FirstReleasePlan: "sha256:" + strings.Repeat("a", 64), Artifact: "sha256:" + hex.EncodeToString(sum[:]), ArtifactBytes: raw})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
func TestInitializationWireRejectsTamperedAndMissingArtifact(t *testing.T) {
	raw := reviewedInitRequest(t, "example")
	var request DataInitPlanArgs
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range [][]byte{nil, []byte("changed"), make([]byte, datainit.ArtifactLimit+1)} {
		request.ArtifactBytes = artifact
		encoded, _ := json.Marshal(request)
		if _, err := decodeDataInitPlan(encoded); err == nil {
			t.Fatal("tampered or absent artifact accepted")
		}
	}
}

func TestInitializationReviewFitsBoundedWireAboveOldRequestLimit(t *testing.T) {
	var args DataInitPlanArgs
	if err := json.Unmarshal([]byte(reviewedInitRequest(t, "example")), &args); err != nil {
		t.Fatal(err)
	}
	// Whitespace is part of the exact artifact digest, not re-encoded away.
	args.ArtifactBytes = append(args.ArtifactBytes, []byte(strings.Repeat(" ", 128<<10))...)
	sum := sha256.Sum256(args.ArtifactBytes)
	args.Artifact = "sha256:" + hex.EncodeToString(sum[:])
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := EncodeRequest(Request{SchemaVersion: SchemaVersion, Op: "data_init_plan", RequestID: "large-review", Args: raw})
	if err != nil || len(wire) <= 64<<10 || len(wire) >= RequestLimit {
		t.Fatal("bounded review encoding failed", err)
	}
	decoded, err := DecodeRequest(wire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeDataInitPlan(decoded.Args); err != nil {
		t.Fatal(err)
	}
}
