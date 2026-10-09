package enroll

import (
	"context"
	"errors"
	"fmt"
)

type Phase string

const (
	Applying Phase = "applying"
	Enrolled Phase = "enrolled"
	Undoing  Phase = "undoing"
	Removed  Phase = "removed"
)

type Journal struct {
	Phase   Phase           `json:"phase"`
	Intents map[string]bool `json:"intents"`
}
type Step struct {
	Name  string
	Check func(context.Context) (bool, error)
	Apply func(context.Context) error
	Undo  func(context.Context) error
}
type Store interface{ Save(Journal) error }

// Intent is durable before any mutation. Check reconciles a previous unknown
// outcome; it must reject drift rather than adopting unrelated resources.
func Apply(ctx context.Context, store Store, j *Journal, steps []Step) error {
	if j.Phase == Undoing || j.Phase == Removed {
		return errors.New("enrollment is being removed; finish undo first")
	}
	if j.Intents == nil {
		j.Intents = map[string]bool{}
	}
	j.Phase = Applying
	if err := store.Save(*j); err != nil {
		return err
	}
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !j.Intents[step.Name] {
			j.Intents[step.Name] = true
			if err := store.Save(*j); err != nil {
				return err
			}
		}
		ok, err := step.Check(ctx)
		if err != nil {
			return fmt.Errorf("enrollment check %s: %w", step.Name, err)
		}
		if !ok {
			if err := step.Apply(ctx); err != nil {
				return fmt.Errorf("enrollment apply %s: %w", step.Name, err)
			}
			ok, err = step.Check(ctx)
			if err != nil || !ok {
				return fmt.Errorf("enrollment step %s did not converge: %w", step.Name, err)
			}
		}
	}
	j.Phase = Enrolled
	return store.Save(*j)
}
func Undo(ctx context.Context, store Store, j *Journal, steps []Step) error {
	j.Phase = Undoing
	if err := store.Save(*j); err != nil {
		return err
	}
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if !j.Intents[step.Name] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step.Undo(ctx); err != nil {
			return fmt.Errorf("enrollment undo %s: %w", step.Name, err)
		}
		delete(j.Intents, step.Name)
		if err := store.Save(*j); err != nil {
			return err
		}
	}
	j.Phase = Removed
	return store.Save(*j)
}
