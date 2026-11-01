//go:build linux

package store

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spec"
)

func TestAppNameOperationAdmission(t *testing.T) {
	base, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "a0", "a-b", strings.Repeat("a", 62) + "0"} {
		t.Run(name, func(t *testing.T) {
			checkAppNameOperationAdmission(t, base, name)
		})
	}
}

func checkAppNameOperationAdmission(t *testing.T, base []byte, name string) {
	t.Helper()
	appData := bytes.Replace(base, []byte(`name = "hello"`), []byte(`name = "`+name+`"`), 1)
	app, err := spec.Parse(appData)
	if err != nil {
		t.Fatal(err)
	}
	s := openTest(t)
	in := fixture(t)
	in.Desired.Name = app.Name
	planned, err := plan.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := s.SavePlan(t.Context(), planned, in.Desired)
	if err != nil {
		t.Fatal(err)
	}
	if planID != planned.Hash {
		t.Fatalf("plan ID = %q, planned hash = %q", planID, planned.Hash)
	}
	op, existing, err := s.CreateOperation(t.Context(), ops.Intent{Kind: ops.Deploy, PlanID: planID}, "requester", "name-"+name)
	if err != nil {
		t.Fatal(err)
	}
	if existing {
		t.Fatal("first operation was reported as existing")
	}
	if op.App != name || op.PlanID != planID || op.State != ops.Queued || !ops.ValidOperation(op) {
		t.Fatalf("operation = %#v", op)
	}
	got, err := s.GetOperation(t.Context(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.App != name || got.PlanID != planID || got.State != ops.Queued || !ops.ValidOperation(got) {
		t.Fatalf("stored operation = %#v", got)
	}
}
