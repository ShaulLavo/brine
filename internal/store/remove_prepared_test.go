//go:build linux

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spec"
)

func TestRemovalReadsPreparedIncarnationWithoutRelease(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	req := dataRequest()
	reserved, err := s.ReserveDatabase(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	in := fixture(t)
	in.Desired.Name = spec.Name(req.App)
	in.State, err = s.LoadBrineState(ctx, in.Snapshot.Identity, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []plan.DataIncarnation{{App: req.App, ID: string(reserved.Database.IncarnationID)}}
	if !reflect.DeepEqual(in.State.DataIncarnations, want) || len(in.State.Releases) != 0 {
		t.Fatalf("prepared ownership omitted: %+v", in.State)
	}
	if _, err = plan.BuildRemove(in); !errors.Is(err, plan.ErrPersistentData) {
		t.Fatalf("prepared app treated as absent: %v", err)
	}
	read, err := OpenReadOnly(ctx, s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	loaded, err := read.LoadBrineState(ctx, in.Snapshot.Identity, 0)
	if err != nil || !reflect.DeepEqual(loaded, in.State) {
		t.Fatalf("read-only ownership lost: %+v %v", loaded, err)
	}
}

func TestRemovalPreservesInitializationAndFence(t *testing.T) {
	for _, state := range []string{"intent", "not_initialized", "succeeded"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			s := openTest(t)
			p, reserved := storeInitPlan(t, s)
			o, err := s.ClaimInitialization(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			if state == "succeeded" {
				for _, step := range []string{"quiesced", "restore_point_intent"} {
					o, err = s.SetInitState(ctx, o, step)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err = s.SaveInitRestorePoint(ctx, o, storeInitPoint(p, o)); err != nil {
					t.Fatal(err)
				}
			}
			if state != "intent" {
				o, err = s.SetInitState(ctx, o, state)
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.ReadReplicaPermit(ctx, reserved.Database.DatabaseID)
			if err != nil {
				t.Fatal(err)
			}
			in := fixture(t)
			in.Desired.Name = spec.Name(p.Request.App)
			in.State, err = s.LoadBrineState(ctx, in.Snapshot.Identity, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = plan.BuildRemove(in); !errors.Is(err, plan.ErrPersistentData) {
				t.Fatalf("initialization %s did not require archive: %v", state, err)
			}
			after, err := s.ReadReplicaPermit(ctx, reserved.Database.DatabaseID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("removal changed fences or reservations: %v", err)
			}
			journal, exists, err := s.ReadInitOperation(ctx, p.ID)
			if err != nil || !exists || !reflect.DeepEqual(journal, o) {
				t.Fatalf("removal changed initialization journal: %v", err)
			}
			if state == "succeeded" {
				if _, err = s.LoadInitRestorePoint(ctx, o); err != nil {
					t.Fatal("removal lost restore receipt", err)
				}
			}
		})
	}
}
