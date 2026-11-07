package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// claimWindow bounds the age of an intent's claim time at its transaction.
const claimWindow = time.Minute

// SQLiteLocationObserver reads the pre-stop database directory identity. Its
// value is only a claim: the journal admits it against protected allocation
// evidence and runner ownership, and the held source must later match it.
type SQLiteLocationObserver func(context.Context, data.SQLiteDatabaseBinding) (sqliteprep.Location, error)

// SQLiteSchemaObserver reads the actual source schema with the registered
// definitions of that database. Its result is only a claim: the journal admits
// it against the current declaration and registry, and every later held-source
// and host observation must equal it.
type SQLiteSchemaObserver func(context.Context, data.SQLiteDatabaseBinding, []data.SQLiteSchemaDefinition) data.SQLiteSchemaObservation

// SQLitePreparationJournal is the protected sqliteprep.Journal of one task.
// Writes need the store-minted mutation guard presented at Claim, or for
// abandon the source hold beneath it; reads rejoin every protected record.
type SQLitePreparationJournal struct {
	store    *Store
	location SQLiteLocationObserver
	schema   SQLiteSchemaObserver
	guard    *SQLiteMutationGuard
}

func (s *Store) SQLitePreparationJournal(location SQLiteLocationObserver) *SQLitePreparationJournal {
	return &SQLitePreparationJournal{store: s, location: location}
}

// SQLiteObservedPreparationJournal additionally selects a fresh preparation's
// schema from an actual source observation, so a declaration accepting
// several markers is resolved by evidence rather than refused or guessed.
func (s *Store) SQLiteObservedPreparationJournal(location SQLiteLocationObserver, schema SQLiteSchemaObserver) *SQLitePreparationJournal {
	return &SQLitePreparationJournal{store: s, location: location, schema: schema}
}

func sqliteNow() time.Time { return time.Now().UTC() }

func (j *SQLitePreparationJournal) readTx(ctx context.Context) (*sql.Tx, error) {
	return j.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}

// ReadTask reports the joined task state. Success is returned only after the
// complete historical completion validates; a point-shaped row is not success.
func (j *SQLitePreparationJournal) ReadTask(ctx context.Context, task sqliteprep.Task) (sqliteprep.TaskStatus, error) {
	tx, err := j.readTx(ctx)
	if err != nil {
		return sqliteprep.TaskStatus{}, err
	}
	defer func() { _ = tx.Rollback() }()
	op, _, err := readSQLiteOperation(ctx, tx, task)
	if err != nil {
		return sqliteprep.TaskStatus{}, err
	}
	status := sqliteprep.TaskStatus{Task: task}
	if status.Claimed, err = sqliteClaimed(ctx, tx, op); err != nil {
		return status, err
	}
	switch op.State {
	case ops.Preflight:
		status.State = sqliteprep.Preflight
	case ops.Failed:
		status.State = sqliteprep.Failed
	case ops.RecoveryRequired:
		status.State = sqliteprep.RecoveryRequired
	case ops.Succeeded:
		receipt, err := readSQLiteSuccess(ctx, tx, op, task)
		if err != nil {
			return status, err
		}
		status.State, status.Receipt = sqliteprep.Succeeded, &receipt
	default:
		return status, ErrConflict
	}
	return status, tx.Commit()
}

// sqliteClaimed reports a protected claim: a preparation journal for a prepare
// task, or a joined stop intent or point consumer for an abandon task.
func sqliteClaimed(ctx context.Context, q dataQuerier, op ops.Operation) (bool, error) {
	query := "SELECT count(*) FROM data_sqlite_preparations WHERE operation_id=?"
	if op.Kind == ops.SQLitePrepareAbandon {
		stopped, err := sqliteAbandonStopped(ctx, q, op)
		if err != nil || stopped {
			return stopped, err
		}
		query = "SELECT count(*) FROM data_sqlite_point_consumers WHERE operation_id=?"
	}
	var n int
	err := q.QueryRowContext(ctx, query, op.ID).Scan(&n)
	return n != 0, err
}

