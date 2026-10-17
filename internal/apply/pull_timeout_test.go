package apply

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeploymentPullStepBudgets(t *testing.T) {
	for _, step := range []string{"pull_image", "frontend_pull", "verify_image", "ensure_secrets", "stage_unit", "publish_route"} {
		t.Run(step, func(t *testing.T) { checkDeploymentStepBudget(t, step, 0) })
		t.Run(step+"/explicit", func(t *testing.T) { checkDeploymentStepBudget(t, step, 3*time.Second) })
	}
}

func checkDeploymentStepBudget(t *testing.T, step string, explicit time.Duration) {
	t.Helper()
	r := newRig(t, false)
	r.executor.EffectTimeout = explicit
	x := &execution{executor: &r.executor, id: "operation-1", state: Preparing}
	want := time.Minute
	if step == "pull_image" || step == "frontend_pull" {
		want = 15 * time.Minute
	}
	if explicit > 0 {
		want = explicit
	}
	started := time.Now()
	err := x.step(t.Context(), step, Preparing, "fixture_failed", func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || deadline.Before(started.Add(want)) || deadline.After(time.Now().Add(want)) {
			t.Errorf("%s deadline = %v, want %s", step, deadline, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentPullStepPreservesEarlierParentAndCancellation(t *testing.T) {
	for _, step := range []string{"pull_image", "frontend_pull"} {
		t.Run(step, func(t *testing.T) { checkPullStepParent(t, step) })
	}
}

func checkPullStepParent(t *testing.T, step string) {
	t.Helper()
	r := newRig(t, false)
	r.executor.EffectTimeout = 0
	x := &execution{executor: &r.executor, id: "operation-1", state: Preparing}
	deadline := time.Now().Add(time.Second)
	parent, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	err := x.step(parent, step, Preparing, "fixture_failed", func(ctx context.Context) error {
		got, ok := ctx.Deadline()
		if !ok || !got.Equal(deadline) {
			t.Errorf("pull extended parent deadline: %v", got)
		}
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pull = %v", err)
	}
}
