package cdn

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fixtureBinding() Binding {
	profile := ProfileScope{Reference: "edge-a", Driver: "local-http", Fingerprint: Hash([]byte("profile"))}
	return Binding{Reference: "public", App: "demo", Target: Hash([]byte("host")), Hostname: "game.example.test",
		Origin:       Origin{Address: "https://game.example.test", Host: "game.example.test", TLSName: "game.example.test"},
		Cache:        CachePolicy{Prefix: "/objects/", Extensions: []string{"png", "ogg"}, TTLSeconds: ImmutableTTL},
		AllowedEdges: []ProfileScope{profile}, Traffic: profile,
		TrafficAuthority: TrafficAuthority{ResourceID: "existing-record", Hostname: "game.example.test", Before: Hash([]byte("direct"))}}
}

func TestBindingParserAndCanonicalEncoding(t *testing.T) {
	binding := fixtureBinding()
	canonical, err := binding.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseBinding(canonical)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Cache.Extensions = []string{"ogg", "png"}
	reordered, err := parsed.CanonicalBytes()
	if err != nil || !bytes.Equal(canonical, reordered) {
		t.Fatal("canonical policy depends on extension order", err)
	}
	if binding.Cache.Extensions[0] != "png" {
		t.Fatal("canonicalization mutated caller input")
	}
	if _, err := ParseBinding(bytes.Replace(canonical, []byte(`"reference":"public"`), []byte(`"secret":"token","reference":"public"`), 1)); err == nil {
		t.Fatal("unknown credential field accepted")
	}
	if _, err := ParseBinding(append(canonical, []byte(" {}")...)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	parsed.TrafficAuthority.Hostname = "other.example.test"
	if parsed.Validate() == nil {
		t.Fatal("authority for another hostname accepted")
	}
}

func TestCacheEligibilityPreservesPublicNoStoreRequests(t *testing.T) {
	policy := fixtureBinding().Cache
	request := httptest.NewRequest("GET", "https://game.example.test/objects/"+strings.Repeat("a", 64)+".png", nil)
	request.Header.Set("Cache-Control", "no-cache, no-store")
	request.Header.Set("Pragma", "no-cache")
	if _, ok := policy.AssetDigest(request, "game.example.test"); !ok {
		t.Fatal("public immutable browser bypass was rejected")
	}
	request.Header.Set("Cookie", "private=session")
	if _, ok := policy.AssetDigest(request, "game.example.test"); ok {
		t.Fatal("private request eligible")
	}
	request.Header.Del("Cookie")
	request.URL.RawQuery = "token=private"
	if _, ok := policy.AssetDigest(request, "game.example.test"); ok {
		t.Fatal("query request eligible")
	}
	request.URL.RawQuery = ""
	request.URL.Path = "/objects/index.json"
	if _, ok := policy.AssetDigest(request, "game.example.test"); ok {
		t.Fatal("mutable index eligible")
	}
}

func TestCacheRejectsEncodedAliasesAndNoncanonicalPrefixes(t *testing.T) {
	policy := fixtureBinding().Cache
	request := httptest.NewRequest("GET", "https://game.example.test/objects/%61"+strings.Repeat("a", 63)+".png", nil)
	if _, ok := policy.AssetDigest(request, "game.example.test"); ok {
		t.Error("encoded filename alias eligible")
	}
	for _, prefix := range []string{"/with space/", "/control\n/", "/café/", "/wild*/", "/" + strings.Repeat("a", 256) + "/"} {
		policy.Prefix = prefix
		if policy.Validate() == nil {
			t.Errorf("noncanonical prefix accepted: %q", prefix)
		}
	}
}

func TestCacheRejectsPresentEmptyPrivateHeaders(t *testing.T) {
	policy := fixtureBinding().Cache
	for _, header := range []string{"Authorization", "Cookie", "Range"} {
		request := httptest.NewRequest("GET", "https://game.example.test/objects/"+strings.Repeat("a", 64)+".png", nil)
		request.Header.Set(header, "")
		if _, ok := policy.AssetDigest(request, "game.example.test"); ok {
			t.Errorf("empty %s header eligible", header)
		}
	}
}

func fixtureEffect() Effect {
	binding := fixtureBinding()
	owner := Owner{Target: binding.Target, App: binding.App, Incarnation: "app-1", Attachment: "attachment-1"}
	attachment := Attachment{Mode: Enabled, Owner: owner, Binding: binding, Profile: binding.AllowedEdges[0], Revision: "revision-1", SourceRelease: "release-1"}
	resource := OwnedResource{Kind: EdgeConfiguration, ExactName: "owned-edge", Owner: owner, Fingerprint: Hash([]byte("edge"))}
	action := Action{Kind: PutEdge, Attachment: attachment, Profile: attachment.Profile, Resource: resource, Expected: Hash(nil), Desired: resource.Fingerprint}
	return Effect{Permit: Permit{Operation: "operation-1", Effect: "effect-1", Action: action}, Stage: Planned}
}

func TestIssuedClaimIsNeverReplayedAndAbsentDoesNotComplete(t *testing.T) {
	effect, work, err := fixtureEffect().Claim("first-writer")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := work.(NeedEffect); !ok {
		t.Fatal("first claim did not grant effect")
	}
	claimed, retry, err := effect.Claim("second-writer")
	if err != nil || claimed.Permit.Claim != "first-writer" {
		t.Fatal("claim changed", err)
	}
	if _, ok := retry.(NeedObservation); !ok {
		t.Fatal("lost permit reply permitted another mutation")
	}
	_, next, err := effect.Inspect(Observed{Effect: effect.Permit.Effect, Relation: Absent})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.(Waiting); !ok {
		t.Fatal("absence proved completion")
	}
}

func TestAcknowledgedEffectNeedsIndependentExactResourceObservation(t *testing.T) {
	effect, _, _ := fixtureEffect().Claim("writer")
	resource := effect.Permit.Action.Resource
	resource.ID = "native-generated-id"
	result := Acknowledged{Effect: effect.Permit.Effect, Receipt: Hash([]byte("native-ack")), Resources: []OwnedResource{resource}}
	accepted, err := effect.Record(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := accepted.Next().(NeedObservation); !ok {
		t.Fatal("API acknowledgement committed success")
	}
	observation := Observation{Profile: effect.Permit.Action.Profile, Resources: []OwnedResource{resource}, Fingerprint: effect.Permit.Action.Desired, ObservedAt: time.Now().UTC()}
	confirmed, work, err := accepted.Inspect(Observed{Effect: effect.Permit.Effect, Current: observation, Relation: Desired})
	if err != nil || confirmed.Stage != Confirmed {
		t.Fatal("acknowledged native resource not confirmed", err)
	}
	if _, ok := work.(Finished); !ok {
		t.Fatal("verified effect not finished")
	}
	observation.Resources[0].ID = "foreign-id"
	if _, _, err := accepted.Inspect(Observed{Effect: effect.Permit.Effect, Current: observation, Relation: Desired}); err == nil {
		t.Fatal("different native resource adopted")
	}
}

func TestUnknownWriteCannotCompleteFromDesiredGET(t *testing.T) {
	effect, _, _ := fixtureEffect().Claim("writer")
	unknown, err := effect.Record(Uncertain{Effect: effect.Permit.Effect, Code: "lost_reply"})
	if err != nil {
		t.Fatal(err)
	}
	current := Observation{Profile: effect.Permit.Action.Profile, Fingerprint: effect.Permit.Action.Desired, ObservedAt: time.Now().UTC()}
	_, work, err := unknown.Inspect(Observed{Effect: effect.Permit.Effect, Current: current, Relation: Desired})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := work.(Waiting); !ok {
		t.Fatal("desired GET invented exactly-once completion")
	}
	wire, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	var restored Effect
	if json.Unmarshal(wire, &restored) != nil {
		t.Fatal("cannot restore canonical effect")
	}
	if _, ok := restored.Next().(NeedObservation); !ok {
		t.Fatal("restart reopened effect claim")
	}
}

func TestQualificationBindsOneCanonicalPublicAsset(t *testing.T) {
	effect := fixtureEffect()
	assetPath := "/objects/" + strings.Repeat("a", 64) + ".png"
	sample, err := NewAssetSample(effect.Permit.Action.Attachment.Binding.Cache, assetPath)
	if err != nil || sample.Digest != Fingerprint("sha256:"+strings.Repeat("a", 64)) {
		t.Fatal("sample does not derive its hash", err)
	}
	qualification := Qualification{Attachment: effect.Permit.Action.Attachment, Sample: sample}
	if qualification.Validate() != nil {
		t.Fatal("valid sample refused")
	}
	qualification.Sample.Digest = Hash([]byte("another asset"))
	if qualification.Validate() == nil {
		t.Fatal("forged sample hash accepted")
	}
	for _, input := range []string{"https://other.example/objects/a.png", assetPath + "?private=1", "/objects/%61" + strings.Repeat("a", 63) + ".png", "/api/private"} {
		if _, err := NewAssetSample(effect.Permit.Action.Attachment.Binding.Cache, input); err == nil {
			t.Errorf("unsafe sample accepted: %q", input)
		}
	}
}

func TestNativeCompletionEvidenceSurvivesStatePersistence(t *testing.T) {
	effect, _, _ := fixtureEffect().Claim("writer")
	proof := Hash([]byte("native completion observation"))
	current := Observation{Profile: effect.Permit.Action.Profile, Fingerprint: effect.Permit.Action.Desired, ObservedAt: time.Now().UTC()}
	confirmed, _, err := effect.Inspect(Completed{Effect: effect.Permit.Effect, Current: current, Evidence: proof})
	if err != nil || confirmed.Completion != proof {
		t.Fatal("native evidence lost", err)
	}
	encoded, err := json.Marshal(confirmed)
	if err != nil {
		t.Fatal(err)
	}
	var restored Effect
	if json.Unmarshal(encoded, &restored) != nil || restored.Completion != proof {
		t.Fatal("stored state lost native completion evidence")
	}
}
