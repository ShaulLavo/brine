package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"slices"
	"strings"
)

func readNativeArchiveDatabase(ctx context.Context, tx *sql.Tx, id data.DatabaseID, owner string) (archive.NativeDatabase, error) {
	var zero archive.NativeDatabase
	var binding data.ReplicaBindingID
	if err := tx.QueryRowContext(ctx, "SELECT id FROM data_replica_bindings WHERE database_id=?", id).Scan(&binding); err != nil {
		return zero, err
	}
	permit, err := readEnginePermit(ctx, tx, binding)
	if err != nil || permit.Runtime.State != data.EngineInitialized && permit.Runtime.State != data.EngineUnpublished {
		return zero, ErrConflict
	}
	if permit.Runtime.State == data.EngineUnpublished && checkNativeUntouchedHistory(ctx, tx, permit.Runtime) != nil {
		return zero, ErrConflict
	}
	head, _, err := readEngineHead(ctx, tx, binding)
	if err != nil {
		return zero, err
	}
	allocation := permit.Allocation
	if allocation == nil {
		initial, err := readInitialEngineAllocation(ctx, tx, permit.Reserved)
		if err != nil {
			return zero, err
		}
		allocation = &initial
	}
	point, err := readNativeArchiveCurrentPoint(ctx, tx, head, owner)
	if err != nil {
		return zero, err
	}
	if _, err = readNativePreparedBundleTx(ctx, tx, point.view.PointID); err != nil {
		return zero, err
	}
	epochs, err := readEngineArchiveBindings(ctx, tx, binding)
	if err != nil {
		return zero, err
	}
	closure, err := readNativeArchiveTransitionClosureTx(ctx, tx, binding)
	if err != nil {
		return zero, err
	}
	dependencies, err := readNativeArchiveDependencies(ctx, tx, permit, point, epochs, closure)
	if err != nil {
		return zero, err
	}
	trees := make([]archive.NativeTransitionTree, len(closure))
	for i, t := range closure {
		trees[i] = archive.NativeTransitionTree{TransitionID: t.Input.Common().Scope.ID, Stage: t.Cursor.Stage}
	}
	return archive.NativeDatabase{Binding: permit.Reserved.Database, Head: head, State: permit.Runtime.State, Allocation: allocation, Epochs: epochs, Retention: []data.RetentionEvidence{}, Dependencies: dependencies, TransitionTrees: trees}, nil
}

func readNativeArchiveCurrentPoint(ctx context.Context, tx *sql.Tx, head data.EngineHeadRef, owner string) (nativePreparedPoint, error) {
	var id string
	if owner == "" {
		if err := tx.QueryRowContext(ctx, "SELECT id FROM data_native_prepared_points WHERE binding_id=? AND epoch_id=? ORDER BY json_extract(canonical,'$.prepared_at') DESC LIMIT 1", head.BindingID, head.EpochID).Scan(&id); err != nil {
			return nativePreparedPoint{}, err
		}
	} else {
		if err := tx.QueryRowContext(ctx, "SELECT point_id FROM data_native_archive_claims WHERE archive_operation_id=? AND binding_id=?", owner, head.BindingID).Scan(&id); err != nil {
			return nativePreparedPoint{}, err
		}
	}
	point, err := readNativePreparedPointTx(ctx, tx, id)
	if err != nil || point.view.RuntimeVersion != head.Version || point.view.RuntimeSHA256 != head.RuntimeSHA256 || point.view.EpochID != head.EpochID || checkNativePointCurrent(ctx, tx, point.view) != nil {
		return nativePreparedPoint{}, ErrConflict
	}
	if owner == "" {
		if _, err = readNativePointConsumer(ctx, tx, id); err == nil {
			return nativePreparedPoint{}, ErrConflict
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nativePreparedPoint{}, err
		}
	}
	return point, nil
}

func readNativeArchiveDependencies(ctx context.Context, tx *sql.Tx, permit EnginePermit, point nativePreparedPoint, epochs []data.ArchiveBinding, closure []NativeArchiveTransition) ([]data.EngineEvidenceRef, error) {
	refs := []data.EngineEvidenceRef{{Kind: data.EnginePreparedPointEvidence, ID: point.view.PointID, SHA256: point.hash, DatabaseID: point.view.DatabaseID, BindingID: point.view.BindingID, EpochID: point.view.EpochID}}
	seen := map[string]bool{point.view.PointID: true}
	for _, transition := range closure {
		refs = appendNativeArchiveTransitionDependencies(refs, seen, transition)
	}
	// Every verified bundle retains its exact signing/recovery versions, module
	// and schema references even when its data epoch is no longer current.
	rows, err := tx.QueryContext(ctx, "SELECT id FROM data_native_verified_points WHERE binding_id=? ORDER BY id", permit.Runtime.BindingID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, id := range ids {
		b, err := readNativeVerifiedPointBundleTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if !seen[id] {
			refs = append(refs, data.EngineEvidenceRef{Kind: data.EngineDependenciesEvidence, ID: id, SHA256: b.dependencies.hash, DatabaseID: b.point.view.DatabaseID, BindingID: b.point.view.BindingID, EpochID: b.point.view.EpochID})
			seen[id] = true
		}
	}
	for _, epoch := range epochs {
		if epoch.Validate(permit.Reserved.Database) != nil {
			return nil, &IntegrityError{}
		}
	}
	slices.SortFunc(refs, func(a, b data.EngineEvidenceRef) int { return strings.Compare(a.ID, b.ID) })
	return refs, nil
}

func nativeArchivePointID(d archive.NativeDatabase) string {
	for _, ref := range d.Dependencies {
		if ref.Kind == data.EnginePreparedPointEvidence && ref.EpochID == d.Head.EpochID {
			return ref.ID
		}
	}
	return ""
}

func nativeArchiveCandidatesMatch(a, b archive.NativeDatabase) bool {
	a.Retention, b.Retention = nil, nil
	a.TransitionTrees = slices.Clone(a.TransitionTrees)
	b.TransitionTrees = slices.Clone(b.TransitionTrees)
	if len(a.TransitionTrees) != len(b.TransitionTrees) {
		return false
	}
	for i := range a.TransitionTrees {
		a.TransitionTrees[i].Seal = data.EngineTreeSeal{}
		b.TransitionTrees[i].Seal = data.EngineTreeSeal{}
	}
	ar, _ := json.Marshal(a)
	br, _ := json.Marshal(b)
	return string(ar) == string(br)
}

func appendNativeArchiveTransitionDependencies(refs []data.EngineEvidenceRef, seen map[string]bool, transition NativeArchiveTransition) []data.EngineEvidenceRef {
	c := transition.Input.Common()
	for _, ref := range []data.EngineEvidenceRef{c.TargetMaterial, c.RetainedDependencies, c.TargetCheckpoint} {
		if seen[ref.ID] {
			continue
		}
		seen[ref.ID] = true
		refs = append(refs, ref)
	}
	return refs
}
