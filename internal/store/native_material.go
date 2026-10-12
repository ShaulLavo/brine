package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/policy"
)

type NativeMaterialInput struct {
	Receipt                nativematerial.Receipt `json:"receipt"`
	Requester              string                 `json:"requester"`
	Runtime                data.EngineRuntime     `json:"runtime"`
	Initial                bool                   `json:"initial"`
	PriorRuntimeSHA256     string                 `json:"prior_runtime_sha256"`
	FenceID                data.FenceID           `json:"fence_id"`
	PreparationOperationID string                 `json:"preparation_operation_id"`
	DesiredRevision        uint64                 `json:"desired_revision"`
	PolicyHash             string                 `json:"policy_hash"`
}

func (s *Store) ReserveNativeMaterial(ctx context.Context, app, planID, requester, key string, candidate data.EngineRuntime) (NativeMaterialInput, bool, error) {
	identity, identityErr := datainit.ParseRequester(requester)
	if s.readOnly || !data.ValidID(key) || identityErr != nil || !identity.Valid() || candidate.Validate() != nil {
		return NativeMaterialInput{}, false, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return NativeMaterialInput{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	old, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE requester=? AND idempotency_key=?", requester, key))
	if err == nil {
		input, e := readNativeMaterial(ctx, tx, old.ID)
		if e != nil || old.Kind != ops.NativeMaterialSet || old.App != app || old.SecretRef != planID || !sameNativeMaterialCandidate(input.Runtime, candidate) {
			return NativeMaterialInput{}, false, errors.Join(ErrConflict, e)
		}
		return input, true, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return NativeMaterialInput{}, false, err
	}
	p, d, err := loadPlan(ctx, tx, planID)
	if err != nil || p.App != app || p.Managed == nil || policy.ValidateManagedDesired(d) != nil || p.Managed.Database.BindingID != candidate.BindingID || p.Managed.Archive.EpochID != candidate.EpochID {
		return NativeMaterialInput{}, false, errors.Join(ErrConflict, err)
	}
	prepared, err := readManagedPreparation(ctx, tx, candidate.BindingID)
	if err != nil || prepared.PlanID != planID {
		return NativeMaterialInput{}, false, errors.Join(ErrConflict, err)
	}
	wanted, err := readManagedDesired(ctx, tx, candidate.BindingID)
	if err != nil {
		return NativeMaterialInput{}, false, err
	}
	head, active, headErr := readEngineHead(ctx, tx, candidate.BindingID)
	initial := errors.Is(headErr, ErrNotFound)
	if headErr != nil && !initial {
		return NativeMaterialInput{}, false, headErr
	}
	if initial && (candidate.State != data.EngineUnpublished || candidate.Database != "" || candidate.Publisher != "" || len(candidate.Names) != 0) {
		return NativeMaterialInput{}, false, ErrConflict
	}
	input := NativeMaterialInput{Requester: requester, Runtime: candidate, Initial: initial, PriorRuntimeSHA256: head.RuntimeSHA256, PreparationOperationID: prepared.OperationID, DesiredRevision: wanted.Revision, PolicyHash: d.PolicyHash}
	outcome := "stored_unreferenced"
	if initial {
		outcome = "configured_unpublished"
	}
	id, err := newID()
	if err != nil {
		return NativeMaterialInput{}, false, err
	}
	input.Receipt = nativematerial.Receipt{OperationID: id, App: app, PreparationPlanID: planID, BindingID: candidate.BindingID, EpochID: candidate.EpochID, Outcome: outcome}
	if initial {
		err = tx.QueryRowContext(ctx, "SELECT id FROM data_fences WHERE database_id=? AND operation_id=? AND state='held'", p.Managed.Database.DatabaseID, prepared.OperationID).Scan(&input.FenceID)
		if err != nil {
			return NativeMaterialInput{}, false, ErrConflict
		}
	}
	var pending int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_native_material_inputs i JOIN operations o ON o.id=i.operation_id WHERE i.binding_id=? AND json_extract(i.canonical,'$.initial')=1 AND o.state<>'succeeded'", candidate.BindingID).Scan(&pending)
	if err != nil || pending != 0 {
		return NativeMaterialInput{}, false, errors.Join(ErrConflict, err)
	}
	var version uint64
	err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0)+1 FROM data_native_material_inputs WHERE binding_id=?", candidate.BindingID).Scan(&version)
	if err != nil || version == 0 || version > 1<<63-1 {
		return NativeMaterialInput{}, false, ErrConflict
	}
	for _, v := range []data.EngineSecretVersion{active.Secrets.Publisher, active.Secrets.SigningPrivate, active.Secrets.SigningPublic, active.Secrets.AuthPolicy} {
		if version <= v.Version {
			version = v.Version + 1
		}
	}
	if version == 0 || version > 1<<63-1 {
		return NativeMaterialInput{}, false, ErrConflict
	}
	input.Runtime.Secrets = materialVersions(candidate.Secrets, version)
	input.Receipt.Secrets = input.Runtime.Secrets
	if err = checkNativeMaterialScope(ctx, tx, input); err != nil {
		return NativeMaterialInput{}, false, err
	}
	now := timestamp()
	_, err = tx.ExecContext(ctx, "INSERT INTO operations VALUES(?,NULL,?,?,?,?,?,?,?,?,?)", id, requester, key, ops.Queued, now, now, ops.NativeMaterialSet, app, planID, "")
	if err != nil {
		return NativeMaterialInput{}, false, err
	}
	raw, _ := json.Marshal(input)
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_material_inputs VALUES(?,?,?,?)", id, candidate.BindingID, version, raw)
	if err != nil {
		return NativeMaterialInput{}, false, err
	}
	return input, false, tx.Commit()
}
func materialVersions(s data.EngineSecrets, version uint64) data.EngineSecrets {
	s.Publisher.Version = version
	s.SigningPrivate.Version = version
	s.SigningPublic.Version = version
	s.AuthPolicy.Version = version
	return s
}
func sameNativeMaterialCandidate(a, b data.EngineRuntime) bool {
	a.Secrets = materialVersions(a.Secrets, 1)
	b.Secrets = materialVersions(b.Secrets, 1)
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(aa, bb)
}
func readNativeMaterial(ctx context.Context, q dataQuerier, id string) (NativeMaterialInput, error) {
	var raw []byte
	var binding data.ReplicaBindingID
	var version uint64
	err := q.QueryRowContext(ctx, "SELECT binding_id,version,canonical FROM data_native_material_inputs WHERE operation_id=?", id).Scan(&binding, &version, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return NativeMaterialInput{}, ErrNotFound
	}
	var input NativeMaterialInput
	if err != nil || len(raw) > 16384 || json.Unmarshal(raw, &input) != nil {
		return input, errors.Join(&IntegrityError{}, err)
	}
	canonical, _ := json.Marshal(input)
	if !bytes.Equal(raw, canonical) || !input.Receipt.Valid() || input.Receipt.OperationID != id || input.Receipt.BindingID != binding || input.Runtime.Validate() != nil || input.Runtime.BindingID != binding || input.Runtime.EpochID != input.Receipt.EpochID || input.Receipt.Secrets != input.Runtime.Secrets || materialVersions(input.Runtime.Secrets, version) != input.Runtime.Secrets {
		return input, &IntegrityError{}
	}
	return input, nil
}
func checkNativeMaterialScope(ctx context.Context, q dataQuerier, input NativeMaterialInput) error {
	r := input.Receipt
	p, d, err := loadPlan(ctx, q, r.PreparationPlanID)
	if err != nil || p.App != r.App || p.Managed == nil || policy.ValidateManagedDesired(d) != nil || d.PolicyHash != input.PolicyHash || p.Managed.Database.BindingID != r.BindingID || p.Managed.Archive.EpochID != r.EpochID {
		return errors.Join(ErrConflict, err)
	}
	reserved, err := readReservedEngine(ctx, q, r.BindingID)
	if err != nil || reserved.Database != p.Managed.Database || reserved.Archive != p.Managed.Archive || !managedAppOwns(ctx, q, r.App, reserved.Database.IncarnationID) {
		return errors.Join(ErrConflict, err)
	}
	wanted, err := readManagedDesired(ctx, q, r.BindingID)
	if err != nil || wanted.Revision != input.DesiredRevision {
		return errors.Join(ErrConflict, err)
	}
	prepared, err := readManagedPreparation(ctx, q, r.BindingID)
	if err != nil || prepared.PlanID != r.PreparationPlanID || prepared.OperationID != input.PreparationOperationID {
		return errors.Join(ErrConflict, err)
	}
	var requester, admission string
	err = q.QueryRowContext(ctx, "SELECT requester FROM operations WHERE id=?", prepared.OperationID).Scan(&requester)
	if err != nil || requester != input.Requester {
		return errors.Join(ErrConflict, err)
	}
	err = q.QueryRowContext(ctx, "SELECT policy_hash FROM data_admissions WHERE database_id=? ORDER BY version DESC LIMIT 1", reserved.Database.DatabaseID).Scan(&admission)
	if err != nil || admission != input.PolicyHash {
		return errors.Join(ErrConflict, err)
	}
	refs := []string{string(d.SpacetimeDB.Publisher), string(d.SpacetimeDB.SigningPrivate), string(d.SpacetimeDB.SigningPublic), string(d.SpacetimeDB.Auth)}
	secrets := []data.EngineSecretVersion{r.Secrets.Publisher, r.Secrets.SigningPrivate, r.Secrets.SigningPublic, r.Secrets.AuthPolicy}
	for i, v := range secrets {
		if v.Reference != refs[i] {
			return ErrConflict
		}
	}
	if !input.Initial {
		head, active, err := readEngineHead(ctx, q, r.BindingID)
		if err != nil || head.EpochID != r.EpochID || head.RuntimeSHA256 != input.PriorRuntimeSHA256 || !sameNativeMaterialRuntime(active, input.Runtime) {
			return errors.Join(ErrConflict, err)
		}
		return nil
	}
	metadata := d.SpacetimeDB.Module.Metadata
	resources := d.SpacetimeDB.Resources
	runtime := input.Runtime
	if metadata == nil || runtime.State != data.EngineUnpublished || runtime.Database != "" || runtime.Publisher != "" || len(runtime.Names) != 0 || runtime.Version != d.SpacetimeDB.Version || runtime.Runtime != d.SpacetimeDB.Runtime || runtime.HostPort != prepared.EnginePort || runtime.MemoryMB != resources.MemoryMB || runtime.PIDsLimit != resources.PIDsLimit || runtime.PagePoolMB != resources.PagePoolMB || runtime.StopSeconds != resources.StopTimeoutSeconds {
		return ErrConflict
	}
	expected := data.EngineModuleDefinition{Program: data.NativeProgramWireHash(metadata.RuntimeModuleHash), Wasm: data.WasmSHA256(metadata.WasmSHA256), Schema: data.EngineSchemaDefinition{Marker: d.SpacetimeDB.Module.Marker, Canonicalizer: metadata.Canonicalizer, SHA256: metadata.SchemaSHA256}}
	if runtime.Module != expected {
		return ErrConflict
	}
	if wanted.Revision != 1 || wanted.PlanID != prepared.PlanID || wanted.OperationID != prepared.OperationID || wanted.RuntimeSHA256 != "" || wanted.EngineRunning || wanted.FrontendRunning || wanted.IngressServing {
		return ErrConflict
	}
	var held int
	err = q.QueryRowContext(ctx, "SELECT count(*) FROM data_fences WHERE id=? AND database_id=? AND operation_id=? AND state='held'", input.FenceID, reserved.Database.DatabaseID, prepared.OperationID).Scan(&held)
	if err != nil || held != 1 {
		return errors.Join(ErrConflict, err)
	}
	head, currentRuntime, err := readEngineHead(ctx, q, r.BindingID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	raw, _ := json.Marshal(input.Runtime)
	if err != nil || head.RuntimeSHA256 != strings.TrimPrefix(digest(raw), "sha256:") || currentRuntime.State != data.EngineUnpublished {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
func (s *Store) NativeMaterialPreparation(ctx context.Context, input NativeMaterialInput) (*EnginePreparation, error) {
	if s.readOnly {
		return nil, ErrInvalid
	}
	guard, err := data.AcquireEngineLaunchGuard(ctx, s.dir, input.Receipt.BindingID)
	if err != nil {
		return nil, err
	}
	if err = checkNativeMaterialScope(ctx, s.db, input); err != nil {
		_ = guard.Release()
		return nil, err
	}
	return &EnginePreparation{root: s.dir, binding: input.Receipt.BindingID, guard: guard}, nil
}
func (s *Store) ReadNativeMaterial(ctx context.Context, id, requester string) (NativeMaterialInput, error) {
	input, err := readNativeMaterial(ctx, s.db, id)
	if err != nil || input.Requester != requester {
		return NativeMaterialInput{}, errors.Join(ErrConflict, err)
	}
	return input, nil
}
func (s *Store) CompleteNativeMaterial(ctx context.Context, input NativeMaterialInput) (nativematerial.Receipt, error) {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nativematerial.Receipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	actual, err := readNativeMaterial(ctx, tx, input.Receipt.OperationID)
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(input)
	if err != nil || !bytes.Equal(a, b) || checkNativeMaterialScope(ctx, tx, input) != nil {
		return nativematerial.Receipt{}, ErrConflict
	}
	if input.Initial {
		_, runtime, err := readEngineHead(ctx, tx, input.Receipt.BindingID)
		if err != nil || !sameNativeMaterialCandidate(runtime, input.Runtime) {
			return nativematerial.Receipt{}, ErrConflict
		}
	}
	raw, _ := json.Marshal(input.Receipt)
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_material_receipts VALUES(?,?)", input.Receipt.OperationID, raw)
	if err != nil {
		return nativematerial.Receipt{}, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE operations SET state='succeeded',updated_at=? WHERE id=? AND state='preparing'", timestamp(), input.Receipt.OperationID)
	if err != nil {
		return nativematerial.Receipt{}, err
	}
	_, err = appendEvent(ctx, tx, input.Receipt.OperationID, Event{Kind: "native_material", Payload: raw})
	if err != nil {
		return nativematerial.Receipt{}, err
	}
	_, err = appendEvent(ctx, tx, input.Receipt.OperationID, Event{Kind: "state", State: ops.Succeeded})
	if err != nil {
		return nativematerial.Receipt{}, err
	}
	return input.Receipt, tx.Commit()
}
func (s *Store) NativeMaterialReceipt(ctx context.Context, id, requester string) (nativematerial.Receipt, error) {
	input, err := s.ReadNativeMaterial(ctx, id, requester)
	if err != nil {
		return nativematerial.Receipt{}, err
	}
	op, err := s.GetOperation(ctx, id)
	if err != nil {
		return nativematerial.Receipt{}, err
	}
	if op.State != ops.Succeeded {
		r := input.Receipt
		r.Outcome = "recovery_required"
		return r, nil
	}
	var raw []byte
	err = s.db.QueryRowContext(ctx, "SELECT canonical FROM data_native_material_receipts WHERE operation_id=?", id).Scan(&raw)
	r, decodeErr := nativematerial.DecodeReceipt(raw)
	if err != nil || decodeErr != nil || r != input.Receipt {
		return r, &IntegrityError{}
	}
	return r, nil
}

func (s *Store) NativeMaterialByKey(ctx context.Context, requester, key string) (NativeMaterialInput, error) {
	op, err := scanOperation(s.db.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE requester=? AND idempotency_key=?", requester, key))
	if err != nil {
		return NativeMaterialInput{}, err
	}
	if op.Kind != ops.NativeMaterialSet {
		return NativeMaterialInput{}, ErrConflict
	}
	return s.ReadNativeMaterial(ctx, op.ID, requester)
}

func (s *Store) ReadManagedPreparationFence(ctx context.Context, p ManagedPreparation) (data.FenceID, error) {
	actual, err := s.ReadManagedPreparation(ctx, p.BindingID)
	if err != nil || actual != p {
		return "", ErrConflict
	}
	reserved, err := s.ReadReservedEngine(ctx, p.BindingID)
	if err != nil {
		return "", err
	}
	var fence data.FenceID
	err = s.db.QueryRowContext(ctx, "SELECT id FROM data_fences WHERE database_id=? AND operation_id=? AND state='held'", reserved.Database.DatabaseID, p.OperationID).Scan(&fence)
	if err != nil || !data.ValidID(string(fence)) {
		return "", errors.Join(ErrConflict, err)
	}
	return fence, nil
}

func sameNativeMaterialRuntime(a, b data.EngineRuntime) bool {
	b.Secrets = a.Secrets
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(aa, bb)
}
