package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// sqlitePreparation is one canonical protected preparation. Every read
// revalidates its columns, canonical bytes, real task join and full journal.
type sqlitePreparation struct {
	intent   sqliteprep.Intent
	raw      []byte
	events   []sqliteprep.Event
	point    *sqliteprep.PreparedPoint
	consumed bool
	state    ops.State
}

func (p sqlitePreparation) history() sqliteprep.History {
	return sqliteprep.History{Intent: p.intent, Events: p.events}
}

func (p sqlitePreparation) event(stage sqliteprep.Stage) *sqliteprep.Event {
	for i := range p.events {
		if p.events[i].Stage == stage {
			return &p.events[i]
		}
	}
	return nil
}

func readSQLitePreparation(ctx context.Context, q dataQuerier, id string) (sqlitePreparation, error) {
	p, err := readSQLitePreparationCore(ctx, q, id)
	if err != nil {
		return p, err
	}
	err = p.readConsumer(ctx, q)
	return p, err
}

func readSQLitePreparationCore(ctx context.Context, q dataQuerier, id string) (sqlitePreparation, error) {
	var p sqlitePreparation
	var operation, fence, mode, arm string
	var database data.DatabaseID
	var binding data.ReplicaBindingID
	var epoch data.ReplicaEpochID
	var supersedes, coverage, baseline sql.NullString
	err := q.QueryRowContext(ctx, "SELECT operation_id,database_id,binding_id,epoch_id,fence_id,mode,arm,supersedes,coverage_origin,baseline_from,canonical FROM data_sqlite_preparations WHERE id=?", id).Scan(&operation, &database, &binding, &epoch, &fence, &mode, &arm, &supersedes, &coverage, &baseline, &p.raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if p.intent, err = sqliteprep.DecodeIntent(p.raw); err != nil {
		return p, &IntegrityError{}
	}
	i := p.intent
	if i.Input.PreparationID != id || i.Task.OperationID != operation || i.Scope.DatabaseID != database || i.Scope.BindingID != binding || i.Scope.EpochID != epoch || string(i.Fence.ID) != fence || string(i.Mode) != mode || string(i.Arm) != arm || i.Input.Supersedes != supersedes.String || i.CoverageOrigin != coverage.String || i.BaselineFrom != baseline.String {
		return p, &IntegrityError{}
	}
	op, _, err := readSQLiteOperation(ctx, q, i.Task)
	if err != nil {
		return p, errors.Join(&IntegrityError{}, err)
	}
	p.state = op.State
	if err = p.readJournal(ctx, q); err != nil {
		return p, err
	}
	return p, nil
}

// readConsumer validates the single immutable consumer, when present.
func (p *sqlitePreparation) readConsumer(ctx context.Context, q dataQuerier) error {
	var operation, kind string
	var successor sql.NullString
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT operation_id,kind,successor_id,canonical FROM data_sqlite_point_consumers WHERE preparation_id=?", p.intent.Input.PreparationID).Scan(&operation, &kind, &successor, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	c, err := sqliteprep.DecodeConsumer(raw)
	if err != nil || c.PreparationID != p.intent.Input.PreparationID || c.OperationID != operation || string(c.Kind) != kind || c.SuccessorID != successor.String || c.App != p.intent.Task.App {
		return &IntegrityError{}
	}
	if err := p.validateConsumer(ctx, q, c); err != nil {
		return err
	}
	p.consumed = true
	return nil
}

func (p *sqlitePreparation) readJournal(ctx context.Context, q dataQuerier) (resultErr error) {
	rows, err := q.QueryContext(ctx, "SELECT seq,stage,canonical FROM data_sqlite_preparation_events WHERE preparation_id=? ORDER BY seq", p.intent.Input.PreparationID)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	for rows.Next() {
		var seq int
		var stage string
		var raw []byte
		if err = rows.Scan(&seq, &stage, &raw); err != nil {
			return err
		}
		e, err := sqliteprep.DecodeEvent(raw)
		if err != nil || e.Sequence != seq || string(e.Stage) != stage {
			return &IntegrityError{}
		}
		p.events = append(p.events, e)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if p.history().Validate() != nil {
		return &IntegrityError{}
	}
	return p.readPoint(ctx, q)
}

func (p *sqlitePreparation) readPoint(ctx context.Context, q dataQuerier) error {
	var source string
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT source_point_id,canonical FROM data_sqlite_prepared_points WHERE preparation_id=?", p.intent.Input.PreparationID).Scan(&source, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	point, err := sqliteprep.DecodePreparedPoint(raw)
	if err != nil || point.Source.Point.ID != source {
		return &IntegrityError{}
	}
	p.point = &point
	return nil
}

// readSQLiteLineage derives the qualified predecessor projection from the
// actual journal. Inherited baseline and coverage references are flattened to
// their protected origin events; a coverage/baseline relationship is reported
// only when one journal or a validated reverify claim proved it.
func readSQLiteLineage(ctx context.Context, q dataQuerier, id string) (sqliteprep.Lineage, sqlitePreparation, error) {
	prev, err := readSQLitePreparation(ctx, q, id)
	if err != nil {
		return sqliteprep.Lineage{}, prev, err
	}
	l := sqliteprep.Lineage{Previous: prev.intent, Terminal: prev.state.IsTerminal(), Unconsumed: !prev.consumed}
	l.OwnStopped = prev.event(sqliteprep.ReplicaStopped) != nil
	l.OwnUploaded = prev.event(sqliteprep.CoverageUploaded) != nil
	if l.Baseline, err = sqliteBaseline(ctx, q, prev); err != nil {
		return l, prev, err
	}
	if l.Baseline == nil {
		return l, prev, nil
	}
	l.Coverage, err = sqliteCoverage(ctx, q, prev, *l.Baseline)
	return l, prev, err
}

func sqliteBaseline(ctx context.Context, q dataQuerier, prev sqlitePreparation) (*sqliteprep.StoppedRecord, error) {
	origin := prev
	if prev.event(sqliteprep.ReplicaStopped) == nil {
		if prev.intent.BaselineFrom == "" {
			return nil, nil
		}
		var err error
		if origin, err = readSQLitePreparation(ctx, q, prev.intent.BaselineFrom); err != nil {
			return nil, err
		}
	}
	stopped := origin.event(sqliteprep.ReplicaStopped)
	if stopped == nil {
		return nil, &IntegrityError{}
	}
	return &sqliteprep.StoppedRecord{PreparationID: origin.intent.Input.PreparationID, Scope: origin.intent.Scope, Fence: origin.intent.Fence, Bundle: *stopped.Stopped}, nil
}

func sqliteCoverage(ctx context.Context, q dataQuerier, prev sqlitePreparation, baseline sqliteprep.StoppedRecord) (*sqliteprep.CoverageRecord, error) {
	origin := prev
	if prev.event(sqliteprep.CoverageUploaded) == nil {
		if prev.intent.CoverageOrigin == "" {
			return nil, nil
		}
		var err error
		if origin, err = readSQLitePreparation(ctx, q, prev.intent.CoverageOrigin); err != nil {
			return nil, err
		}
	}
	uploaded := origin.event(sqliteprep.CoverageUploaded)
	if uploaded == nil {
		return nil, &IntegrityError{}
	}
	digest, err := sqliteprep.Fingerprint(baseline.Bundle)
	if err != nil {
		return nil, &IntegrityError{}
	}
	return &sqliteprep.CoverageRecord{Source: *uploaded.Source, Scope: origin.intent.Scope, Fence: origin.intent.Fence, BaselinePreparationID: baseline.PreparationID, BaselineSHA256: digest}, nil
}

// uncertainSnapshot is a recorded capture intent whose single upload
// invocation has no known result. It is informational for abandon only.
func (p sqlitePreparation) uncertainSnapshot() *sqliteprep.SnapshotPlan {
	intent := p.event(sqliteprep.CoverageIntent)
	if intent == nil || p.event(sqliteprep.CoverageUploaded) != nil {
		return nil
	}
	return intent.Snapshot
}