// Inspect derives the current scope, fence, lineage and point only from
// protected records. The observed location is admitted against allocation
// evidence; it is read before the transaction, never inside it.
func (j *SQLitePreparationJournal) Inspect(ctx context.Context, task sqliteprep.Task, in sqliteprep.Input) (sqliteprep.Facts, error) {
	location, err := j.observeLocation(ctx, in.DatabaseID)
	if err != nil {
		return sqliteprep.Facts{}, err
	}
	var observed *data.SQLiteSchemaObservation
	if task.Kind == sqliteprep.PrepareTask && in.Supersedes == "" {
		if observed, err = j.observeSchema(ctx, in.DatabaseID); err != nil {
			return sqliteprep.Facts{}, err
		}
	}
	tx, err := j.readTx(ctx)
	if err != nil {
		return sqliteprep.Facts{}, err
	}
	defer func() { _ = tx.Rollback() }()
	op, stored, err := readSQLiteOperation(ctx, tx, task)
	if err != nil || op.State != ops.Preflight || stored != in {
		return sqliteprep.Facts{}, errors.Join(sqliteRefuse(sqliteprep.InvalidEvidence), err)
	}
	now := sqliteNow()
	c, err := readSQLiteCurrent(ctx, tx, in, now)
	if err != nil {
		return sqliteprep.Facts{}, err
	}
	if err = c.location(location, uint32(os.Geteuid()), uint32(os.Getegid())); err != nil { //nolint:gosec // Runner IDs are nonnegative.
		return sqliteprep.Facts{}, err
	}
	facts := sqliteprep.Facts{Location: location, CredentialExpiresAt: c.expires}
	if task.Kind == sqliteprep.PrepareTask && in.Supersedes == "" {
		return j.freshFacts(c, task, facts, observed, now)
	}
	return facts, j.lineageFacts(ctx, tx, c, task, in, &facts, now)
}

// observeSchema reads the actual source with every registered definition of
// the database before the read transaction. Without an observer it reports
// nothing and the single-marker rule applies.
func (j *SQLitePreparationJournal) observeSchema(ctx context.Context, database data.DatabaseID) (*data.SQLiteSchemaObservation, error) {
	if j.schema == nil {
		return nil, nil
	}
	permit, err := readReplicaPermit(ctx, j.store.db, database)
	if err != nil {
		return nil, err
	}
	definitions, err := readRegisteredSQLiteSchemas(ctx, j.store.db, permit.Database)
	if err != nil {
		return nil, err
	}
	observed := j.schema(ctx, permit.Database, definitions)
	return &observed, nil
}

func readRegisteredSQLiteSchemas(ctx context.Context, q dataQuerier, database data.SQLiteDatabaseBinding) (definitions []data.SQLiteSchemaDefinition, resultErr error) {
	rows, err := q.QueryContext(ctx, "SELECT marker,catalog_sha256 FROM data_schema_definitions WHERE database_id=? ORDER BY marker LIMIT 65", database.DatabaseID)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	definitions = []data.SQLiteSchemaDefinition{}
	for rows.Next() {
		d := data.SQLiteSchemaDefinition{Database: database.Name}
		if err = rows.Scan(&d.Marker, &d.CatalogSHA256); err != nil {
			return nil, err
		}
		if !data.ValidMarker(d.Marker) || !data.ValidCatalogHash(d.CatalogSHA256) || len(definitions) == 64 {
			return nil, &IntegrityError{}
		}
		definitions = append(definitions, d)
	}
	return definitions, rows.Err()
}

func (j *SQLitePreparationJournal) observeLocation(ctx context.Context, database data.DatabaseID) (sqliteprep.Location, error) {
	if j.location == nil {
		return sqliteprep.Location{}, ErrInvalid
	}
	permit, err := readReplicaPermit(ctx, j.store.db, database)
	if err != nil {
		return sqliteprep.Location{}, err
	}
	return j.location(ctx, permit.Database)
}

