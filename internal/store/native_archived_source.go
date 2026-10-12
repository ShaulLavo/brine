package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

// NativeArchivedSource retains only the original archive's read authority.
// Launch and lifetime locks remain process-owned; serialized values cannot
// admit repository reads, release the original fence or restart the engine.
type NativeArchivedSource struct {
	state       *Store
	archiveID   data.ArchiveID
	binding     data.ReplicaBindingID
	preparation *EnginePreparation
	lifetime    *data.EngineLock
}

func (*NativeArchivedSource) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

// AcquireNativeArchivePreparation uses the retained archive identity after its
// incarnation has retired. Ordinary live preparation still requires an active
// incarnation and cannot use this archive-only admission.
func (s *Store) AcquireNativeArchivePreparation(ctx context.Context, id data.ArchiveID, binding data.ReplicaBindingID, lifetime *data.EngineLock) (*EnginePreparation, error) {
	if s.readOnly || !data.ValidID(string(id)) || !lifetime.HoldsLifetime(s.dir, binding) {
		return nil, ErrConflict
	}
	guard, err := data.AcquireEngineLaunchGuard(ctx, s.dir, binding)
	if err != nil {
		return nil, err
	}
	preparation := &EnginePreparation{root: s.dir, binding: binding, guard: guard}
	if _, err = s.NativeArchivedSource(ctx, id, binding, preparation, lifetime); err != nil {
		return nil, errors.Join(err, preparation.Close())
	}
	return preparation, nil
}

func (s *Store) NativeArchivedSource(ctx context.Context, id data.ArchiveID, binding data.ReplicaBindingID, preparation *EnginePreparation, lifetime *data.EngineLock) (*NativeArchivedSource, error) {
	capability := &NativeArchivedSource{state: s, archiveID: id, binding: binding, preparation: preparation, lifetime: lifetime}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = checkNativeArchivedSourceTx(ctx, tx, capability); err != nil {
		return nil, err
	}
	return capability, tx.Commit()
}

func (c *NativeArchivedSource) active() bool {
	return c != nil && c.state != nil && !c.state.readOnly && c.preparation != nil && c.preparation.StateRoot() == c.state.dir && c.preparation.Active(c.binding) && c.lifetime.HoldsLifetime(c.state.dir, c.binding)
}

// ReadNativeArchiveFence describes the original retained fence without a live
// startup permit. Retired maintenance must not require an active incarnation.
func (s *Store) ReadNativeArchiveFence(ctx context.Context, id data.ArchiveID, binding data.ReplicaBindingID) (data.QuiescenceFence, error) {
	var zero data.QuiescenceFence
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := loadArchivePlanByArchive(ctx, tx, id)
	if err != nil || len(p.Candidate.Databases) != 1 {
		return zero, errors.Join(ErrConflict, err)
	}
	d, ok := p.Candidate.Databases[0].Native()
	if !ok || d.Binding.BindingID != binding {
		return zero, ErrConflict
	}
	m, err := readArchiveManifest(ctx, tx, id)
	if err != nil || !reflect.DeepEqual(m.Plan, p) || len(m.Coverage) != 1 {
		return zero, errors.Join(ErrConflict, err)
	}
	o, exists, err := readArchiveOperation(ctx, tx, p.ID)
	if err != nil || !exists || o.ID != m.OperationID || o.Stage != archive.Prepared {
		return zero, errors.Join(ErrConflict, err)
	}
	pointID, err := checkNativeArchivedClaimTx(ctx, tx, p, o, d)
	if err != nil {
		return zero, err
	}
	if err = checkNativeArchivedVerificationTx(ctx, tx, id, pointID, m.Coverage[0]); err != nil {
		return zero, err
	}
	point, err := readNativePreparedPointTx(ctx, tx, pointID)
	if err != nil {
		return zero, err
	}
	return data.QuiescenceFence{ID: point.view.FenceID, IncarnationID: d.Binding.IncarnationID, DatabaseID: d.Binding.DatabaseID, OperationID: point.view.FenceOwnerOpID, State: data.FenceHeld}, tx.Commit()
}

