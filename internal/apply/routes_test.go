package apply

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

type rejectCandidate struct{ calls int }

func (v *rejectCandidate) Validate(context.Context, string) error      { v.calls++; return injected }
func (*rejectCandidate) Adapt(context.Context, string) ([]byte, error) { return nil, injected }

type rejectReload struct{ calls int }

func (r *rejectReload) Reload(context.Context) error { r.calls++; return injected }

func TestGenerationRoutesDelegatesValidatedCandidateAndRestore(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "gen-0"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gen-0", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	validator, reloader := &rejectCandidate{}, &rejectReload{}
	manager, err := caddy.NewManager(root, validator, reloader)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	baseline, err := manager.Observe()
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string) []byte {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	pol, err := policy.Parse(bytes.ReplaceAll(read("../policy/testdata/operator.toml"), []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(bytes.ReplaceAll(read("../spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello")))
	if err != nil {
		t.Fatal(err)
	}
	reads, sites := 0, 0
	routes := GenerationRoutes{Manager: manager,
		Main: func(context.Context) ([]byte, error) {
			reads++
			return []byte("import " + root + "/current/*.caddy\n"), nil
		},
		Site: func(d policy.Desired, port target.Port) (caddy.Site, error) {
			sites++
			if d.Name != app.Name {
				t.Fatal("wrong app")
			}
			return caddy.NewSite(app, pol, spec.Port(port))
		},
	}
	r := newRig(t, false)
	if _, err := routes.Publish(context.Background(), baseline, r.plan, r.desired); !errors.Is(err, injected) {
		t.Fatalf("publish %v", err)
	}
	if err := routes.Restore(context.Background(), baseline, baseline); !errors.Is(err, injected) {
		t.Fatalf("restore %v", err)
	}
	if reads != 2 || sites != 1 || validator.calls != 2 || reloader.calls != 0 {
		t.Fatalf("reads/sites/validation/reload %d/%d/%d/%d", reads, sites, validator.calls, reloader.calls)
	}
}
func TestGenerationRoutesRequiresAdapters(t *testing.T) {
	var routes GenerationRoutes
	r := newRig(t, false)
	if _, err := routes.Publish(context.Background(), caddy.State{}, r.plan, r.desired); err == nil {
		t.Fatal("missing route adapters accepted")
	}
	if err := routes.Restore(context.Background(), caddy.State{}, caddy.State{}); err == nil {
		t.Fatal("missing restore adapters accepted")
	}
}
