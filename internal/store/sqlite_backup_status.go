package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/sqlitestatus"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

// SQLiteStatusFacts reads protected current scope without startup admission,
// mutation locks, schema observations or migrations. Uncommitted data is visible.
type SQLiteStatusFacts struct {
	Facts      sqlitestatus.Facts
	Replica    data.ReplicaBinding
	Credential *CredentialRecord
}

func (s *Store) ReadSQLiteStatus(ctx context.Context, app, database string) (SQLiteStatusFacts, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SQLiteStatusFacts{}, err
	}
	defer func() { _ = tx.Rollback() }()
	permit, err := s.sqliteStatusPermit(ctx, tx, app, database)
	if err != nil {
		return SQLiteStatusFacts{}, err
	}
	b := permit.Replica
	out := SQLiteStatusFacts{Replica: b, Facts: sqlitestatus.Facts{App: app, Database: string(permit.Database.Name), DatabaseID: string(permit.Database.DatabaseID), BindingID: string(b.BindingID), EpochID: string(b.EpochID), Committed: b.Committed, Credential: sqlitestatus.Credential{State: "missing"}, Lifecycle: sqlitestatus.Lifecycle{State: "unknown"}, Fences: []sqlitestatus.Fence{}}}
	for _, f := range permit.Fences {
		if f.State == data.FenceHeld {
			out.Facts.Fences = append(out.Facts.Fences, sqlitestatus.Fence{ID: string(f.ID), OwnerOperation: f.OperationID})
		}
	}
	out.Facts.LastVerified, err = sqliteStatusVerification(ctx, tx, app, string(permit.Database.Name), b)
	if err != nil {
		return SQLiteStatusFacts{}, err
	}
	if err := tx.Commit(); err != nil {
		return SQLiteStatusFacts{}, err
	}
	record, err := s.sqliteStatusCredential(ctx, app, b)
	if err == nil {
		out.Credential = &record
		out.Facts.Credential = sqlitestatus.Credential{State: "available", Version: record.Version, ReceivedAt: &record.ReceivedAt, ExpiresAt: record.ExpiresAt}
	} else if !errors.Is(err, ErrNotFound) {
		return SQLiteStatusFacts{}, err
	}
	out.Facts.LastVerified, err = s.sqliteStatusInitialization(ctx, out)
	return out, err
}
func (s *Store) sqliteStatusPermit(ctx context.Context, tx *sql.Tx, app, database string) (ReplicaPermit, error) {
	rows, err := tx.QueryContext(ctx, `SELECT d.id FROM data_databases d JOIN data_active_incarnations a ON a.incarnation_id=d.incarnation_id JOIN data_engine_kinds k ON k.database_id=d.id WHERE a.app=? AND k.engine='sqlite' AND (?='' OR d.name=?) ORDER BY d.name LIMIT 2`, app, database, database)
	if err != nil {
		return ReplicaPermit{}, err
	}
	ids := []data.DatabaseID{}
	for rows.Next() {
		var id data.DatabaseID
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return ReplicaPermit{}, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return ReplicaPermit{}, err
	}
	if len(ids) != 1 {
		return ReplicaPermit{}, ErrNotFound
	}
	p, err := readReplicaPermit(ctx, tx, ids[0])
	if err != nil {
		return p, err
	}
	return p, s.validateReplicaPermit(ctx, tx, p)
}
func (s *Store) sqliteStatusCredential(ctx context.Context, app string, b data.ReplicaBinding) (CredentialRecord, error) {
	r, err := s.CredentialReceipt(ctx, b.BindingID, b.CredentialVersion)
	if err != nil {
		return CredentialRecord{}, err
	}
	if r.App != app || r.EpochID != b.EpochID || r.Destination != b.Destination.Reference || r.CredentialRef != b.Destination.CredentialRef {
		return CredentialRecord{}, &IntegrityError{}
	}
	return r, nil
}

