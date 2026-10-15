package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativematerial"
)

var ErrMaterialRequiresStop = errors.New("material activation requires a reviewed full stop")
var ErrMaterialLeaseExpired = errors.New("selected material lease expired")

func migrateNativeMaterialActivation(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE data_native_material_activation_plans(id TEXT PRIMARY KEY,canonical BLOB NOT NULL CHECK(length(canonical)<=16384));
 CREATE TABLE data_native_material_activations(plan_id TEXT PRIMARY KEY REFERENCES data_native_material_activation_plans(id),binding_id TEXT NOT NULL,version INTEGER NOT NULL,canonical BLOB NOT NULL,UNIQUE(binding_id,version));
 CREATE TRIGGER data_native_material_activation_plans_no_update BEFORE UPDATE ON data_native_material_activation_plans BEGIN SELECT RAISE(ABORT,'immutable activation plan'); END;
 CREATE TRIGGER data_native_material_activation_plans_no_delete BEFORE DELETE ON data_native_material_activation_plans BEGIN SELECT RAISE(ABORT,'retained activation plan'); END;
 CREATE TRIGGER data_native_material_activations_no_update BEFORE UPDATE ON data_native_material_activations BEGIN SELECT RAISE(ABORT,'immutable activation receipt'); END;
 CREATE TRIGGER data_native_material_activations_no_delete BEFORE DELETE ON data_native_material_activations BEGIN SELECT RAISE(ABORT,'retained activation receipt'); END;
 UPDATE schema_version SET version=30;`)
	return err
}

func (s *Store) SaveNativeMaterialActivationPlan(ctx context.Context, app, delivery, requester string) (nativematerial.ActivationPlan, error) {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nativematerial.ActivationPlan{}, err
	}
	defer func() { _ = tx.Rollback() }()
	input, err := readNativeMaterial(ctx, tx, delivery)
	if err != nil || input.Initial || input.Requester != requester || input.Receipt.App != app {
		return nativematerial.ActivationPlan{}, ErrConflict
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_material_receipts WHERE operation_id=?", delivery).Scan(&raw); err != nil {
		return nativematerial.ActivationPlan{}, ErrConflict
	}
	receipt, err := nativematerial.DecodeReceipt(raw)
	if err != nil || receipt != input.Receipt {
		return nativematerial.ActivationPlan{}, ErrConflict
	}
	head, runtime, err := readEngineHead(ctx, tx, input.Receipt.BindingID)
	if err != nil || runtime.State != data.EngineInitialized || runtime.EpochID != input.Receipt.EpochID || !sameNativeMaterialRuntime(runtime, input.Runtime) {
		return nativematerial.ActivationPlan{}, ErrConflict
	}
	wanted, err := readManagedDesired(ctx, tx, head.BindingID)
	if err != nil {
		return nativematerial.ActivationPlan{}, err
	}
	now := time.Now().UTC()
	id, err := data.NewID()
	if err != nil {
		return nativematerial.ActivationPlan{}, err
	}
	p := nativematerial.ActivationPlan{ID: id, App: app, Requester: requester, DeliveryOperationID: delivery, Head: head, DesiredRevision: wanted.Revision, PolicyHash: input.PolicyHash, Secrets: receipt.Secrets, RestartRequired: runtime.Secrets.SigningPrivate != receipt.Secrets.SigningPrivate || runtime.Secrets.SigningPublic != receipt.Secrets.SigningPublic || runtime.Secrets.AuthPolicy != receipt.Secrets.AuthPolicy, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	if err = checkMaterialActivation(ctx, tx, p); err != nil {
		return p, err
	}
	raw, _ = json.Marshal(p)
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_material_activation_plans VALUES(?,?)", id, raw); err != nil {
		return p, err
	}
	return p, tx.Commit()
}
func readMaterialActivationPlan(ctx context.Context, q dataQuerier, id string) (nativematerial.ActivationPlan, error) {
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT canonical FROM data_native_material_activation_plans WHERE id=?", id).Scan(&raw); err != nil {
		return nativematerial.ActivationPlan{}, err
	}
	p, err := nativematerial.DecodeActivationPlan(raw)
	canonical, _ := json.Marshal(p)
	if err != nil || p.ID != id || !bytes.Equal(raw, canonical) {
		return p, &IntegrityError{}
	}
	return p, nil
}
func (s *Store) ReadNativeMaterialActivationPlan(ctx context.Context, id string) (nativematerial.ActivationPlan, error) {
	return readMaterialActivationPlan(ctx, s.db, id)
}
func checkMaterialActivation(ctx context.Context, tx *sql.Tx, p nativematerial.ActivationPlan) error {
	if !p.Valid() || !time.Now().UTC().Before(p.ExpiresAt) {
		return ErrConflict
	}
	input, err := readNativeMaterial(ctx, tx, p.DeliveryOperationID)
	if err != nil || input.Initial || input.Requester != p.Requester || input.Receipt.App != p.App || input.Runtime.Secrets != p.Secrets || input.PolicyHash != p.PolicyHash {
		return ErrConflict
	}
	head, runtime, err := readEngineHead(ctx, tx, p.Head.BindingID)
	if err != nil || head != p.Head || runtime.State != data.EngineInitialized || !sameNativeMaterialRuntime(runtime, input.Runtime) {
		return ErrConflict
	}
	permit, err := readEnginePermit(ctx, tx, p.Head.BindingID)
	if err != nil || !permit.Identity.Allows(data.EngineSpacetimeDB) || !managedAppOwns(ctx, tx, p.App, permit.Reserved.Database.IncarnationID) {
		return ErrConflict
	}
	wanted, err := readManagedDesired(ctx, tx, p.Head.BindingID)
	if err != nil || !wanted.Current(head.BindingID, head.EpochID, head.RuntimeSHA256) || wanted.Revision != p.DesiredRevision {
		return ErrConflict
	}
	if wanted.EngineRunning || wanted.FrontendRunning || wanted.IngressServing {
		return ErrMaterialRequiresStop
	}
	if p.Secrets.Publisher.Version <= runtime.Secrets.Publisher.Version || p.Secrets.SigningPrivate.Version <= runtime.Secrets.SigningPrivate.Version || p.Secrets.SigningPublic.Version <= runtime.Secrets.SigningPublic.Version || p.Secrets.AuthPolicy.Version <= runtime.Secrets.AuthPolicy.Version {
		return ErrConflict
	}
	runtime.Secrets = p.Secrets
	if !runtime.PublishingSecretsAdmit(time.Now().UTC(), time.Duration(runtime.StartupSeconds+runtime.StopSeconds+35)*time.Second) {
		return ErrMaterialLeaseExpired
	}
	return refusePendingNativeCaptureMutation(ctx, tx, head.BindingID, "")
}

// ActivateNativeMaterial only selects immutable files. The caller holds the host,
// launch and lifetime locks and proves all managed services stopped beforehand.
func (s *Store) ActivateNativeMaterial(ctx context.Context, p nativematerial.ActivationPlan, guard *data.EngineLock, lifetime *data.EngineLock) (nativematerial.ActivationReceipt, error) {
	if !guard.HoldsLaunch(s.dir, p.Head.BindingID) || !lifetime.HoldsLifetime(s.dir, p.Head.BindingID) {
		return nativematerial.ActivationReceipt{}, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nativematerial.ActivationReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	actual, err := readMaterialActivationPlan(ctx, tx, p.ID)
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(p)
	if err != nil || !bytes.Equal(a, b) {
		return nativematerial.ActivationReceipt{}, ErrConflict
	}
	if receipt, err := readMaterialActivationReceipt(ctx, tx, p.ID); err == nil {
		return receipt, tx.Commit()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return receipt, err
	}
	if err = checkMaterialActivation(ctx, tx, p); err != nil {
		return nativematerial.ActivationReceipt{}, err
	}
	_, runtime, err := readEngineHead(ctx, tx, p.Head.BindingID)
	if err != nil {
		return nativematerial.ActivationReceipt{}, err
	}
	runtime.Secrets = p.Secrets
	raw, _ := json.Marshal(runtime)
	receipt := nativematerial.ActivationReceipt{PlanID: p.ID, BindingID: p.Head.BindingID, EpochID: p.Head.EpochID, RuntimeVersion: p.Head.Version + 1, RuntimeSHA256: nativeCanonicalHash(raw)}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_engine_runtime_commits VALUES(?,?,?)", receipt.BindingID, receipt.RuntimeVersion, raw); err != nil {
		return receipt, err
	}
	wanted, err := readManagedDesired(ctx, tx, p.Head.BindingID)
	if err != nil {
		return receipt, err
	}
	wanted.Revision++
	wanted.RuntimeSHA256 = receipt.RuntimeSHA256
	raw, _ = json.Marshal(wanted)
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_engine_desired_states VALUES(?,?,?,?,?)", wanted.BindingID, wanted.Revision, wanted.PlanID, wanted.OperationID, raw); err != nil {
		return receipt, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE data_engine_desired_heads SET revision=? WHERE binding_id=?", wanted.Revision, wanted.BindingID); err != nil {
		return receipt, err
	}
	raw, _ = json.Marshal(receipt)
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_material_activations VALUES(?,?,?,?)", p.ID, receipt.BindingID, receipt.RuntimeVersion, raw); err != nil {
		return receipt, err
	}
	return receipt, tx.Commit()
}
func readMaterialActivationReceipt(ctx context.Context, q dataQuerier, id string) (nativematerial.ActivationReceipt, error) {
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT canonical FROM data_native_material_activations WHERE plan_id=?", id).Scan(&raw); err != nil {
		return nativematerial.ActivationReceipt{}, err
	}
	r, err := nativematerial.DecodeActivationReceipt(raw)
	if err != nil || r.PlanID != id {
		return r, &IntegrityError{}
	}
	return r, nil
}

// Material revisions retain the allocation and transition proof of their epoch.
func materialRuntimeHead(ctx context.Context, q dataQuerier, head data.EngineHeadRef, ancestorSHA256 string) (data.EngineHeadRef, error) {
	for head.RuntimeSHA256 != ancestorSHA256 {
		var id string
		err := q.QueryRowContext(ctx, "SELECT plan_id FROM data_native_material_activations WHERE binding_id=? AND version=?", head.BindingID, head.Version).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return head, nil
		}
		if err != nil {
			return head, err
		}
		p, err := readMaterialActivationPlan(ctx, q, id)
		if err != nil {
			return head, err
		}
		receipt, err := readMaterialActivationReceipt(ctx, q, id)
		if err != nil || receipt.BindingID != head.BindingID || receipt.EpochID != head.EpochID || receipt.RuntimeVersion != head.Version || receipt.RuntimeSHA256 != head.RuntimeSHA256 || p.Head.Version+1 != head.Version || p.Head.BindingID != head.BindingID || p.Head.EpochID != head.EpochID {
			return head, &IntegrityError{}
		}
		head = p.Head
	}
	return head, nil
}
func (s *Store) ReadNativeMaterialActivationReceipt(ctx context.Context, id string) (nativematerial.ActivationReceipt, error) {
	return readMaterialActivationReceipt(ctx, s.db, id)
}
