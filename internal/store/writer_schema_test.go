//go:build linux

package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestReadWriterSchemaRequiresCurrentCommittedRelease(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	req := dataRequest()
	req.App = "hello"
	reserved, err := s.ReserveDatabase(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadWriterSchema(ctx, reserved.Database.IncarnationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("uncommitted schema: %v", err)
	}
	in := fixture(t)
	in.Desired.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
	in.Desired.Databases = []data.Database{req.Database}
	in.Desired.SchemaCompatibility = []data.SchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{data.EmptyMarker}}}
	// Store-only historical fixture: no deployment or startup is performed.
	p, err := plan.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.SavePlan(ctx, p, in.Desired)
	if err != nil {
		t.Fatal(err)
	}
	release := Release{ID: "writer-release", PlanID: id, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: []target.Unit{{Name: "hello.container", Hash: "sha256:" + strings.Repeat("b", 64)}}, CaddyFile: target.CaddyFile{Name: "hello.caddy", Hash: "sha256:" + strings.Repeat("c", 64)}}
	if err = s.CommitRelease(ctx, "hello", release); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	observed, err := ro.ReadWriterSchema(ctx, reserved.Database.IncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.ReleaseID != release.ID || len(observed.Bindings) != 1 || observed.Bindings[0] != reserved.Database || !reflect.DeepEqual(observed.Desired.SchemaCompatibility, in.Desired.SchemaCompatibility) {
		t.Fatalf("wrong declaration: %+v", observed)
	}
	if _, err = s.HoldDataFence(ctx, reserved.Database.DatabaseID, "migration"); err != nil {
		t.Fatal(err)
	}
	if _, err = ro.ReadWriterSchema(ctx, reserved.Database.IncarnationID); !errors.Is(err, ErrConflict) {
		t.Fatalf("held fence allowed: %v", err)
	}
}
