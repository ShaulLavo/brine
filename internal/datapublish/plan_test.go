package datapublish

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

func nativeSchema(t *testing.T, variant string) spacetimedb.CanonicalSchema {
	t.Helper()
	root, err := os.OpenRoot("../spacetimedb/testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	raw, err := root.ReadFile("descriptor-" + variant + ".json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := spacetimedb.DecodeDescriptorV10(raw, spacetimedb.DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func testFacts() BindingFacts {
	return BindingFacts{App: "example", IncarnationID: strings.Repeat("1", 32), DatabaseID: strings.Repeat("2", 32), BindingID: strings.Repeat("3", 32), EpochID: strings.Repeat("4", 32), Generation: 1,
		TargetSHA256: strings.Repeat("a", 64), PolicySHA256: strings.Repeat("b", 64), DesiredSHA256: strings.Repeat("c", 64), ConfigSHA256: strings.Repeat("d", 64),
		SecretVersions: SecretReferences{Publisher: "brine.example.publisher.v1", SigningPublic: "brine.example.public.v1", SigningPrivate: "brine.example.private.v1", RecoveryPassword: "brine.example.recovery.v1"}} // #nosec G101 -- Immutable version names checked by secretReference, never credential values.
}

func capturedFixture(t *testing.T) Source {
	t.Helper()
	point := CapturedPoint{PointID: strings.Repeat("5", 32), FenceID: strings.Repeat("6", 32), PreparationOpID: "prepared-fixture",
		RestoreReceiptSHA256: strings.Repeat("a", 64), BundleManifestSHA256: strings.Repeat("b", 64), SourceSealSHA256: strings.Repeat("c", 64), StateWitnessSHA256: strings.Repeat("d", 64), StopProofSHA256: strings.Repeat("e", 64), CheckpointSHA256: strings.Repeat("f", 64),
		NativeDatabase: strings.Repeat("a", 64), NativePublisher: strings.Repeat("b", 64), ProgramWireHash: strings.Repeat("c", 64), WasmSHA256: "92f4efff417d8f17ba4329c46fd1bdc13c8e234137e6150ac33f62503cb212cc", Names: []string{"fixture"}}
	source, err := NewCapturedSource(point, nativeSchema(t, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func emptyFixture(t *testing.T) Source {
	t.Helper()
	source, err := NewEmptySource(EmptySource{AllocationID: strings.Repeat("7", 32), PointID: strings.Repeat("5", 32), FenceID: strings.Repeat("6", 32), PreparationOpID: "prepared-empty",
		RestoreReceiptSHA256: strings.Repeat("a", 64), BundleManifestSHA256: strings.Repeat("b", 64), SourceSealSHA256: strings.Repeat("c", 64), ConfigSHA256: strings.Repeat("d", 64), SigningRecoverySHA256: strings.Repeat("e", 64)})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func planFixture(t *testing.T, create bool) PlanSpec {
	t.Helper()
	requester, err := datainit.ParseRequester("deploy:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := NewCandidate("40960700aea4b6dd4d201539e91ff6fc27fcd96c050c92e182bf4c0240a03269", strings.Repeat("e", 64), nativeSchema(t, "default-column"))
	if err != nil {
		t.Fatal(err)
	}
	issued := time.Unix(1_700_000_000, 0).UTC()
	spec := PlanSpec{Requester: requester, Facts: testFacts(), Candidate: candidate, Review: ReviewReferences{GuestCodeSHA256: strings.Repeat("f", 64), RecoveryReleases: []string{strings.Repeat("1", 64)}}, IssuedAt: issued, ExpiresAt: issued.Add(5 * time.Minute)}
	if create {
		spec.Source = emptyFixture(t)
		spec.CreationName = "fixture"
		return spec
	}
	spec.Source = capturedFixture(t)
	spec.Preflight = NativePreflight{ResponseSHA256: strings.Repeat("7", 64), TokenSHA256: strings.Repeat("8", 64), PrivateTokenRef: strings.Repeat("9", 32)}
	return spec
}

func TestPublicationPlansKeepFreshCreationDisjoint(t *testing.T) {
	for _, create := range []bool{true, false} {
		assertPlanRoundTrip(t, planFixture(t, create))
	}
}

func assertPlanRoundTrip(t *testing.T, spec PlanSpec) {
	t.Helper()
	p, err := NewPlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePlan(raw, spec.Source.Schema(), spec.Candidate.Schema())
	if err != nil || decoded.ID() != p.ID() || !slices.Equal(decoded.Requirements(), p.Requirements()) {
		t.Fatal("private record changed derived review", err)
	}
	if spec.Source.Kind() == CreateSource && (strings.Contains(string(raw), "native_database") || strings.Contains(string(raw), "coverage_contract") || strings.Contains(string(raw), "replica")) {
		t.Fatal("fresh server record invented native history")
	}
}

func TestPublicationMetadataCannotDowngradeRequiredDataChecks(t *testing.T) {
	spec := planFixture(t, false)
	p, err := NewPlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.Requirements(), spacetimedb.VerifyDefaultProjection) || !slices.Contains(p.Requirements(), spacetimedb.ReviewGuestCode) {
		t.Fatal("actual default/effect requirements lost")
	}
	spec.Review.RecoveryReleases[0] = strings.Repeat("2", 64)
	refs := p.Review()
	refs.RecoveryReleases[0] = strings.Repeat("3", 64)
	requirements := p.Requirements()
	requirements[0] = "reviewed"
	point, _ := p.Source().Captured()
	point.Names[0] = "changed"
	kept, _ := p.Source().Captured()
	if p.Review().RecoveryReleases[0] != strings.Repeat("1", 64) || !slices.Contains(p.Requirements(), spacetimedb.ReviewGuestCode) || kept.Names[0] != "fixture" {
		t.Fatal("mutable caller evidence changed retained plan")
	}
}

func TestCapturedContractIsBoundToCurrentArtifactAndSchema(t *testing.T) {
	point, _ := capturedFixture(t).Captured()
	point.WasmSHA256 = "f9d9510db3d75c78cd77ff4816f5af4b97c0af0499cad6c6ec0ebf4072d5c811"
	if _, err := NewCapturedSource(point, nativeSchema(t, "v1")); !errors.Is(err, ErrRecord) {
		t.Fatal("old program fallback accepted", err)
	}
	point.WasmSHA256 = "92f4efff417d8f17ba4329c46fd1bdc13c8e234137e6150ac33f62503cb212cc"
	if _, err := NewCapturedSource(point, nativeSchema(t, "observer-hooks")); !errors.Is(err, ErrRecord) {
		t.Fatal("unqualified hook/schema inherited finite contract", err)
	}
}

func TestReviewFreshnessRejectsRebindingAndExpiry(t *testing.T) {
	spec := planFixture(t, false)
	p, err := NewPlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.CheckFreshness(spec.Requester, spec.Facts, spec.Source, spec.IssuedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	facts := spec.Facts
	facts.Generation++
	if err = p.CheckFreshness(spec.Requester, facts, spec.Source, spec.IssuedAt.Add(time.Minute)); !errors.Is(err, ErrStale) {
		t.Fatal("generation rebound", err)
	}
	if err = p.CheckFreshness(spec.Requester, spec.Facts, spec.Source, spec.ExpiresAt); !errors.Is(err, ErrStale) {
		t.Fatal("expired review accepted", err)
	}
	point, _ := spec.Source.Captured()
	point.SourceSealSHA256 = strings.Repeat("e", 64)
	changed, err := NewCapturedSource(point, spec.Source.Schema())
	if err != nil {
		t.Fatal(err)
	}
	if err = p.CheckFreshness(spec.Requester, spec.Facts, changed, spec.IssuedAt.Add(time.Minute)); !errors.Is(err, ErrStale) {
		t.Fatal("source point rebound", err)
	}
}
