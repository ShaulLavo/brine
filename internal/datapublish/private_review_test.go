package datapublish

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func privateReviewFixture(t *testing.T, create bool) PrivateReview {
	t.Helper()
	raw, err := os.ReadFile("../spec/testdata/managed-module.wasm")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := NewArtifact(raw)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := os.ReadFile("../spacetimedb/testdata/descriptor-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	spec := planFixture(t, create)
	spec.Candidate, err = NewCandidate(string(artifact.wasm), string(artifact.program), nativeSchema(t, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	spec.Review = reviewFixture(t, spec.Candidate, create)
	var source, response, token []byte
	if !create {
		var envelope map[string]json.RawMessage
		if err = json.Unmarshal(descriptor, &envelope); err != nil {
			t.Fatal(err)
		}
		source = envelope["V10"]
		response, token = []byte("retained private preflight response"), []byte("retained private token")
		spec.Preflight.ResponseSHA256, spec.Preflight.TokenSHA256 = privateBytesSHA256(response), privateBytesSHA256(token)
	}
	plan, err := NewPlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	review, err := NewPrivateReview(plan, artifact, source, descriptor, response, token)
	if err != nil {
		t.Fatal(err)
	}
	return review
}

func TestPrivateReviewOwnsMaterialAndKeepsTokenOutOfPresentation(t *testing.T) {
	for _, create := range []bool{false, true} {
		review := privateReviewFixture(t, create)
		raw, err := review.StorageBytes()
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodePrivateReview(raw)
		if err != nil || decoded.Plan().ID() != review.Plan().ID() {
			t.Fatal("private stored review did not round trip", err)
		}
		descriptor := decoded.CandidateDescriptor()
		descriptor[0] = 'x'
		if !create {
			decoded.NativePreflightToken()[0] = 'x'
		}
		kept, err := decoded.StorageBytes()
		if err != nil || !bytes.Equal(kept, raw) {
			t.Fatal("caller changed retained source/token/artifact", err)
		}
		if fmt.Sprintf("%s %#v", review, review) != "[private publication review] [private publication review]" {
			t.Fatal("private review leaked through formatting")
		}
		if _, err := json.Marshal(review); err == nil {
			t.Fatal("private review acquired public JSON presentation")
		}
	}
}

func TestPrivateReviewRefusesChangedBytesAndForeignArms(t *testing.T) {
	review := privateReviewFixture(t, false)
	for _, mutate := range []func(map[string]any){
		func(value map[string]any) { value["native_preflight_token"] = []byte("changed") },
		func(value map[string]any) { value["native_preflight_response"] = []byte("changed") },
		func(value map[string]any) { value["artifact"] = []byte("changed") },
		func(value map[string]any) { value["candidate_descriptor"] = []byte(`{"V10":null}`) },
		func(value map[string]any) { value["source_schema_v10"] = nil },
		func(value map[string]any) { value["reviewed"] = true },
	} {
		raw, _ := review.StorageBytes()
		var value map[string]any
		if json.Unmarshal(raw, &value) != nil {
			t.Fatal("private fixture decode failed")
		}
		mutate(value)
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodePrivateReview(raw); !errors.Is(err, ErrRecord) {
			t.Fatal("private review accepted mismatched material", err)
		}
	}
	if _, err := NewPrivateReview(review.plan, review.artifact, review.source, review.descriptor, review.preflight, []byte(strings.Repeat("x", NativePreflightTokenLimit+1))); !errors.Is(err, ErrRecord) {
		t.Fatal("oversized private token accepted", err)
	}
	create := privateReviewFixture(t, true)
	if _, err := NewPrivateReview(create.plan, create.artifact, review.source, create.descriptor, nil, nil); !errors.Is(err, ErrRecord) {
		t.Fatal("fresh review acquired initialized source material", err)
	}
}