func (j *SQLitePreparationJournal) freshFacts(c sqliteCurrent, task sqliteprep.Task, facts sqliteprep.Facts, observed *data.SQLiteSchemaObservation, now time.Time) (sqliteprep.Facts, error) {
	scope, err := sqliteTaskScope(c, task.Kind, nil, observed, now)
	if err != nil {
		return facts, err
	}
	// An existing held fence belongs to another owner; fresh never adopts it.
	if c.held != nil {
		return facts, sqliteRefuse(sqliteprep.LocalUncertain)
	}
	id, err := data.NewID()
	if err != nil {
		return facts, err
	}
	facts.Scope = scope
	facts.Fence = data.QuiescenceFence{ID: data.FenceID(id), DatabaseID: scope.DatabaseID, IncarnationID: scope.IncarnationID, OperationID: task.OperationID, State: data.FenceHeld}
	return facts, nil
}

func (j *SQLitePreparationJournal) lineageFacts(ctx context.Context, tx *sql.Tx, c sqliteCurrent, task sqliteprep.Task, in sqliteprep.Input, facts *sqliteprep.Facts, now time.Time) error {
	previous := in.Supersedes
	if task.Kind == sqliteprep.AbandonTask {
		previous = in.PreparationID
	}
	lineage, prev, err := readSQLiteLineage(ctx, tx, previous)
	if err != nil {
		return errors.Join(sqliteRefuse(sqliteprep.InvalidEvidence), err)
	}
	if prev.intent.Scope.DatabaseID != in.DatabaseID || c.held == nil || *c.held != prev.intent.Fence {
		return sqliteRefuse(sqliteprep.InvalidEvidence)
	}
	facts.Scope, err = sqliteTaskScope(c, task.Kind, &prev.intent.Scope, nil, now)
	if err != nil {
		return err
	}
	facts.Fence, facts.Lineage, facts.Point = prev.intent.Fence, &lineage, prev.point
	facts.UncertainSnapshot = prev.uncertainSnapshot()
	return nil
}

// sqliteTaskScope re-derives the scope a task may use now. Preparation needs
// current retention and exact credentials; abandon keeps the original record.
func sqliteTaskScope(c sqliteCurrent, kind sqliteprep.TaskKind, previous *sqliteprep.Scope, observed *data.SQLiteSchemaObservation, now time.Time) (sqliteprep.Scope, error) {
	schema, err := c.schema(previous, observed, now)
	if err != nil {
		return sqliteprep.Scope{}, err
	}
	scope := c.scope
	scope.Schema = schema
	if kind != sqliteprep.AbandonTask {
		return scope, c.authorization
	}
	if previous == nil {
		return sqliteprep.Scope{}, sqliteRefuse(sqliteprep.InvalidEvidence)
	}
	scope.Retention = previous.Retention
	return scope, nil
}

