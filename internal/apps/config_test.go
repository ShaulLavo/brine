package apps

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func configFixture(t *testing.T) (Service, *fakeStore) {
	t.Helper()
	s, db, snap := fixture(t)
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Parse(bytes.ReplaceAll(raw, []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if err != nil {
		t.Fatal(err)
	}
	s.LoadPolicy = func(context.Context) (policy.Policy, error) { return p, nil }
	d := db.state.Releases[0].Desired
	db.desired["current-plan"] = d
	db.plans["current-plan"] = plan.Plan{App: "hello", Target: snap.Identity, Image: db.state.Releases[0].Image}
	return s, db
}

func TestConfigEdits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edits   []Edit
		refused bool
	}{
		{"environment", []Edit{{Key: "environment.API_KEY", Value: "PLANTED_PRIVATE_SETTING"}}, false},
		{"unset", []Edit{{Key: "environment.API_KEY", Action: "unset"}}, false},
		{"memory", []Edit{{Key: "resources.memory_mb", Value: "256"}}, false},
		{"pids", []Edit{{Key: "resources.pids_limit", Value: "64"}}, false},
		{"health", []Edit{{Key: "health.path", Value: "/ready"}, {Key: "health.expected_status", Value: "204"}, {Key: "health.startup_deadline_seconds", Value: "40"}, {Key: "health.timeout_seconds", Value: "3"}}, false},
		{"domain_add", []Edit{{Key: "domains", Value: "other.example.net", Action: "add"}}, false},
		{"domain_remove", []Edit{{Key: "domains", Value: "hello.example.com", Action: "remove"}}, true},
		{"domain_denied", []Edit{{Key: "domains", Value: "evil.invalid", Action: "add"}}, true},
		{"memory_denied", []Edit{{Key: "resources.memory_mb", Value: "513"}}, true},
		{"pids_denied", []Edit{{Key: "resources.pids_limit", Value: "129"}}, true},
		{"health_invalid", []Edit{{Key: "health.path", Value: "//unsafe"}}, true},
		{"health_type", []Edit{{Key: "health.expected_status", Value: "two"}}, true},
		{"unset_invalid", []Edit{{Key: "environment.9BAD", Action: "unset"}}, true},
		{"env_invalid", []Edit{{Key: "environment.9BAD", Value: "PLANTED_PRIVATE_SETTING"}}, true},
		{"env_nul", []Edit{{Key: "environment.API_KEY", Value: "PLANTED_PRIVATE_SETTING\x00"}}, true},
		{"secret_denied", []Edit{{Key: "secrets.TOKEN", Value: "other-app-ref"}}, true},
		{"arbitrary", []Edit{{Key: "image", Value: "PLANTED_PRIVATE_SETTING"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := configFixture(t)
			got, err := s.ConfigSet(context.Background(), "hello", tc.edits)
			if tc.refused {
				if err == nil || len(db.saved) != 0 {
					t.Fatalf("expected refusal, saved=%d err=%v", len(db.saved), err)
				}
				if strings.Contains(err.Error(), "PLANTED_PRIVATE_SETTING") {
					t.Fatal("error leaked")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "PLANTED_PRIVATE_SETTING") {
				t.Fatal("result leaked")
			}
			if len(db.saved) != 1 {
				t.Fatal("plan not saved")
			}
			raw, _ = json.Marshal(db.saved[0])
			if strings.Contains(string(raw), "PLANTED_PRIVATE_SETTING") {
				t.Fatal("plan leaked")
			}
			if tc.name == "environment" && db.desired[got.PlanID].Environment[0].Value != "PLANTED_PRIVATE_SETTING" {
				t.Fatal("desired value not retained")
			}
		})
	}
}

func TestLifecyclePlanShapes(t *testing.T) {
	for _, action := range []plan.ChangeKind{plan.RestartApp, plan.StopApp, plan.StartApp} {
		t.Run(string(action), func(t *testing.T) {
			s, db := configFixture(t)
			got, err := s.Lifecycle(context.Background(), "hello", action)
			if err != nil {
				t.Fatal(err)
			}
			p := db.saved[0]
			if got.Kind != plan.Update || p.Lifecycle != action || len(p.Changes) != 1 || p.Changes[0].Kind != action || p.Diff != nil {
				t.Fatalf("bad lifecycle plan: %#v", p)
			}
		})
	}
}

func TestConfigCanRepairConfigurationAfterPolicyCeilingDrops(t *testing.T) {
	s, db := configFixture(t)
	old := db.desired["current-plan"]
	old.Resources.MemoryMB = 1024
	db.desired["current-plan"] = old
	db.state.Releases[0].Desired = old
	got, err := s.ConfigSet(context.Background(), "hello", []Edit{{Key: "resources.memory_mb", Value: "256"}})
	if err != nil || db.desired[got.PlanID].Resources.MemoryMB != 256 {
		t.Fatal(got, err)
	}
}

func TestLifecycleRejectsHiddenConfigurationChange(t *testing.T) {
	s, _ := configFixture(t)
	in, _, err := s.currentInput(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	in.Desired.Resources.MemoryMB--
	if _, err := plan.BuildLifecycle(in, plan.StartApp); err == nil {
		t.Fatal("lifecycle changed configuration")
	}
}

func TestConfigBindsNewestSecretButLifecycleKeepsCommittedVersion(t *testing.T) {
	s, db := configFixture(t)
	snap := s.Inventory.(inventory).snapshot
	d := db.desired["current-plan"]
	d.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "hello-token"}}
	db.desired["current-plan"] = d
	db.state.Releases[0].Desired = d
	db.state.Releases[0].Secrets = []plan.SecretBinding{{Environment: "TOKEN", Reference: "hello-token", VersionName: "brine.hello.hello-token.v1", ID: "id-one"}}
	(*snap.Apps.Value)[0].Secrets = target.Known([]target.Secret{{Name: "brine.hello.hello-token.v1", ID: "id-one"}, {Name: "brine.hello.hello-token.v2", ID: "id-two"}})
	got, err := s.Lifecycle(context.Background(), "hello", plan.RestartApp)
	if err != nil {
		t.Fatal(err)
	}
	p := db.saved[len(db.saved)-1]
	if p.Kind != plan.Update || len(p.Secrets) != 1 || p.Secrets[0].VersionName != "brine.hello.hello-token.v1" {
		t.Fatal(p)
	}
	got, err = s.ConfigSet(context.Background(), "hello", []Edit{{Key: "secrets.TOKEN", Value: "hello-token"}})
	if err != nil {
		t.Fatal(err)
	}
	p = db.saved[len(db.saved)-1]
	if p.Kind != plan.Update || len(p.Secrets) != 1 || p.Secrets[0].VersionName != "brine.hello.hello-token.v2" || got.Diff == nil || len(got.Diff.Secrets) != 1 {
		t.Fatal(p)
	}
}

func TestConfigDomainRemovalAndUnset(t *testing.T) {
	s, db := configFixture(t)
	d := db.desired["current-plan"]
	d.Domains = append(d.Domains, spec.Domain("other.example.net"))
	d.Environment = []policy.Environment{{Name: "REMOVE", Value: "PLANTED_PRIVATE_SETTING"}}
	db.desired["current-plan"] = d
	db.state.Releases[0].Desired = d
	got, err := s.ConfigSet(context.Background(), "hello", []Edit{{Key: "domains", Action: "remove", Value: "other.example.net"}, {Key: "environment.REMOVE", Action: "unset"}})
	if err != nil {
		t.Fatal(err)
	}
	edited := db.desired[got.PlanID]
	if len(edited.Domains) != 1 || len(edited.Environment) != 0 {
		t.Fatal("edits did not take effect")
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "PLANTED_PRIVATE_SETTING") {
		t.Fatal("old setting leaked")
	}
}
