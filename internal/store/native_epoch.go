package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
)

func nativeCanonicalHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func readEngineHead(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID) (data.EngineHeadRef, data.EngineRuntime, error) {
	var head data.EngineHeadRef
	var runtime data.EngineRuntime
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT version,canonical FROM data_engine_runtime_commits WHERE binding_id=? ORDER BY version DESC LIMIT 1", binding).Scan(&head.Version, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return head, runtime, ErrNotFound
	}
	if err != nil {
		return head, runtime, err
	}
	if len(raw) > 65536 || json.Unmarshal(raw, &runtime) != nil || runtime.Validate() != nil || runtime.BindingID != binding {
		return head, runtime, &IntegrityError{}
	}
	canonical, _ := json.Marshal(runtime)
	if !bytes.Equal(canonical, raw) {
		return head, runtime, &IntegrityError{}
	}
	head.BindingID, head.EpochID, head.RuntimeSHA256 = binding, runtime.EpochID, nativeCanonicalHash(raw)
	if head.Validate() != nil {
		return head, runtime, &IntegrityError{}
	}
	return head, runtime.Clone(), nil
}

func (s *Store) ReadEngineHead(ctx context.Context, binding data.ReplicaBindingID) (data.EngineHeadRef, data.EngineRuntime, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return data.EngineHeadRef{}, data.EngineRuntime{}, err
	}
	defer func() { _ = tx.Rollback() }()
	head, runtime, err := readEngineHead(ctx, tx, binding)
	if err != nil {
		return head, runtime, err
	}
	return head, runtime, tx.Commit()
}

func readNativeEpoch(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID, epoch data.ReplicaEpochID) (data.NativeEpoch, data.ArchiveBinding, error) {
	var reservation data.NativeEpoch
	var archive data.ArchiveBinding
	var raw, archiveRaw []byte
	var predecessor data.ReplicaEpochID
	var transition, endpoint, bucket, prefix string
	err := q.QueryRowContext(ctx, "SELECT predecessor_epoch_id,transition_id,endpoint,bucket,prefix,archive_canonical,canonical FROM data_native_epochs WHERE epoch_id=? AND binding_id=?", epoch, binding).Scan(&predecessor, &transition, &endpoint, &bucket, &prefix, &archiveRaw, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return reservation, archive, ErrNotFound
	}
	if err != nil {
		return reservation, archive, err
	}
	reservation, err = data.DecodeNativeEpoch(raw)
	if err != nil || len(archiveRaw) > 16384 || json.Unmarshal(archiveRaw, &archive) != nil {
		return reservation, archive, &IntegrityError{}
	}
	r := reservation.Record()
	canonical, _ := json.Marshal(archive)
	if !bytes.Equal(raw, reservation.Bytes()) || !bytes.Equal(archiveRaw, canonical) || r.ArchiveBindingSHA256 != nativeCanonicalHash(archiveRaw) ||
		r.BindingID != binding || r.EpochID != epoch || r.PredecessorEpochID != predecessor || r.TransitionID != transition ||
		archive.BindingID != binding || archive.EpochID != epoch || archive.Destination.Endpoint != endpoint || archive.Destination.Bucket != bucket || archive.RemotePrefix != prefix {
		return reservation, archive, &IntegrityError{}
	}
	return reservation, archive, nil
}

func checkNativeDestinationOwner(ctx context.Context, q dataQuerier, endpoint, bucket, prefix string, owner data.ReplicaBindingID) error {
	rows, err := q.QueryContext(ctx, "SELECT binding_id,prefix FROM data_native_epochs WHERE endpoint=? AND bucket=? UNION ALL SELECT binding_id,prefix FROM data_native_repository_intents WHERE endpoint=? AND bucket=?", endpoint, bucket, endpoint, bucket)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var binding data.ReplicaBindingID
		var recorded string
		if err = rows.Scan(&binding, &recorded); err != nil {
			return err
		}
		if binding != owner && nativePrefixesOverlap(prefix, recorded) {
			return ErrConflict
		}
	}
	return rows.Err()
}

func nativePrefixesOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

func admitInitialEngineCommit(ctx context.Context, q dataQuerier, reserved ReservedEngine) error {
	head, _, err := readEngineHead(ctx, q, reserved.Database.BindingID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil || head.EpochID != reserved.Archive.EpochID {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
