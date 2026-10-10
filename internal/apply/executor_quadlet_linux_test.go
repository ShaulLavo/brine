//go:build linux

package apply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/quadlet"
)

type unitValidatorFunc func(context.Context, quadlet.Candidate) error

func (f unitValidatorFunc) Validate(ctx context.Context, c quadlet.Candidate) error { return f(ctx, c) }

type diskUnits struct {
	r       *rig
	manager *quadlet.Manager
}

func (u diskUnits) VerifyCurrent(ctx context.Context, name string, hashes ...string) error {
	return u.manager.VerifyCurrent(ctx, name, hashes...)
}
func (u diskUnits) Stage(ctx context.Context, unit quadlet.Unit) error {
	if err := u.r.hit("stage_unit"); err != nil {
		return err
	}
	return u.manager.Stage(ctx, unit)
}
func (u diskUnits) Install(ctx context.Context, unit quadlet.Unit, old string) error {
	if u.r.active {
		panic("install while writer active")
	}
	if err := u.r.hit("install_unit"); err != nil {
		return err
	}
	return u.manager.Install(ctx, unit, old)
}
func (u diskUnits) Rollback(ctx context.Context, name, installed, previous string) error {
	if u.r.active {
		panic("restore while writer active")
	}
	if err := u.r.hit("rollback_unit"); err != nil {
		return err
	}
	return u.manager.Rollback(ctx, name, installed, previous)
}

func TestInstallFailureBeforeRetentionRestartsAndProvesPreviousRelease(t *testing.T) {
	r := newRig(t, true)
	// Real fsync needs the production budget, not the fake rig's one-second deadline.
	r.executor.EffectTimeout = 0
	old, err := quadlet.Render(r.oldDesired, r.oldPlan, *r.oldPlan.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	// Bind the observed and committed fixture hashes to the real on-disk unit.
	for i := range r.release.Units {
		if r.release.Units[i].Name == old.Name() {
			r.release.Units[i].Hash = old.Hash()
		}
	}
	r.plan, err = plan.Build(r.facts.Input)
	if err != nil || r.plan.Kind != plan.Update {
		t.Fatalf("update plan: %v %v", r.plan.Conflicts, err)
	}
	home := t.TempDir()
	validations := 0
	manager, err := quadlet.NewManager(home, unitValidatorFunc(func(context.Context, quadlet.Candidate) error {
		validations++
		// Initial installation and candidate staging pass; publication fails
		// before the predecessor can be retained.
		if validations == 3 {
			return injected
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Install(context.Background(), old, ""); err != nil {
		t.Fatal(err)
	}
	r.executor.Units = diskUnits{r, manager}
	failure(t, r.executor.Run(context.Background(), "operation-1", r.plan, r.desired), RolledBack, "install_unit")
	if !r.active || r.committed {
		t.Fatalf("previous active=%v, candidate committed=%v", r.active, r.committed)
	}
	activePath := filepath.Join(home, quadlet.ActiveDirectory, old.Name())
	data, err := os.ReadFile(activePath)
	if err != nil || string(data) != string(old.Bytes()) {
		t.Fatalf("previous bytes changed: %v", err)
	}
	if _, err := os.Stat(activePath + ".brine-prev"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected retained predecessor: %v", err)
	}
	checks := 0
	for _, effect := range r.effects {
		if effect == "rollback_check" {
			checks++
		}
	}
	if checks != 2 {
		t.Fatalf("previous direct/routed health checks: %d", checks)
	}
}
