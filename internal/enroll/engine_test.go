package enroll

import (
	"context"
	"errors"
	"testing"
)

type memoryStore struct {
	fail   bool
	writes int
}

func (s *memoryStore) Save(Journal) error {
	s.writes++
	if s.fail {
		return errors.New("injected save failure")
	}
	return nil
}
func TestEveryFailureConverges(t *testing.T) {
	names := []string{"ssh-layout", "mask", "packages", "user", "layout", "linger", "binary", "polkit", "caddy-tree", "caddy-import", "caddy-validate", "unmask", "caddy-enable", "caddy-start", "caddy-writable", "inventory-key", "key", "ssh-policy", "bypass"}
	for _, failAt := range names {
		for _, after := range []bool{false, true} {
			t.Run(failAt+map[bool]string{false: "-before", true: "-after"}[after], func(t *testing.T) {
				store := &memoryStore{}
				j := Journal{}
				state := map[string]bool{}
				injected := false
				steps := []Step{}
				for _, name := range names {
					steps = append(steps, Step{Name: name, Check: func(context.Context) (bool, error) { return state[name], nil }, Apply: func(context.Context) error {
						if name == failAt && !injected {
							injected = true
							if after {
								state[name] = true
							}
							return errors.New("injected")
						}
						state[name] = true
						return nil
					}, Undo: func(context.Context) error { delete(state, name); return nil }})
				}
				if Apply(context.Background(), store, &j, steps) == nil {
					t.Fatal("missing failure")
				}
				if !j.Intents[failAt] {
					t.Fatal("mutation without intent")
				}
				if err := Apply(context.Background(), store, &j, steps); err != nil {
					t.Fatal(err)
				}
				if len(state) != len(names) || j.Phase != Enrolled {
					t.Fatal("did not converge")
				}
				if err := Apply(context.Background(), store, &j, steps); err != nil {
					t.Fatal(err)
				}
				if err := Undo(context.Background(), store, &j, steps); err != nil {
					t.Fatal(err)
				}
				if len(state) != 0 || j.Phase != Removed {
					t.Fatal("undo leaked state")
				}
				if err := Undo(context.Background(), store, &j, steps); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestIntentSaveFailureDoesNotMutate(t *testing.T) {
	changed := false
	s := &memoryStore{fail: true}
	j := Journal{}
	err := Apply(context.Background(), s, &j, []Step{{Name: "fixture", Apply: func(context.Context) error { changed = true; return nil }}})
	if err == nil || changed {
		t.Fatal("mutation without durable intent")
	}
}
func TestUndoDoesNotTouchUnattemptedSteps(t *testing.T) {
	touched := false
	j := Journal{Intents: map[string]bool{}}
	s := &memoryStore{}
	if err := Undo(context.Background(), s, &j, []Step{{Name: "preexisting", Undo: func(context.Context) error { touched = true; return nil }}}); err != nil || touched {
		t.Fatal("removed an unowned resource")
	}
}

func TestEveryUndoFailureConverges(t *testing.T) {
	for _, failAt := range []int{0, 1, 2} {
		state := map[int]bool{0: true, 1: true, 2: true}
		failed := false
		steps := []Step{}
		for i := range 3 {
			steps = append(steps, Step{Name: string(rune('a' + i)), Undo: func(context.Context) error {
				delete(state, i)
				if i == failAt && !failed {
					failed = true
					return errors.New("unknown outcome")
				}
				return nil
			}})
		}
		j := Journal{Phase: Enrolled, Intents: map[string]bool{"a": true, "b": true, "c": true}}
		store := &memoryStore{}
		if Undo(context.Background(), store, &j, steps) == nil {
			t.Fatal("no injected failure")
		}
		if err := Undo(context.Background(), store, &j, steps); err != nil {
			t.Fatal(err)
		}
		if len(state) != 0 || j.Phase != Removed {
			t.Fatal("undo did not reconcile")
		}
	}
}
