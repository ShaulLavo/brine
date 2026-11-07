package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

func (s *Store) RemovalCandidate(ctx context.Context, app string) (archive.Candidate, []archive.PreparedRef, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return archive.Candidate{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	c, refs, err := readRemovalCandidate(ctx, tx, app, s.removalNow())
	if err != nil {
		return c, nil, err
	}
	return c, refs, tx.Commit()
}

func readRemovalCandidate(ctx context.Context, tx *sql.Tx, app string, now time.Time) (archive.Candidate, []archive.PreparedRef, error) {
	if err := guardArchiveCDN(ctx, tx, app); err != nil {
		return archive.Candidate{}, nil, err
	}
	if err := guardArchiveUDP(ctx, tx, app); err != nil {
		return archive.Candidate{}, nil, result.New(result.UDPReleaseRequired, err)
	}
	if err := guardRemovalEngines(ctx, tx, app); err != nil {
		return archive.Candidate{}, nil, err
	}
	c, err := readArchiveCandidateFor(ctx, tx, app, "", true)
	if err != nil {
		return c, nil, err
	}
	refs := []archive.PreparedRef{}
	for _, variant := range c.Databases {
		database, ok := variant.SQLite()
		if !ok {
			return c, nil, result.New(result.PersistentArchiveRequired, nil)
		}
		if len(database.Epochs) != 1 {
			return c, nil, result.New(result.ArchiveMultiEpochUnsupported, nil)
		}
		ref, found, err := readRemovalRef(ctx, tx, database.Binding.DatabaseID, now)
		if err != nil {
			return c, nil, result.New(result.RemovalPreparationRequired, err)
		}
		if database.Written && !found {
			return c, nil, result.New(result.RemovalPreparationRequired, nil)
		}
		if found {
			refs = append(refs, ref)
		}
	}
	return c, refs, nil
}

func sqliteArchiveFenceAllowed(ctx context.Context, q dataQuerier, f data.QuiescenceFence, owner string, removal bool) bool {
	if owner != "" {
		return checkSQLiteArchiveFenceOwner(ctx, q, owner, f) == nil
	}
	if !removal {
		return false
	}
	ref, found, err := readRemovalRef(ctx, q, f.DatabaseID, sqliteNow())
	return err == nil && found && ref.Fence == f
}

func readRemovalRef(ctx context.Context, q dataQuerier, database data.DatabaseID, now time.Time) (archive.PreparedRef, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id FROM data_sqlite_preparations p JOIN data_fences f ON f.id=p.fence_id WHERE p.database_id=? AND f.state='held' AND NOT EXISTS(SELECT 1 FROM data_sqlite_point_consumers c WHERE c.preparation_id=p.id) ORDER BY p.id`, database)
	if err != nil {
		return archive.PreparedRef{}, false, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return archive.PreparedRef{}, false, errors.Join(err, rows.Close())
		}
		ids = append(ids, id)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return archive.PreparedRef{}, false, err
	}
	if len(ids) == 0 {
		return archive.PreparedRef{}, false, nil
	}
	if len(ids) != 1 {
		return archive.PreparedRef{}, false, ErrConflict
	}
	p, err := readSQLitePreparation(ctx, q, ids[0])
	if err != nil || p.consumed || p.point == nil || p.state != ops.Succeeded || !now.Before(p.point.ExpiresAt) {
		return archive.PreparedRef{}, false, errors.Join(ErrConflict, err)
	}
	if err = checkRemovalPreparedHistory(ctx, q, p); err != nil {
		return archive.PreparedRef{}, false, err
	}
	ref, err := removalRef(p)
	return ref, err == nil, err
}

func removalRef(p sqlitePreparation) (archive.PreparedRef, error) {
	if p.point == nil {
		return archive.PreparedRef{}, ErrConflict
	}
	hash, err := sqliteprep.Fingerprint(*p.point)
	location, locErr := sqliteprep.Fingerprint(p.point.Stopped.Location)
	if err != nil || locErr != nil {
		return archive.PreparedRef{}, ErrInvalid
	}
	i := p.intent
	return archive.PreparedRef{DatabaseID: i.Scope.DatabaseID, PreparationID: i.Input.PreparationID, PreparedSHA256: hash, SourcePointID: p.point.Source.Point.ID, Fence: i.Fence, CredentialVersion: i.Scope.CredentialVersion, DestinationSHA256: i.Scope.TargetSHA256, RetentionSHA256: i.Scope.Retention.EvidenceSHA256, LocationSHA256: location, Cutoff: p.point.Source.Cutoff, ExpiresAt: p.point.ExpiresAt}, nil
}

func checkRemovalPreparedHistory(ctx context.Context, q dataQuerier, p sqlitePreparation) error {
	op, _, err := readSQLiteOperation(ctx, q, p.intent.Task)
	if err != nil || op.State != ops.Succeeded || p.point == nil {
		return errors.Join(ErrConflict, err)
	}
	var raw []byte
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", op.ID).Scan(&raw); err != nil {
		return err
	}
	outcome, err := ops.DecodeTaskOutcome(op, raw)
	if err != nil {
		return &IntegrityError{}
	}
	receipt, err := sqliteprep.DecodeReceipt(outcome.Receipt)
	if err != nil || sqliteprep.ValidateCompletion(p.history(), *p.point, receipt) != nil {
		return &IntegrityError{}
	}
	encoded, err := json.Marshal(p.point.Source.Point)
	var source []byte
	readErr := q.QueryRowContext(ctx, "SELECT canonical FROM data_restore_points WHERE id=? AND binding_id=? AND epoch_id=?", p.point.Source.Point.ID, p.intent.Scope.BindingID, p.intent.Scope.EpochID).Scan(&source)
	if err != nil || readErr != nil || string(source) != string(encoded) {
		return &IntegrityError{}
	}
	return nil
}

func removalCandidateMatches(current, wanted archive.Candidate) bool {
	return archiveCandidatesMatch(current, wanted)
}

func removalCurrentScope(ctx context.Context, tx *sql.Tx, p archive.Plan, d archive.SQLiteDatabase, prep sqlitePreparation, now time.Time) (sqliteprep.Scope, error) {
	current, err := readSQLiteCurrent(ctx, tx, prep.intent.Input, now)
	if err != nil {
		return sqliteprep.Scope{}, err
	}
	if current.scope.EmptyAllocation != nil {
		current.scope.EmptyAllocation.Location = prep.intent.Location
	}
	scope, err := sqliteTaskScope(current, sqliteprep.PrepareTask, &prep.intent.Scope, nil, now)
	if err != nil || scope.PolicySHA256 != strings.TrimPrefix(p.PolicyHash, "sha256:") || prep.point == nil || !sqliteprep.SameLineageScope(prep.intent.Scope, scope) || current.held == nil || *current.held != prep.intent.Fence {
		return scope, errors.Join(ErrConflict, err)
	}
	location := prep.point.Stopped.Location
	if current.allocated == nil || location.Device != current.allocated.DirectoryDevice || location.Inode != current.allocated.DirectoryInode {
		return scope, ErrConflict
	}
	if _, err = readSQLiteCredential(ctx, tx, p.Candidate.App, current.permit.Replica, prep.intent.Scope.CredentialVersion); err != nil {
		return scope, err
	}
	destination, err := json.Marshal(d.Epochs[0].Destination)
	if err != nil || sqliteHash(destination) != scope.TargetSHA256 || !reflect.DeepEqual(d.Definitions, matchingRemovalDefinitions(d, scope)) {
		return scope, ErrConflict
	}
	return scope, nil
}

func matchingRemovalDefinitions(d archive.SQLiteDatabase, scope sqliteprep.Scope) []data.SQLiteSchemaDefinition {
	for _, definition := range d.Definitions {
		if definition.Database == scope.DatabaseName && definition.Marker == scope.Schema.Marker && definition.CatalogSHA256 == scope.Schema.CatalogSHA256 {
			return d.Definitions
		}
	}
	if scope.Schema.Marker == data.EmptyMarker && (len(d.Definitions) == 0 || scope.EmptyAllocation != nil && !d.Written) {
		return d.Definitions
	}
	return nil
}

func guardRemovalEngines(ctx context.Context, q dataQuerier, app string) error {
	var count int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM data_engine_kinds k JOIN data_databases d ON d.id=k.database_id JOIN data_active_incarnations a ON a.incarnation_id=d.incarnation_id WHERE a.app=? AND k.engine!='sqlite'`, app).Scan(&count)
	if err != nil {
		return err
	}
	if count != 0 {
		return result.New(result.PersistentArchiveRequired, nil)
	}
	return nil
}