// Claim atomically binds the real preflight task, current scope, fence and
// fixed mode. Fresh records its own fence; supersede consumes its predecessor
// under the identical original fence. Only store-minted guards are accepted.
func (j *SQLitePreparationJournal) Claim(ctx context.Context, i sqliteprep.Intent, observation sqliteprep.HostObservation, held sqliteprep.StoppedSource) error {
	guard, hold, err := j.claimAuthority(ctx, i, observation, held)
	if err != nil {
		return err
	}
	observed, err := claimObservation(ctx, observation, held)
	if err != nil {
		return err
	}
	location, err := j.observeLocation(ctx, i.Scope.DatabaseID)
	if err != nil || location != i.Location {
		return errors.Join(sqliteRefuse(sqliteprep.LocalUncertain), err)
	}
	tx, cancel, err := j.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = j.claimTx(ctx, tx, i, observed, guard); err != nil {
		return err
	}
	if !guard.live(j.store, i.Scope.DatabaseID) || hold != nil && !hold.live(j.store, i.Scope.DatabaseID) {
		return ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	j.guard = guard
	return nil
}

// claimAuthority accepts only this store's live guard and, for a stopped mode,
// its live source hold. Running modes prove the replica lifetime lock is held
// by its supervisor; the store never starts a replica.
func (j *SQLitePreparationJournal) claimAuthority(ctx context.Context, i sqliteprep.Intent, observation sqliteprep.HostObservation, held sqliteprep.StoppedSource) (*SQLiteMutationGuard, *SQLiteSourceHold, error) {
	guard := sqliteGuardOf(observation)
	if i.Validate() != nil || !guard.live(j.store, i.Scope.DatabaseID) {
		return nil, nil, ErrConflict
	}
	stopped := i.Arm != sqliteprep.LTX || i.Mode == sqliteprep.SupersedeReverify
	if !stopped {
		if held != nil {
			return nil, nil, ErrConflict
		}
		return guard, nil, guard.requireRunningReplica(ctx)
	}
	hold := sqliteHoldOf(held)
	if !hold.live(j.store, i.Scope.DatabaseID) || hold.guard != guard {
		return nil, nil, ErrConflict
	}
	return guard, hold, nil
}

// sqliteObserved is the live host observation and held-source recheck made
// immediately before the claim transaction, outside it.
type sqliteObserved struct {
	host   sqliteprep.Observation
	bundle *sqliteprep.StoppedBundle
}

func claimObservation(ctx context.Context, observation sqliteprep.HostObservation, held sqliteprep.StoppedSource) (sqliteObserved, error) {
	o, err := observation.Observe(ctx)
	if err != nil {
		return sqliteObserved{}, err
	}
	out := sqliteObserved{host: o}
	if held == nil {
		return out, nil
	}
	bundle, err := held.Recheck(ctx)
	if err != nil {
		return sqliteObserved{}, err
	}
	out.bundle = &bundle
	return out, nil
}

// sqliteSelectionAdmits requires the intent's fixed mode, arm, stop requirement
// and inherited references to equal Select over protected lineage and the
// live observation. A request never supplies them.
func sqliteSelectionAdmits(i sqliteprep.Intent, scope sqliteprep.Scope, lineage *sqliteprep.Lineage, observed sqliteObserved) bool {
	sel, err := sqliteprep.Select(scope, lineage, observed.host, observed.bundle, sqliteNow())
	return err == nil && sel.Mode == i.Mode && sel.Arm == i.Arm && sel.StopWritersRequired == i.StopWritersRequired && sel.CoverageOrigin == i.CoverageOrigin && sel.BaselineFrom == i.BaselineFrom
}

func (j *SQLitePreparationJournal) claimTx(ctx context.Context, tx *sql.Tx, i sqliteprep.Intent, observed sqliteObserved, guard *SQLiteMutationGuard) error {
	now := sqliteNow()
	if i.ClaimedAt.After(now) || now.Sub(i.ClaimedAt) > claimWindow {
		return ErrConflict
	}
	op, in, err := readSQLiteOperation(ctx, tx, i.Task)
	if err != nil || op.State != ops.Preflight || in != i.Input {
		return errors.Join(ErrConflict, err)
	}
	if claimed, err := sqliteClaimed(ctx, tx, op); err != nil || claimed {
		return errors.Join(ErrConflict, err)
	}
	c, err := readSQLiteCurrent(ctx, tx, i.Input, now)
	if err != nil {
		return err
	}
	if err = guard.admitUnreleased(c, i.Task.Kind); err != nil {
		return err
	}
	if err = c.location(i.Location, uint32(os.Geteuid()), uint32(os.Getegid())); err != nil { //nolint:gosec // Runner IDs are nonnegative.
		return err
	}
	if i.Mode == sqliteprep.Fresh {
		return j.claimFresh(ctx, tx, c, i, observed, now)
	}
	return j.claimSuccessor(ctx, tx, c, i, observed, now)
}

func (j *SQLitePreparationJournal) claimFresh(ctx context.Context, tx *sql.Tx, c sqliteCurrent, i sqliteprep.Intent, observed sqliteObserved, now time.Time) error {
	// The fresh schema is re-derived from the live guarded host observation,
	// never from the intent; it must name the identical admitted schema.
	scope, err := sqliteTaskScope(c, i.Task.Kind, nil, &observed.host.Schema, now)
	if err != nil || !sameSQLiteCurrent(i.Scope, scope) || c.held != nil || i.Fence.OperationID != i.Task.OperationID || !sqliteSelectionAdmits(i, scope, nil, observed) {
		return errors.Join(ErrConflict, err)
	}
	f := i.Fence
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_fences VALUES(?,?,?,?,?)", f.ID, f.DatabaseID, f.IncarnationID, f.OperationID, f.State); err != nil {
		return err
	}
	return insertSQLitePreparation(ctx, tx, i)
}

