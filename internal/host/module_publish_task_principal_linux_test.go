//go:build linux

package host

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

func publicationPrincipalPlanFixture(t *testing.T) datapublish.Plan {
	t.Helper()
	requester, err := datainit.ParseRequester("deploy:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	artifact := publicationArtifactFixture(t)
	raw, err := os.ReadFile("../spacetimedb/testdata/descriptor-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := spacetimedb.DecodeDescriptorV10(raw, spacetimedb.DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := datapublish.NewCandidate(string(artifact.WasmSHA256()), string(artifact.ProgramWireHash()), schema)
	if err != nil {
		t.Fatal(err)
	}
	source, err := datapublish.NewEmptySource(datapublish.EmptySource{AllocationID: strings.Repeat("7", 32), PointID: strings.Repeat("5", 32), FenceID: strings.Repeat("6", 32), PreparationOpID: "prepared-empty", RestoreReceiptSHA256: strings.Repeat("a", 64), BundleManifestSHA256: strings.Repeat("b", 64), SourceSealSHA256: strings.Repeat("c", 64), ConfigSHA256: strings.Repeat("d", 64), SigningRecoverySHA256: strings.Repeat("e", 64)})
	if err != nil {
		t.Fatal(err)
	}
	secret := func(ref string) data.EngineSecretVersion {
		return data.EngineSecretVersion{Reference: ref, Version: 1, SHA256: strings.Repeat("e", 64)}
	}
	facts := datapublish.BindingFacts{App: "game", IncarnationID: strings.Repeat("1", 32), DatabaseID: strings.Repeat("2", 32), BindingID: strings.Repeat("3", 32), EpochID: strings.Repeat("4", 32), Generation: 1, TargetSHA256: strings.Repeat("a", 64), PolicySHA256: strings.Repeat("b", 64), DesiredSHA256: strings.Repeat("c", 64), ConfigSHA256: strings.Repeat("d", 64), SecretVersions: datapublish.SecretReferences{Publisher: secret("publisher"), SigningPublic: secret("signing-public"), SigningPrivate: secret("signing-private"), AuthPolicy: secret("auth-policy"), RecoveryPassword: secret("recovery-password")}}
	now := time.Now().UTC()
	reviewed, err := datapublish.NewPlan(datapublish.PlanSpec{Requester: requester, Facts: facts, Source: source, Candidate: candidate, Review: datapublish.ReviewReferences{GuestCodeSHA256: string(artifact.WasmSHA256()), Recovery: datapublish.NoPriorReleaseReview()}, CreationName: "fixture", IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return reviewed
}

func TestPublicationTaskPrincipalComesFromMatchingAcceptedReview(t *testing.T) {
	reviewed := publicationPrincipalPlanFixture(t)
	for _, kind := range []ops.Kind{ops.ModulePublishApply, ops.ModulePublishResolve} {
		checkPublicationTaskPrincipal(t, reviewed, kind)
	}
}

func checkPublicationTaskPrincipal(t *testing.T, reviewed datapublish.Plan, kind ops.Kind) {
	t.Helper()
	job := ops.Operation{ID: "accepted-task", Kind: kind, State: ops.Preflight, App: reviewed.Facts().App, SecretRef: reviewed.ID(), Requester: reviewed.Requester().String()}
	for _, ambient := range []string{"", "unrelated-operator"} {
		base := Service{Requester: ambient}
		scoped, err := publicationTaskService(base, job, reviewed)
		if err != nil || scoped.Requester != job.Requester || base.Requester != ambient {
			t.Fatal("accepted principal did not stay task-local", err)
		}
	}
	changed := []ops.Operation{job, job, job, job, job}
	changed[0].Requester = "deploy:" + strings.Repeat("b", 64)
	changed[1].SecretRef = "sha256:" + strings.Repeat("c", 64)
	changed[2].App = "foreign"
	changed[3].State = ops.Queued
	changed[4].Kind = ops.NativePrepare
	for _, foreign := range changed {
		if _, err := publicationTaskService(Service{Requester: job.Requester}, foreign, reviewed); !errors.Is(err, datapublish.ErrRecord) {
			t.Fatal("ambient principal hid a foreign job/review binding", err)
		}
	}
	if _, err := publicationTaskService(Service{Requester: job.Requester}, job, datapublish.Plan{}); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("empty review supplied authority", err)
	}
}