func checkNativeArchivedSourceTx(ctx context.Context, tx *sql.Tx, c *NativeArchivedSource) (string, error) {
	if tx == nil || !c.active() {
		return "", ErrConflict
	}
	p, err := loadArchivePlanByArchive(ctx, tx, c.archiveID)
	if err != nil || len(p.Candidate.Databases) != 1 {
		return "", errors.Join(ErrConflict, err)
	}
	d, ok := p.Candidate.Databases[0].Native()
	if !ok || d.Binding.BindingID != c.binding {
		return "", ErrConflict
	}
	m, err := readArchiveManifest(ctx, tx, c.archiveID)
	if err != nil || !reflect.DeepEqual(m.Plan, p) || len(m.Coverage) != 1 {
		return "", errors.Join(ErrConflict, err)
	}
	o, exists, err := readArchiveOperation(ctx, tx, p.ID)
	if err != nil || !exists || o.Stage != archive.Prepared || o.ID != m.OperationID {
		return "", errors.Join(ErrConflict, err)
	}
	pointID, err := checkNativeArchivedClaimTx(ctx, tx, p, o, d)
	if err != nil {
		return "", err
	}
	if err = checkNativeArchivedVerificationTx(ctx, tx, p.ArchiveID, pointID, m.Coverage[0]); err != nil {
		return "", err
	}
	disposition, exists, err := readArchiveDisposition(ctx, tx, c.archiveID)
	if err != nil || exists && (disposition.Stage == archive.AbandonIntent || disposition.Stage == archive.Abandoned) {
		return "", errors.Join(ErrConflict, err)
	}
	if exists && disposition.Stage == archive.Retired {
		if _, _, err = retiredArchive(ctx, tx, c.archiveID); err != nil {
			return "", err
		}
		if err = checkNativeArchiveDatabaseTreesMoved(ctx, tx, c.archiveID, d); err != nil {
			return "", err
		}
		deletion, err := readArchiveDeletion(ctx, tx, c.archiveID)
		if err != nil || deletion.Sequence != 0 {
			return "", errors.Join(ErrConflict, err)
		}
	}
	if !c.active() {
		return "", ErrConflict
	}
	return pointID, nil
}

func checkNativeArchivedVerificationTx(ctx context.Context, tx *sql.Tx, id data.ArchiveID, pointID string, coverage archive.Coverage) error {
	c, ok := coverage.Native()
	if !ok || c.Point.ID != pointID {
		return ErrConflict
	}
	point, err := readNativePreparedPointTx(ctx, tx, pointID)
	if err != nil || c.Point.SHA256 != point.hash || c.FenceID != point.view.FenceID || c.FenceOwnerOpID != point.view.FenceOwnerOpID {
		return errors.Join(ErrConflict, err)
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_archive_verifications WHERE archive_id=? AND point_id=?", id, pointID).Scan(&raw); err != nil {
		return err
	}
	var verified nativeArchiveVerification
	if json.Unmarshal(raw, &verified) != nil || verified.Format != "brine-native-archive-verification-v1" || verified.ArchiveID != id || verified.PointID != pointID || verified.PointSHA256 != point.hash || verified.Coverage != c {
		return &IntegrityError{}
	}
	canonical, err := json.Marshal(verified)
	if err != nil || !bytes.Equal(canonical, raw) {
		return &IntegrityError{}
	}
	return nil
}

func checkNativeArchivedClaimTx(ctx context.Context, tx *sql.Tx, p archive.Plan, o archive.Operation, d archive.NativeDatabase) (string, error) {
	var archiveOperation, task, pointID, owner string
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT archive_operation_id,task_operation_id,point_id,original_fence_owner,canonical FROM data_native_archive_claims WHERE archive_id=? AND binding_id=?", p.ArchiveID, d.Binding.BindingID).Scan(&archiveOperation, &task, &pointID, &owner, &raw)
	if err != nil || archiveOperation != o.ID || pointID != nativeArchivePointID(d) {
		return "", errors.Join(ErrConflict, err)
	}
	point, err := readNativePreparedPointTx(ctx, tx, pointID)
	if err != nil || point.view.FenceOwnerOpID != owner || point.view.BindingID != d.Binding.BindingID || point.view.EpochID != d.Head.EpochID || point.view.RuntimeVersion != d.Head.Version || point.view.RuntimeSHA256 != d.Head.RuntimeSHA256 {
		return "", errors.Join(ErrConflict, err)
	}
	consumer := nativePointConsumer{kind: nativePointRetire, pointID: pointID, planID: p.ID, applyOperationID: task, requester: p.Requester, binding: d.Binding.BindingID, epoch: d.Head.EpochID, generation: p.Generation, generationID: point.view.GenerationID, policySHA256: strings.TrimPrefix(p.PolicyHash, "sha256:")}
	expected, err := consumer.canonical()
	if err != nil || !bytes.Equal(raw, expected) {
		return "", &IntegrityError{}
	}
	actual, err := readNativePointConsumer(ctx, tx, pointID)
	if err != nil || !bytes.Equal(actual, expected) {
		return "", errors.Join(ErrConflict, err)
	}
	var held int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_archive_fences a JOIN data_fences f ON f.id=a.fence_id WHERE a.operation_id=? AND a.database_id=? AND f.id=? AND f.operation_id=? AND f.state='held'", o.ID, d.Binding.DatabaseID, point.view.FenceID, owner).Scan(&held)
	if err != nil || held != 1 {
		return "", errors.Join(ErrConflict, err)
	}
	return pointID, nil
}