func (j *SQLitePreparationJournal) claimSuccessor(ctx context.Context, tx *sql.Tx, c sqliteCurrent, i sqliteprep.Intent, observed sqliteObserved, now time.Time) error {
	lineage, prev, err := readSQLiteLineage(ctx, tx, i.Input.Supersedes)
	if err != nil {
		return errors.Join(ErrConflict, err)
	}
	scope, err := sqliteTaskScope(c, i.Task.Kind, &prev.intent.Scope, nil, now)
	if err != nil || !sameSQLiteCurrent(i.Scope, scope) || !sqliteprep.SameLineageScope(prev.intent.Scope, scope) {
		return errors.Join(ErrConflict, err)
	}
	if !lineage.Terminal || !lineage.Unconsumed || prev.intent.Task.Requester != i.Task.Requester || prev.intent.Fence != i.Fence || prev.intent.Location != i.Location || c.held == nil || *c.held != i.Fence {
		return ErrConflict
	}
	if !sqliteSelectionAdmits(i, scope, &lineage, observed) {
		return ErrConflict
	}
	consumer := sqliteprep.Consumer{PreparationID: prev.intent.Input.PreparationID, OperationID: i.Task.OperationID, Requester: i.Task.Requester, App: i.Task.App, InputSHA256: i.Task.InputSHA256, Kind: sqliteprep.SupersedeConsumer, SuccessorID: i.Input.PreparationID, PolicySHA256: i.Scope.PolicySHA256, ClaimedAt: i.ClaimedAt}
	raw, err := sqliteprep.Encode(consumer)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_point_consumers VALUES(?,?,?,?,?)", consumer.PreparationID, consumer.OperationID, consumer.Kind, consumer.SuccessorID, raw); err != nil {
		return err
	}
	return insertSQLitePreparation(ctx, tx, i)
}

func insertSQLitePreparation(ctx context.Context, tx *sql.Tx, i sqliteprep.Intent) error {
	raw, err := sqliteprep.Encode(i)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_preparations VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", i.Input.PreparationID, i.Task.OperationID, i.Scope.DatabaseID, i.Scope.BindingID, i.Scope.EpochID, i.Fence.ID, i.Mode, i.Arm, sqliteNullable(i.Input.Supersedes), sqliteNullable(i.CoverageOrigin), sqliteNullable(i.BaselineFrom), raw)
	if err != nil {
		return err
	}
	claimed := sqliteprep.Event{PreparationID: i.Input.PreparationID, Sequence: 1, Stage: sqliteprep.Claimed, At: i.ClaimedAt}
	return insertSQLiteEvent(ctx, tx, claimed)
}

func insertSQLiteEvent(ctx context.Context, tx *sql.Tx, e sqliteprep.Event) error {
	raw, err := sqliteprep.Encode(e)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_preparation_events VALUES(?,?,?,?)", e.PreparationID, e.Sequence, e.Stage, raw)
	return err
}

func sqliteNullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// Append records one checked mode-specific edge. Retained coverage must be the
// exact protected origin source and a recorded stopped baseline must match
// the inherited one; prepared is reserved for atomic completion.
func (j *SQLitePreparationJournal) Append(ctx context.Context, i sqliteprep.Intent, e sqliteprep.Event) error {
	needsHold := i.Arm != sqliteprep.LTX || i.Mode == sqliteprep.SupersedeReverify || e.Stage == sqliteprep.ReplicaStopped
	if !j.guard.live(j.store, i.Scope.DatabaseID) || e.Stage == sqliteprep.Prepared || needsHold && !j.guard.source.Load().live(j.store, i.Scope.DatabaseID) {
		return ErrConflict
	}
	tx, cancel, err := j.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	prev, err := readSQLiteAppendable(ctx, tx, i)
	if err != nil {
		return err
	}
	needsHold = needsHold || prev.event(sqliteprep.ReplicaStopped) != nil
	if needsHold && !j.guard.source.Load().live(j.store, i.Scope.DatabaseID) {
		return ErrConflict
	}
	candidate := prev.history()
	candidate.Events = append(candidate.Events, e)
	if candidate.Validate() != nil {
		return ErrConflict
	}
	if err = checkSQLiteInheritance(ctx, tx, i, e); err != nil {
		return err
	}
	if err = insertSQLiteEvent(ctx, tx, e); err != nil {
		return err
	}
	if !j.guard.live(j.store, i.Scope.DatabaseID) || needsHold && !j.guard.source.Load().live(j.store, i.Scope.DatabaseID) {
		return ErrConflict
	}
	return tx.Commit()
}

// readSQLiteAppendable requires the exact canonical intent of an unconsumed,
// incomplete preflight preparation that still holds its original fence.
func readSQLiteAppendable(ctx context.Context, tx *sql.Tx, i sqliteprep.Intent) (sqlitePreparation, error) {
	prev, err := readSQLitePreparation(ctx, tx, i.Input.PreparationID)
	if err != nil {
		return prev, err
	}
	raw, err := sqliteprep.Encode(i)
	if err != nil || !bytes.Equal(raw, prev.raw) || prev.state != ops.Preflight || prev.consumed || prev.point != nil {
		return prev, errors.Join(ErrConflict, err)
	}
	var state string
	if err = tx.QueryRowContext(ctx, "SELECT state FROM data_fences WHERE id=? AND operation_id=? AND database_id=? AND incarnation_id=?", i.Fence.ID, i.Fence.OperationID, i.Fence.DatabaseID, i.Fence.IncarnationID).Scan(&state); err != nil || state != string(data.FenceHeld) {
		return prev, errors.Join(ErrConflict, err)
	}
	return prev, nil
}

func checkSQLiteInheritance(ctx context.Context, tx *sql.Tx, i sqliteprep.Intent, e sqliteprep.Event) error {
	if e.Stage == sqliteprep.ReplicaStopped && i.BaselineFrom != "" {
		origin, err := readSQLitePreparation(ctx, tx, i.BaselineFrom)
		if err != nil {
			return err
		}
		baseline := origin.event(sqliteprep.ReplicaStopped)
		if baseline == nil || !baseline.Stopped.Matches(*e.Stopped) {
			return sqliteRefuse(sqliteprep.StoppedSourceChanged)
		}
	}
	if e.Stage != sqliteprep.CoverageRetained {
		return nil
	}
	origin, err := readSQLitePreparation(ctx, tx, i.CoverageOrigin)
	if err != nil {
		return err
	}
	uploaded := origin.event(sqliteprep.CoverageUploaded)
	if uploaded == nil || !sqliteSameJSON(*uploaded.Source, *e.Source) {
		return ErrConflict
	}
	return nil
}

func sqliteSameJSON(a, b any) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}
