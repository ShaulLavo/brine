package host

import (
	"bytes"
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
)

// DataWriterStarts implements apply.WriterStarts. The apply engine calls it
// inside its journaled start step while holding the host mutation lock.
type DataWriterStarts struct{ Store *store.Store }

func (w DataWriterStarts) BindWriterStart(ctx context.Context, operationID string, p plan.Plan, d policy.Desired) error {
	if d.Stateless() {
		return nil
	}
	if w.Store == nil || len(p.DataMounts) == 0 {
		return store.ErrConflict
	}
	incarnation := p.DataMounts[0].Database.IncarnationID
	for _, mount := range p.DataMounts {
		if mount.Database.IncarnationID != incarnation {
			return store.ErrConflict
		}
	}
	raw, err := d.CanonicalBytes()
	if err != nil {
		return err
	}
	_, stored, err := w.Store.LoadPlan(ctx, p.Hash)
	if err != nil {
		return err
	}
	storedRaw, err := stored.CanonicalBytes()
	if err != nil || !bytes.Equal(raw, storedRaw) {
		return store.ErrConflict
	}
	schema, err := w.Store.CandidateWriterSchema(ctx, incarnation, d)
	if err != nil {
		return err
	}
	if !data.WriterCompatibleWithAllocations(ctx, schema.Bindings, d.SchemaCompatibility, schema.Definitions, schema.Allocations) {
		return errors.New("candidate writer schema incompatible or unknown")
	}
	return w.Store.BindWriterStart(ctx, operationID, p.Hash, incarnation, p.DesiredHash)
}
func (w DataWriterStarts) ClearWriterStart(ctx context.Context, operationID string) error {
	if w.Store == nil {
		return store.ErrConflict
	}
	return w.Store.ClearWriterStart(ctx, operationID)
}

// DataCompatibility observes CURRENT data, not historical release flags. It
// checks both the candidate and previous writer so compensation cannot rewind
// code to a release that no longer understands the database.
type DataCompatibility struct{ Store *store.Store }

func (c DataCompatibility) Safe(ctx context.Context, previous, candidate policy.Desired) (bool, error) {
	if previous.Stateless() && candidate.Stateless() {
		return true, nil
	}
	if c.Store == nil {
		return false, store.ErrConflict
	}
	incarnation, err := c.Store.ActiveDataIncarnation(ctx, string(candidate.Name))
	if err != nil {
		return false, err
	}
	desired := []policy.Desired{candidate}
	if previous.Name != "" && !previous.Stateless() {
		desired = append(desired, previous)
	}
	for _, d := range desired {
		schema, err := c.Store.CandidateWriterSchema(ctx, incarnation, d)
		if err != nil {
			return false, err
		}
		if !data.WriterCompatibleWithAllocations(ctx, schema.Bindings, d.SchemaCompatibility, schema.Definitions, schema.Allocations) {
			return false, nil
		}
	}
	return true, nil
}
