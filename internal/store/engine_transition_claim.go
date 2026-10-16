package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

// insertEngineTransitionTx is the storage half of the protected 17/18 claim.
// Its caller must join the actual point consumer and target proof in this tx.
func (s *Store) insertEngineTransitionTx(ctx context.Context, tx *sql.Tx, preparation *EnginePreparation, input data.EngineTransition, repositoryID string) (data.EngineTransitionCursor, error) {
	var zero data.EngineTransitionCursor
	if !s.transitionGuard(preparation, input) {
		return zero, ErrConflict
	}
	if err := checkEngineTransitionFence(ctx, tx, input); err != nil {
		return zero, err
	}
	if err := checkTransitionOperation(ctx, tx, input); err != nil {
		return zero, err
	}
	return s.insertGuardedEngineTransitionTx(ctx, tx, preparation, input, repositoryID)
}

func (s *Store) insertGuardedEngineTransitionTx(ctx context.Context, tx *sql.Tx, preparation *EnginePreparation, input data.EngineTransition, repositoryID string) (data.EngineTransitionCursor, error) {
	var zero data.EngineTransitionCursor
	if !s.transitionGuard(preparation, input) {
		return zero, ErrConflict
	}
	if err := checkEngineTransitionFence(ctx, tx, input); err != nil {
		return zero, err
	}
	if current, cursor, err := readEngineTransition(ctx, tx, input.Common().Scope.ID); err == nil {
		if current.SHA256() != input.SHA256() {
			return zero, ErrConflict
		}
		return cursor, nil
	} else if !errors.Is(err, ErrNotFound) {
		return zero, err
	}
	reserved, err := readReservedEngine(ctx, tx, input.Common().Scope.BindingID)
	if err != nil || !transitionReservedIdentity(input, reserved) {
		return zero, errors.Join(ErrConflict, err)
	}
	if err = checkTransitionBase(ctx, tx, input, reserved); err != nil {
		return zero, err
	}
	archive := reserved.Archive
	archive.EpochID = input.Common().NewEpochID
	archive.RemotePrefix, err = data.RemotePrefix(archive.Destination.BasePrefix, reserved.Database.IncarnationID, reserved.Database.DatabaseID, archive.EpochID)
	if err != nil || archive.Validate(reserved.Database) != nil {
		return zero, ErrInvalid
	}
	if err = checkFreshNativePrefix(ctx, tx, archive); err != nil {
		return zero, err
	}
	if err = checkPendingNativeTransitionRepository(ctx, tx, archive, input, repositoryID); err != nil {
		return zero, err
	}
	archiveRaw, _ := json.Marshal(archive)
	epoch, err := data.NewNativeEpoch(input, nativeCanonicalHash(archiveRaw), repositoryID, time.Now().UTC())
	if err != nil {
		return zero, ErrInvalid
	}
	event, err := data.PrepareEngineTransition(input)
	if err != nil {
		return zero, err
	}
	if err = insertEngineTransitionRecords(ctx, tx, input, epoch, archive, archiveRaw, event); err != nil {
		return zero, err
	}
	return event.Cursor(), nil
}

func checkTransitionOperation(ctx context.Context, q dataQuerier, input data.EngineTransition) error {
	c := input.Common()
	var requester, plan, state string
	err := q.QueryRowContext(ctx, "SELECT requester,COALESCE(secret_ref,''),state FROM operations WHERE id=?", c.Scope.ApplyOperationID).Scan(&requester, &plan, &state)
	if err != nil || requester != c.Scope.Requester || plan != c.Scope.PlanID || state != "preflight" || !time.Now().UTC().Before(c.Scope.ExpiresAt) {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func transitionReservedIdentity(input data.EngineTransition, reserved ReservedEngine) bool {
	c := input.Common()
	return c.Scope.BindingID == reserved.Database.BindingID && c.Scope.DatabaseID == reserved.Database.DatabaseID && c.Scope.IncarnationID == reserved.Database.IncarnationID
}

func checkTransitionBase(ctx context.Context, q dataQuerier, input data.EngineTransition, reserved ReservedEngine) error {
	base := input.Common().Base
	head, runtime, err := readEngineHead(ctx, q, reserved.Database.BindingID)
	if expected, committed := base.Committed(); committed {
		if err != nil || head != expected || runtime.State != data.EngineInitialized {
			return errors.Join(ErrConflict, err)
		}
		return nil
	}
	if err != nil || base.Kind() != "unpublished" || runtime.State != data.EngineUnpublished || runtime.Database != "" || runtime.Publisher != "" {
		return ErrConflict
	}
	initial, _ := base.Unpublished()
	if head != initial.Head || initial.Head.EpochID != reserved.Archive.EpochID || initial.Allocation.ID != string(reserved.Database.DatabaseID) {
		return ErrConflict
	}
	allocation, err := readInitialEngineAllocation(ctx, q, reserved)
	raw, marshalErr := json.Marshal(allocation)
	if err != nil || marshalErr != nil || initial.Allocation.SHA256 != nativeCanonicalHash(raw) {
		return errors.Join(ErrConflict, err, marshalErr)
	}
	return err
}

func checkFreshNativePrefix(ctx context.Context, q dataQuerier, archive data.ArchiveBinding) error {
	rows, err := q.QueryContext(ctx, "SELECT prefix FROM data_replica_bindings WHERE endpoint=? AND bucket=? UNION ALL SELECT prefix FROM data_native_epochs WHERE endpoint=? AND bucket=?", archive.Destination.Endpoint, archive.Destination.Bucket, archive.Destination.Endpoint, archive.Destination.Bucket)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var prefix string
		if err = rows.Scan(&prefix); err != nil {
			return err
		}
		if nativePrefixesOverlap(prefix, archive.RemotePrefix) {
			return ErrConflict
		}
	}
	return rows.Err()
}

func insertEngineTransitionRecords(ctx context.Context, tx *sql.Tx, input data.EngineTransition, epoch data.NativeEpoch, archive data.ArchiveBinding, archiveRaw []byte, event data.EngineTransitionEvent) error {
	c := input.Common()
	_, err := tx.ExecContext(ctx, "INSERT INTO data_engine_transition_inputs VALUES(?,?,?,?,?,?)", c.Scope.ID, c.Scope.ApplyOperationID, c.PreparedPoint.ID, c.Scope.BindingID, c.FenceID, input.Bytes())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_epochs VALUES(?,?,?,?,?,?,?,?,?)", archive.EpochID, archive.BindingID, epoch.Record().PredecessorEpochID, c.Scope.ID, archive.Destination.Endpoint, archive.Destination.Bucket, archive.RemotePrefix, archiveRaw, epoch.Bytes())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_engine_transition_events VALUES(?,?,?,?)", c.Scope.ID, event.Cursor().Sequence, event.Cursor().Stage, event.Bytes())
	return err
}

func transitionHeadMatches(ctx context.Context, q dataQuerier, input data.EngineTransition) error {
	reserved, err := readReservedEngine(ctx, q, input.Common().Scope.BindingID)
	if err != nil {
		return err
	}
	return checkTransitionBase(ctx, q, input, reserved)
}
