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
