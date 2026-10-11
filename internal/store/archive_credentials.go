package store

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
)

func validateArchiveCredentialScope(ctx context.Context, q dataQuerier, scope backupcredentials.ArchiveScope, requester string) error {
	if !scope.Valid() {
		return ErrInvalid
	}
	p, d, err := retiredArchive(ctx, q, data.ArchiveID(scope.ArchiveID))
	if err != nil || p.Requester != requester || p.TargetHash != scope.TargetHash || !scope.RetainUntil.Equal(d.RetainUntil) {
		return ErrConflict
	}
	manifest, err := readArchiveManifest(ctx, q, p.ArchiveID)
	if err != nil {
		return err
	}
	for _, database := range p.Candidate.Databases {
		for _, epoch := range database.Epochs {
			if string(epoch.BindingID) != scope.Binding || string(epoch.EpochID) != scope.Epoch {
				continue
			}
			if string(epoch.Destination.Reference) != scope.Destination || epoch.Destination.CredentialRef != scope.CredentialRef {
				return ErrConflict
			}
			var held int
			if err = q.QueryRowContext(ctx, "SELECT count(*) FROM data_archive_fences af JOIN data_fences f ON af.fence_id=f.id WHERE af.operation_id=? AND af.database_id=? AND f.state='held' AND f.operation_id=?", manifest.OperationID, database.Binding.DatabaseID, manifest.OperationID).Scan(&held); err != nil {
				return err
			}
			if held != 1 {
				return ErrConflict
			}
			return nil
		}
	}
	return ErrConflict
}
func loadArchiveCredentialPlan(ctx context.Context, q dataQuerier, id string) (out backupcredentials.ArchivePlan, err error) {
	if !digestPattern.MatchString(id) {
		return out, ErrInvalid
	}
	var raw []byte
	var archiveID string
	if err = q.QueryRowContext(ctx, "SELECT archive_id,canonical FROM data_archive_credential_plans WHERE id=?", id).Scan(&archiveID, &raw); err != nil {
		return out, err
	}
	if len(raw) > 8192 || json.Unmarshal(raw, &out) != nil || !out.Valid() || out.ID != id || out.Scope.ArchiveID != archiveID {
		return out, &IntegrityError{}
	}
	canonical, err := json.Marshal(out)
	if err != nil || !bytes.Equal(canonical, raw) || validateArchiveCredentialScope(ctx, q, out.Scope, out.Requester) != nil {
		return out, &IntegrityError{}
	}
	return out, nil
}
func (s *Store) LoadArchiveCredentialPlan(ctx context.Context, id string) (backupcredentials.ArchivePlan, error) {
	return loadArchiveCredentialPlan(ctx, s.db, id)
}
func (s *Store) RecordArchiveCredentialPlan(ctx context.Context, p backupcredentials.ArchivePlan) error {
	if !p.Valid() || !time.Now().Before(p.Scope.RetainUntil) {
		return ErrInvalid
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 8192 {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = validateArchiveCredentialScope(ctx, tx, p.Scope, p.Requester); err != nil {
		return err
	}
	deletion, err := readArchiveDeletion(ctx, tx, data.ArchiveID(p.Scope.ArchiveID))
	if err != nil || deletion.Sequence != 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_credential_plans VALUES(?,?,?) ON CONFLICT(id) DO NOTHING", p.ID, p.Scope.ArchiveID, raw); err != nil {
		return err
	}
	stored, err := loadArchiveCredentialPlan(ctx, tx, p.ID)
	if err != nil || !reflect.DeepEqual(stored, p) {
		return ErrConflict
	}
	return tx.Commit()
}
func (s *Store) RecordArchiveCredentialReceipt(ctx context.Context, r backupcredentials.ArchiveReceipt) error {
	if !r.Valid() || r.ReceivedAt.After(time.Now().UTC()) || r.Admit(time.Now().UTC(), 0) != nil {
		return ErrInvalid
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > 8192 {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := loadArchiveCredentialPlan(ctx, tx, r.Plan.ID)
	if err != nil || !reflect.DeepEqual(p, r.Plan) {
		return ErrConflict
	}
	deletion, err := readArchiveDeletion(ctx, tx, data.ArchiveID(p.Scope.ArchiveID))
	if err != nil || deletion.Sequence != 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_credential_receipts VALUES(?,?,?,?,?,?) ON CONFLICT(plan_id) DO NOTHING", p.ID, p.Scope.ArchiveID, p.Scope.Binding, p.Scope.Epoch, p.Version, raw); err != nil {
		return err
	}
	var stored []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_archive_credential_receipts WHERE plan_id=?", p.ID).Scan(&stored); err != nil {
		return err
	}
	if !bytes.Equal(raw, stored) {
		return ErrConflict
	}
	return tx.Commit()
}

// ReadArchiveCredentialReceipt selects only the original archive/binding/epoch;
// no current app name, current epoch, or ordinary credential ref is consulted.
func (s *Store) ReadArchiveCredentialReceipt(ctx context.Context, id data.ArchiveID, binding data.ReplicaBindingID, epoch data.ReplicaEpochID) (out backupcredentials.ArchiveReceipt, err error) {
	if !data.ValidID(string(id)) || !data.ValidID(string(binding)) || !data.ValidID(string(epoch)) {
		return out, ErrInvalid
	}
	var raw []byte
	var planID string
	var version uint64
	if err = s.db.QueryRowContext(ctx, "SELECT plan_id,version,canonical FROM data_archive_credential_receipts WHERE archive_id=? AND binding_id=? AND epoch_id=? ORDER BY version DESC LIMIT 1", id, binding, epoch).Scan(&planID, &version, &raw); err != nil {
		return out, err
	}
	if len(raw) > 8192 || json.Unmarshal(raw, &out) != nil || !out.Valid() || out.Plan.ID != planID || out.Plan.Version != version || out.Plan.Scope.ArchiveID != string(id) || out.Plan.Scope.Binding != string(binding) || out.Plan.Scope.Epoch != string(epoch) {
		return out, &IntegrityError{}
	}
	p, err := loadArchiveCredentialPlan(ctx, s.db, planID)
	if err != nil || !reflect.DeepEqual(p, out.Plan) {
		return out, &IntegrityError{}
	}
	canonical, err := json.Marshal(out)
	if err != nil || !bytes.Equal(canonical, raw) {
		return out, &IntegrityError{}
	}
	return out, nil
}

var _ backupcredentials.ArchiveJournal = (*Store)(nil)
