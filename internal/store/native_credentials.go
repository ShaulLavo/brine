package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

type NativeCredentialScope struct {
	App        string
	PolicyHash string
	Database   data.SpacetimeDBBinding
	Archive    data.ArchiveBinding
	FenceHeld  bool
}

func (s *Store) AppDatabaseEngine(ctx context.Context, app, database string) (data.EngineKind, error) {
	if !nativePointLogicalName.MatchString(app) || database != "" && !nativePointLogicalName.MatchString(database) {
		return "", ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, "SELECT COALESCE(k.engine,'sqlite') FROM data_active_incarnations a JOIN data_databases d ON d.incarnation_id=a.incarnation_id LEFT JOIN data_engine_kinds k ON k.database_id=d.id WHERE a.app=? AND (?='' OR d.name=?)", app, database, database)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var kind data.EngineKind
	count := 0
	for rows.Next() {
		if err := rows.Scan(&kind); err != nil {
			return "", err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if count != 1 || kind != data.EngineSQLite && kind != data.EngineSpacetimeDB {
		return "", ErrConflict
	}
	return kind, nil
}
func (s *Store) NativeCredentialScope(ctx context.Context, app, database string) (NativeCredentialScope, error) {
	var binding data.ReplicaBindingID
	err := s.db.QueryRowContext(ctx, "SELECT b.id FROM data_active_incarnations a JOIN data_databases d ON d.incarnation_id=a.incarnation_id JOIN data_replica_bindings b ON b.database_id=d.id JOIN data_engine_kinds k ON k.database_id=d.id WHERE a.app=? AND (?='' OR d.name=?) AND k.engine='spacetimedb'", app, database, database).Scan(&binding)
	if err != nil {
		return NativeCredentialScope{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeCredentialScope{}, err
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := readNativeCredentialScope(ctx, tx, binding)
	if err != nil || scope.App != app || database != "" && string(scope.Database.Name) != database {
		return NativeCredentialScope{}, errors.Join(ErrConflict, err)
	}
	return scope, tx.Commit()
}
func readNativeCredentialScope(ctx context.Context, tx *sql.Tx, binding data.ReplicaBindingID) (NativeCredentialScope, error) {
	reserved, err := readReservedEngine(ctx, tx, binding)
	if err != nil {
		return NativeCredentialScope{}, err
	}
	head, _, err := readEngineHead(ctx, tx, binding)
	if err == nil && head.EpochID != reserved.Archive.EpochID {
		_, reserved.Archive, err = readNativeEpoch(ctx, tx, binding, head.EpochID)
		if err != nil {
			return NativeCredentialScope{}, err
		}
	} else if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
		return NativeCredentialScope{}, err
	}
	var app, policy string
	var held int
	err = tx.QueryRowContext(ctx, "SELECT a.app,(SELECT policy_hash FROM data_admissions WHERE database_id=? ORDER BY version DESC LIMIT 1),(SELECT COUNT(*) FROM data_fences WHERE database_id=? AND state='held') FROM data_active_incarnations a WHERE a.incarnation_id=?", reserved.Database.DatabaseID, reserved.Database.DatabaseID, reserved.Database.IncarnationID).Scan(&app, &policy, &held)
	if err != nil {
		return NativeCredentialScope{}, err
	}
	return NativeCredentialScope{App: app, PolicyHash: policy, Database: reserved.Database, Archive: reserved.Archive, FenceHeld: held != 0}, nil
}
func (s *Store) SaveNativeCredentialRecord(ctx context.Context, r CredentialRecord) error {
	if s.readOnly || r.Requester == "" || len(r.Requester) > 256 || !data.ValidID(r.ID) || !data.ValidID(string(r.BindingID)) || !data.ValidID(string(r.EpochID)) || r.Kind != "plan" && r.Kind != "receipt" || !digestPattern.MatchString(r.PlanID) || r.PlanHash != r.PlanID || !digestPattern.MatchString(r.PolicyHash) || !digestPattern.MatchString(r.TargetHash) || r.Version == 0 || r.Kind == "receipt" && r.ReceivedAt.IsZero() {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := readNativeCredentialScope(ctx, tx, r.BindingID)
	if err != nil || scope.FenceHeld || scope.App != r.App || scope.PolicyHash != r.PolicyHash || scope.Archive.EpochID != r.EpochID || scope.Archive.Destination.CredentialRef != r.CredentialRef || scope.Archive.Destination.Reference != r.Destination {
		return errors.Join(ErrConflict, err)
	}
	raw, _ := json.Marshal(r)
	if _, err := tx.ExecContext(ctx, "INSERT INTO data_credential_records VALUES(?,?,?,?)", r.ID, r.BindingID, r.Kind, raw); err != nil {
		return err
	}
	return tx.Commit()
}
