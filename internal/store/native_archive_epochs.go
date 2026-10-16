package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
)

// ReadEngineArchiveBindings retains the initial destination and every reserved
// successor, including unfinished transitions. It grants no deletion authority.
func (s *Store) ReadEngineArchiveBindings(ctx context.Context, binding data.ReplicaBindingID) ([]data.ArchiveBinding, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	bindings, err := readEngineArchiveBindings(ctx, tx, binding)
	if err != nil {
		return nil, err
	}
	return bindings, tx.Commit()
}

func readEngineArchiveBindings(ctx context.Context, q *sql.Tx, binding data.ReplicaBindingID) ([]data.ArchiveBinding, error) {
	reserved, err := readReservedEngine(ctx, q, binding)
	if err != nil {
		return nil, err
	}
	epochs, err := nativeArchiveEpochIDs(ctx, q, binding)
	if err != nil {
		return nil, err
	}
	bindings := make([]data.ArchiveBinding, 1, len(epochs)+1)
	bindings[0] = reserved.Archive
	for _, epoch := range epochs {
		reservation, archive, err := readNativeEpoch(ctx, q, binding, epoch)
		if err != nil || archive.Validate(reserved.Database) != nil || archive.EpochID == reserved.Archive.EpochID {
			return nil, errors.Join(&IntegrityError{}, err)
		}
		transition, _, err := readEngineTransition(ctx, q, reservation.Record().TransitionID)
		if err != nil || !reservation.Matches(transition) || !transitionReservedIdentity(transition, reserved) {
			return nil, errors.Join(&IntegrityError{}, err)
		}
		bindings = append(bindings, archive)
	}
	abandoned, err := readNativeArchiveAbandonedRepositories(ctx, q, binding)
	if err != nil {
		return nil, err
	}
	for _, intent := range abandoned {
		bindings = append(bindings, intent.archive)
	}
	slices.SortFunc(bindings, func(a, b data.ArchiveBinding) int { return strings.Compare(string(a.EpochID), string(b.EpochID)) })
	return bindings, nil
}

func nativeArchiveEpochIDs(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID) ([]data.ReplicaEpochID, error) {
	rows, err := q.QueryContext(ctx, "SELECT epoch_id FROM data_native_epochs WHERE binding_id=? ORDER BY epoch_id", binding)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var epochs []data.ReplicaEpochID
	for rows.Next() {
		var epoch data.ReplicaEpochID
		if err = rows.Scan(&epoch); err != nil {
			return nil, err
		}
		epochs = append(epochs, epoch)
	}
	return epochs, rows.Err()
}
