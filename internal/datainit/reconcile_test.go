package datainit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInitializationReconcileInspectsWithoutReplayingEffects(t *testing.T) {
	for _, boundary := range []string{"intent", "mutation_completed", "unknown"} {
		t.Run(boundary, func(t *testing.T) {
			s, j, request := initFixture(t)
			ctx := context.Background()
			p, err := s.Plan(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			j.fail = boundary
			if boundary == "unknown" {
				j.fail = "intent"
			}
			_, err = s.Apply(ctx, request.App, p.ID)
			if !errors.Is(err, ErrRecovery) {
				t.Fatal("missing interruption", err)
			}
			j.fail = ""
			if boundary == "unknown" {
				if err = os.WriteFile(filepath.Join(string(p.Database.Root), p.Database.RelativeDirectory, string(p.Database.Filename)), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.Quiesce = func(context.Context, Plan) (func(), error) { t.Fatal("reconcile quiesced again"); return nil, nil }
			s.PrepareRestorePoint = func(context.Context, Plan, Operation) (VerifiedRestorePoint, error) {
				t.Fatal("reconcile uploaded again")
				return VerifiedRestorePoint{}, nil
			}
			op, err := s.Reconcile(ctx, request.App, p.ID)
			if boundary == "unknown" {
				if !errors.Is(err, ErrRecovery) || op.State != "intent" {
					t.Fatal("unknown live state settled", err)
				}
				return
			}
			want := "not_initialized"
			if boundary == "mutation_completed" {
				want = "succeeded"
			}
			if err != nil || op.State != want {
				t.Fatal("inspection did not settle", op, err)
			}
		})
	}
}