// Restore-point rows alone record uploads, not independent verification. Only
// succeeded restore tasks with their exact input and canonical outcome qualify.
func sqliteStatusVerification(ctx context.Context, tx *sql.Tx, app, database string, b data.ReplicaBinding) (*sqlitestatus.Verified, error) {
	row := tx.QueryRowContext(ctx, `SELECT o.id,COALESCE(o.plan_id,''),o.requester,o.idempotency_key,o.state,o.created_at,o.updated_at,o.kind,o.app,o.secret_ref,o.recovery_of FROM operations o JOIN operation_outcomes r ON r.operation_id=o.id WHERE o.kind='restore_test' AND o.state='succeeded' AND o.app=? AND (json_extract(r.canonical,'$.receipt.source.litestream_ltx.binding_id')=? OR json_extract(r.canonical,'$.receipt.source.sqlite_snapshot.binding_id')=?) AND (json_extract(r.canonical,'$.receipt.source.litestream_ltx.epoch')=? OR json_extract(r.canonical,'$.receipt.source.sqlite_snapshot.epoch')=?) ORDER BY julianday(json_extract(r.canonical,'$.receipt.observed_at')) DESC,o.id DESC LIMIT 1`, app, b.BindingID, b.BindingID, b.EpochID, b.EpochID)
	op, err := scanOperation(row)
	if errors.Is(err, ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var raw, input []byte
	if err := tx.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", op.ID).Scan(&raw); err != nil {
		return nil, err
	}
	outcome, err := ops.DecodeTaskOutcome(op, raw)
	if err != nil {
		return nil, &IntegrityError{}
	}
	receipt, err := restore.DecodeReceipt(outcome.Receipt)
	if err != nil {
		return nil, &IntegrityError{}
	}
	if err := tx.QueryRowContext(ctx, "SELECT canonical FROM restore_task_inputs WHERE id=? AND requester=?", op.SecretRef, op.Requester).Scan(&input); err != nil {
		return nil, err
	}
	if !sqliteStatusInput(input, op, app, database, receipt) || !sqliteStatusSelectedPoint(ctx, tx, input, receipt, b) {
		return nil, &IntegrityError{}
	}
	if !sqliteStatusSource(receipt.Source, b) {
		return nil, &IntegrityError{}
	}
	return &sqlitestatus.Verified{OperationID: op.ID, Source: receipt.Source, VerifiedAt: receipt.ObservedAt, InvariantCheck: receipt.InvariantCheck}, nil
}
func sqliteStatusInput(raw []byte, op ops.Operation, app, database string, r restore.SQLiteReceipt) bool {
	if len(raw) > 4096 {
		return false
	}
	if _, err := strictjson.Object(raw, "app", "database", "txid", "point"); err != nil {
		return false
	}
	var input ops.RestoreTaskInput
	if json.Unmarshal(raw, &input) != nil || !input.Valid() || input.App != app || input.Database != "" && input.Database != database {
		return false
	}
	canonical, _ := json.Marshal(input)
	digest := sha256.Sum256(append([]byte(op.Requester+"\x00"), raw...))
	if !bytes.Equal(canonical, raw) || hex.EncodeToString(digest[:]) != op.SecretRef {
		return false
	}
	if input.Point == "" && r.Source.LTX == nil {
		return false
	}
	if input.TXID != "" && (r.Source.LTX == nil || strings.TrimLeft(strings.ToLower(input.TXID), "0") != strconv.FormatUint(r.Source.LTX.TXID, 16)) {
		return false
	}
	return true
}
func sqliteStatusSource(s restore.SQLiteSource, b data.ReplicaBinding) bool {
	if l := s.LTX; l != nil {
		return s.Kind == restore.LitestreamLTX && l.BindingID == string(b.BindingID) && l.Epoch == string(b.EpochID)
	}
	p := s.Snapshot
	return p != nil && s.Kind == restore.SQLiteSnapshot && p.BindingID == string(b.BindingID) && p.Epoch == string(b.EpochID) && p.ObjectKey == strings.TrimSuffix(b.RemotePrefix, "/")+"/restore-points/"+p.PointID+"/snapshot.sqlite"
}

func sqliteStatusSelectedPoint(ctx context.Context, tx *sql.Tx, raw []byte, r restore.SQLiteReceipt, b data.ReplicaBinding) bool {
	var input ops.RestoreTaskInput
	if json.Unmarshal(raw, &input) != nil {
		return false
	}
	if input.Point == "" {
		return true
	}
	p, err := readReplicaCursor(tx.QueryRowContext(ctx, "SELECT canonical FROM data_restore_points WHERE id=? AND binding_id=? AND epoch_id=?", input.Point, b.BindingID, b.EpochID), 16<<10, func(p data.SQLiteRestorePoint) bool {
		return p.Validate() == nil && p.ID == input.Point && p.BindingID == b.BindingID && p.EpochID == b.EpochID && p.Schema.DatabaseID == b.DatabaseID
	})
	if err != nil {
		return false
	}
	if r.Schema.State != restore.SchemaState(p.Schema.State) || r.Schema.Marker != p.Schema.Marker || r.Schema.CatalogSHA256 != p.Schema.CatalogSHA256 {
		return false
	}
	if l := r.Source.LTX; l != nil {
		return p.LTX != nil && p.LTX.TXID == l.TXID
	}
	snapshot := r.Source.Snapshot
	return snapshot != nil && p.Snapshot != nil && snapshot.PointID == p.ID && snapshot.ObjectKey == p.Snapshot.ObjectKey && snapshot.SHA256 == p.Snapshot.SHA256 && snapshot.Size == p.Snapshot.Size
}

// Initialization has its own independently verified snapshot receipt. Reuse its
// protected reader; a raw data_restore_points upload row never qualifies.
func (s *Store) sqliteStatusInitialization(ctx context.Context, f SQLiteStatusFacts) (*sqlitestatus.Verified, error) {
	newest := f.Facts.LastVerified
	var planID string
	err := s.db.QueryRowContext(ctx, `SELECT p.id FROM data_init_plans p JOIN data_init_operations o ON o.plan_id=p.id JOIN data_init_events e ON e.operation_id=o.id WHERE json_extract(p.canonical,'$.database.database_id')=? AND json_extract(p.canonical,'$.replica_epoch')=? AND e.state='restore_point_verified' ORDER BY julianday(json_extract(e.receipt,'$.receipt.observed_at')) DESC LIMIT 1`, f.Replica.DatabaseID, f.Replica.EpochID).Scan(&planID)
	if errors.Is(err, sql.ErrNoRows) {
		return newest, nil
	}
	if err != nil {
		return nil, err
	}
	op, exists, err := s.ReadInitOperation(ctx, planID)
	if err != nil || !exists {
		return nil, &IntegrityError{}
	}
	point, err := s.LoadInitRestorePoint(ctx, op)
	if err != nil || point.DatabaseID != f.Replica.DatabaseID || !sqliteStatusSource(point.Receipt.Source, f.Replica) {
		return nil, &IntegrityError{}
	}
	if newest != nil && !point.Receipt.ObservedAt.After(newest.VerifiedAt) {
		return newest, nil
	}
	return &sqlitestatus.Verified{OperationID: op.ID, Source: point.Receipt.Source, VerifiedAt: point.Receipt.ObservedAt, InvariantCheck: point.Receipt.InvariantCheck}, nil
}
