package apps

import (
	"bytes"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

var (
	tokenV1 = target.Secret{Name: "brine.hello.token.v1", ID: "fixture-token-001"}
	tokenV2 = target.Secret{Name: "brine.hello.token.v2", ID: "fixture-token-002"}
	tokenV3 = target.Secret{Name: "brine.hello.token.v3", ID: "fixture-token-003"}
	keyV1   = target.Secret{Name: "brine.hello.key.v1", ID: "fixture-key-001"}
	keyV2   = target.Secret{Name: "brine.hello.key.v2", ID: "fixture-key-002"}
)

type secretHistoryRelease struct {
	id      string
	desired policy.Desired
	image   plan.Image
	visible []target.Secret
}

// secretHistory commits older, previous and current releases through the real
// store. Each release binds the newest version visible when it was planned;
// the live inventory retains every version.
func secretHistory(t *testing.T, refs []policy.Secret) (Service, *store.Store, target.Snapshot) {
	t.Helper()
	service, fake, snap := fixture(t)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	ready, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := target.Decode(ready)
	if err != nil {
		t.Fatal(err)
	}
	current := fake.state.Releases[0]
	old, newer := fake.desired["old-plan"], current.Desired
	old.Secrets, newer.Secrets = refs, refs
	for _, r := range []secretHistoryRelease{
		{"older", old, fake.previous.Image, []target.Secret{tokenV1, keyV1}},
		{"previous", old, fake.previous.Image, []target.Secret{tokenV1, tokenV2, keyV1}},
		{"current", newer, current.Image, []target.Secret{tokenV1, tokenV2, tokenV3, keyV1, keyV2}},
	} {
		commitSecretRelease(t, db, empty, current, snap.CaddyConfig.Value.Generation, r)
	}
	(*snap.Apps.Value)[0].Secrets = target.Known([]target.Secret{tokenV1, tokenV2, tokenV3, keyV1, keyV2})
	service.Store = db
	service.Inventory = inventory{snap}
	return service, db, snap
}

func commitSecretRelease(t *testing.T, db *store.Store, base target.Snapshot, current plan.CurrentRelease, generation uint64, r secretHistoryRelease) {
	t.Helper()
	base.Apps = target.Known([]target.App{{Name: "hello", Image: target.Observation[target.Image]{Status: target.Absent}, AllocatedHostPort: target.Observation[target.Port]{Status: target.Absent}, QuadletUnits: target.Known([]target.Unit{}), Secrets: target.Known(r.visible), UDPPublication: target.Observation[target.UDPPublication]{Status: target.Absent}}})
	p, err := plan.Build(plan.Input{Desired: r.desired, Snapshot: base, Image: r.image, State: plan.BrineState{Target: base.Identity, Generation: *base.Generation.Value, Releases: []plan.CurrentRelease{}}})
	if err != nil || len(p.Conflicts) != 0 {
		t.Fatal(p.Conflicts, err)
	}
	id, err := db.SavePlan(t.Context(), p, r.desired)
	if err != nil {
		t.Fatal(err)
	}
	release := ops.Release{ID: r.id, PlanID: id, Image: r.image, HostPort: p.HostPort, Secrets: p.Secrets, Units: current.Units, CaddyFile: current.CaddyFile, CaddyGeneration: generation}
	if err := db.CommitRelease(t.Context(), "hello", release); err != nil {
		t.Fatal(err)
	}
}

func bindingNames(bindings []plan.SecretBinding) []string {
	names := make([]string, 0, len(bindings))
	for _, b := range bindings {
		names = append(names, b.Environment+"="+b.VersionName+"#"+b.ID)
	}
	slices.Sort(names)
	return names
}

// rebuildAtApply repeats the executor and host admission freshness check:
// restore the plan's recorded inputs over fresh facts, rebuild, compare bytes.
func rebuildAtApply(t *testing.T, db *store.Store, snap target.Snapshot, stored plan.Plan, desired policy.Desired) bool {
	t.Helper()
	state, err := db.LoadBrineState(t.Context(), snap.Identity, *snap.Generation.Value)
	if err != nil {
		t.Fatal(err)
	}
	input, _, err := plan.RebuildInput(plan.Input{Desired: desired, Snapshot: snap, Image: stored.Image, State: state}, stored, []string{"rollback-operation"})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := plan.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	original, err := stored.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := fresh.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(original, rebuilt)
}

func TestRollbackBindsRecordedSecretVersions(t *testing.T) {
	refs := []policy.Secret{{Name: "API_KEY", Reference: "key"}, {Name: "TOKEN", Reference: "token"}}
	for _, tc := range []struct {
		release string
		want    []string
	}{
		{"", []string{"API_KEY=" + keyV1.Name + "#" + keyV1.ID, "TOKEN=" + tokenV2.Name + "#" + tokenV2.ID}},
		{"older", []string{"API_KEY=" + keyV1.Name + "#" + keyV1.ID, "TOKEN=" + tokenV1.Name + "#" + tokenV1.ID}},
	} {
		t.Run("release_"+expectedRelease(tc.release), func(t *testing.T) {
			service, db, snap := secretHistory(t, refs)
			recorded, err := db.ReleaseByID(t.Context(), "hello", expectedRelease(tc.release))
			if err != nil || !slices.Equal(bindingNames(recorded.Secrets), tc.want) {
				t.Fatal("fixture history", bindingNames(recorded.Secrets), err)
			}
			planned, err := service.Rollback(t.Context(), "hello", tc.release)
			if err != nil {
				t.Fatal(err)
			}
			stored, desired, err := db.LoadPlan(t.Context(), planned.PlanID)
			if err != nil {
				t.Fatal(err)
			}
			if got := bindingNames(stored.Secrets); planned.Kind != plan.Update || !slices.Equal(got, tc.want) {
				t.Fatalf("rollback to %s bound %v, want recorded %v (kind %s, conflicts %v)", recorded.ID, got, tc.want, planned.Kind, planned.Conflicts)
			}
			if !rebuildAtApply(t, db, snap, stored, desired) {
				t.Fatal("rollback plan drifts when rebuilt at apply")
			}
		})
	}
}

func TestRollbackMissingRecordedSecretVersionConflicts(t *testing.T) {
	refs := []policy.Secret{{Name: "API_KEY", Reference: "key"}, {Name: "TOKEN", Reference: "token"}}
	for name, replacement := range map[string][]target.Secret{
		"pruned":      {tokenV2, tokenV3, keyV1, keyV2},
		"replaced_id": {{Name: tokenV1.Name, ID: "fixture-token-other"}, tokenV2, tokenV3, keyV1, keyV2},
	} {
		t.Run(name, func(t *testing.T) {
			service, db, snap := secretHistory(t, refs)
			(*snap.Apps.Value)[0].Secrets = target.Known(replacement)
			service.Inventory = inventory{snap}
			planned, err := service.Rollback(t.Context(), "hello", "older")
			if err != nil {
				t.Fatal(err)
			}
			stored, _, err := db.LoadPlan(t.Context(), planned.PlanID)
			if err != nil {
				t.Fatal(err)
			}
			want := []plan.Diagnostic{{Code: plan.SecretMissing, Field: "secrets.TOKEN"}}
			if planned.Kind != plan.Conflict || !reflect.DeepEqual(planned.Conflicts, want) || len(stored.Changes) != 0 {
				t.Fatalf("missing recorded version planned %s %v changes %d", planned.Kind, planned.Conflicts, len(stored.Changes))
			}
			for _, b := range stored.Secrets {
				if b.Environment == "TOKEN" {
					t.Fatalf("substituted %s for the missing recorded version", b.VersionName)
				}
			}
			head, err := db.CurrentRelease(t.Context(), "hello")
			if err != nil || head.ID != "current" {
				t.Fatal(head.ID, err)
			}
		})
	}
}

func TestRollbackWithoutSecretsKeepsPlannerOutput(t *testing.T) {
	service, db, snap := secretHistory(t, nil)
	planned, err := service.Rollback(t.Context(), "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	stored, desired, err := db.LoadPlan(t.Context(), planned.PlanID)
	if err != nil || planned.Kind != plan.Update || len(stored.Secrets) != 0 {
		t.Fatal(planned, err)
	}
	state, err := db.LoadBrineState(t.Context(), snap.Identity, *snap.Generation.Value)
	if err != nil {
		t.Fatal(err)
	}
	unpinned, err := plan.Build(plan.Input{Desired: desired, Snapshot: snap, Image: stored.Image, State: state})
	if err != nil {
		t.Fatal(err)
	}
	if unpinned.Hash != stored.Hash || strings.Contains(string(mustCanonical(t, stored)), "secret_exact") {
		t.Fatal("no-secret rollback plan changed")
	}
	if !rebuildAtApply(t, db, snap, stored, desired) {
		t.Fatal("no-secret rollback plan drifts when rebuilt at apply")
	}
}

func mustCanonical(t *testing.T, p plan.Plan) []byte {
	t.Helper()
	b, err := p.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
