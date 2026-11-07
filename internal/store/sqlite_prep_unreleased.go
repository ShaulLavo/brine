package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
	"github.com/ShaulLavo/brine/internal/target"
)

// SQLiteUnreleasedMetadata is retained evidence, never a mutation capability.
type SQLiteUnreleasedMetadata struct {
	Permit                  ReplicaPermit
	Allocation              data.AllocationReceipt
	Desired                 policy.Desired
	DeclarationTarget       target.Identity
	DeclarationTargetSHA256 string
	DeliveryTargetSHA256    string
	Definitions             []data.SQLiteSchemaDefinition
	Credential              CredentialRecord
	Initialization          *datainit.Plan
	NeverWrittenSHA256      string
	ProducerOperations      []string
	Scope                   sqliteprep.Scope
}

func (s *Store) ReadSQLiteUnreleasedMetadata(ctx context.Context, in sqliteprep.Input) (SQLiteUnreleasedMetadata, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SQLiteUnreleasedMetadata{}, err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := readSQLiteCurrent(ctx, tx, in, sqliteNow())
	if err != nil || c.unreleased == nil {
		return SQLiteUnreleasedMetadata{}, errors.Join(ErrConflict, err)
	}
	out := *c.unreleased
	out.Scope = c.scope
	return out, tx.Commit()
}

// effectiveSQLiteInput follows producing inputs, never a consumer or fence owner.
func effectiveSQLiteInput(ctx context.Context, q dataQuerier, in sqliteprep.Input) (sqliteprep.Input, error) {
	original := in
	seen := map[string]bool{}
	for depth := 0; depth < 64; depth++ {
		if in.DeclarationPlanID != "" {
			return in, nil
		}
		previous := in.Supersedes
		if in.Kind == sqliteprep.AbandonTask {
			previous = in.PreparationID
		}
		if previous == "" {
			return in, nil
		}
		if seen[previous] {
			return original, &IntegrityError{}
		}
		seen[previous] = true
		var raw []byte
		if err := q.QueryRowContext(ctx, "SELECT canonical FROM data_sqlite_preparations WHERE id=?", previous).Scan(&raw); err != nil {
			return original, err
		}
		intent, err := sqliteprep.DecodeIntent(raw)
		if err != nil || intent.Input.PreparationID != previous || intent.Input.App != original.App || intent.Input.DatabaseID != original.DatabaseID {
			return original, &IntegrityError{}
		}
		_, stored, err := readSQLiteOperation(ctx, q, intent.Task)
		if err != nil || stored != intent.Input {
			return original, errors.Join(ErrConflict, err)
		}
		in = stored
	}
	return original, &IntegrityError{}
}

func readSQLiteUnreleased(ctx context.Context, tx *sql.Tx, in sqliteprep.Input, permit ReplicaPermit, now time.Time) (sqliteCurrent, error) {
	effective, err := effectiveSQLiteInput(ctx, tx, in)
	if err != nil || effective.DeclarationPlanID == "" || !sqliteprep.ValidSelectors(effective.DeclarationPlanID, effective.DeliveredCredentialVersion) {
		return sqliteCurrent{}, errors.Join(sqliteRefuse(sqliteprep.InvalidEvidence), err)
	}
	b, r := permit.Database, permit.Replica
	declaration := data.SQLiteDatabase{Name: b.Name, PersistentRoot: b.Root, MountPath: b.MountPath, Filename: b.Filename, BackupDestination: r.Destination.Reference}
	if !(data.AllocationProposal{Database: b, Replica: r}).Valid(declaration, r.Destination) {
		return sqliteCurrent{}, &IntegrityError{}
	}
	if err = checkDestinationOwner(ctx, tx, r.Destination.Endpoint, r.Destination.Bucket, r.RemotePrefix, r.BindingID); err != nil {
		return sqliteCurrent{}, err
	}
	var released int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM release_heads WHERE app=?", in.App).Scan(&released); err != nil || released != 0 {
		return sqliteCurrent{}, errors.Join(ErrConflict, err)
	}
	allocation, err := readPreparationEvidence(ctx, tx, b.DatabaseID)
	if err != nil || allocation == nil || allocation.IncarnationID != b.IncarnationID || allocation.RootProof == nil || allocation.MappingProof == nil {
		return sqliteCurrent{}, errors.Join(ErrConflict, err)
	}
	c := sqliteCurrent{permit: permit, allocated: allocation, expires: now.Add(sqliteprep.PreparationBudget + time.Minute)}
	c.scope = sqliteprep.Scope{App: in.App, DatabaseName: b.Name, IncarnationID: b.IncarnationID, DatabaseID: b.DatabaseID, BindingID: r.BindingID, EpochID: r.EpochID, CredentialVersion: effective.DeliveredCredentialVersion, RemotePrefix: strings.TrimSuffix(r.RemotePrefix, "/") + "/"}
	destination, _ := json.Marshal(r.Destination)
	c.scope.TargetSHA256 = sqliteHash(destination)
	for _, fence := range permit.Fences {
		if fence.State == data.FenceHeld {
			f := fence
			c.held = &f
		}
	}
	if err = c.readPolicy(ctx, tx); err != nil {
		return c, err
	}
	p, d, err := readUnreleasedDeclaration(ctx, tx, effective.DeclarationPlanID, b.IncarnationID)
	if err != nil || p.App != in.App || d.Runtime == nil || allocation.MappingProof.Runtime != *d.Runtime {
		return c, errors.Join(ErrConflict, err)
	}
	declared, err := c.declaration(ctx, tx, d)
	if err != nil {
		return c, err
	}
	encoded, _ := json.Marshal(declared)
	c.scope.DeclarationSHA256, c.scope.Checks = sqliteHash(encoded), declared.Checks
	c.authorization = c.retention(d, now)
	if strings.TrimPrefix(d.PolicyHash, "sha256:") != c.scope.PolicySHA256 {
		c.authorization = errors.Join(c.authorization, ErrConflict)
	}
	record, err := readSQLiteCredential(ctx, tx, in.App, r, effective.DeliveredCredentialVersion)
	if err != nil {
		return c, err
	}
	deliveryTarget, err := readUnreleasedDelivery(ctx, tx, record)
	if err != nil {
		return c, err
	}
	if record.ExpiresAt != nil {
		c.expires = record.ExpiresAt.UTC()
	}
	hostRaw, _ := json.Marshal(p.Target)
	host := "sha256:" + sqliteHash(hostRaw)
	if host != deliveryTarget {
		return c, ErrConflict
	}
	c.unreleased = &SQLiteUnreleasedMetadata{Permit: permit, Allocation: *allocation, Desired: d, DeclarationTarget: p.Target, DeclarationTargetSHA256: host, DeliveryTargetSHA256: deliveryTarget, Definitions: declared.Definitions, Credential: record, ProducerOperations: []string{}}
	if err = c.unreleasedHistory(ctx, tx, p, declared, in); err != nil {
		return c, err
	}
	return c, nil
}

func readUnreleasedDeclaration(ctx context.Context, q dataQuerier, id string, incarnation data.AppIncarnationID) (plan.Plan, policy.Desired, error) {
	p, d, err := loadPlan(ctx, q, id)
	if err != nil {
		return p, d, err
	}
	input := p.DecisionInput()
	if len(input) == 0 || len(p.Conflicts) != 0 || p.Kind != plan.Create && p.Kind != plan.Update || p.Lifecycle != "" && p.Lifecycle != plan.PrepareData || p.Managed != nil || p.ManagedLifecycle != nil || d.Runtime == nil || d.Runtime.Validate() != nil || p.Runtime == nil || *p.Runtime != *d.Runtime || p.Target.ID == "" || p.Target.HostKeyFingerprint == "" {
		return p, d, ErrConflict
	}
	if err = validateUnreleasedDecision(p, d); err != nil {
		return p, d, err
	}
	if err = validateUnreleasedDesired(d); err != nil {
		return p, d, err
	}
	var count int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM data_databases WHERE incarnation_id=?", incarnation).Scan(&count); err != nil || count != len(d.Databases) {
		return p, d, errors.Join(ErrConflict, err)
	}
	for _, declaration := range d.Databases {
		if err = validateUnreleasedMapping(ctx, q, p, d, declaration, incarnation); err != nil {
			return p, d, err
		}
	}
	return p, d, nil
}

func validateUnreleasedDesired(d policy.Desired) error {
	if len(d.Databases) == 0 || len(d.Databases) > 16 || len(d.SchemaCompatibility) != len(d.Databases) || len(d.SchemaDefinitions) > 128 || len(d.RestoreInvariants) > 64 {
		return ErrConflict
	}
	destinations := map[data.BackupDestinationRef]bool{}
	if len(d.BackupDestinations) == 0 || len(d.BackupDestinations) > 16 || len(d.BackupRetention) > 16 {
		return ErrConflict
	}
	for _, destination := range d.BackupDestinations {
		if destination.Validate() != nil || destinations[destination.Reference] {
			return ErrConflict
		}
		destinations[destination.Reference] = true
	}
	retention := map[data.BackupDestinationRef]bool{}
	for _, evidence := range d.BackupRetention {
		if !evidence.Valid() || !destinations[evidence.Destination] || retention[evidence.Destination] {
			return ErrConflict
		}
		retention[evidence.Destination] = true
	}
	names := map[data.DatabaseName]bool{}
	mounts := map[data.ContainerMountPath]bool{}
	for _, declaration := range d.Databases {
		if declaration.Validate() != nil || !destinations[declaration.BackupDestination] || names[declaration.Name] || mounts[declaration.MountPath] || !slices.Contains(d.PersistentRoots, declaration.PersistentRoot) {
			return ErrConflict
		}
		names[declaration.Name], mounts[declaration.MountPath] = true, true
	}
	compatible := map[data.DatabaseName]bool{}
	for _, compat := range d.SchemaCompatibility {
		if !names[compat.Database] || compatible[compat.Database] || compat.Startup != "preserve" || !uniqueSQLiteMarkers(compat.Accepts) {
			return ErrConflict
		}
		compatible[compat.Database] = true
	}
	definitions := map[string]bool{}
	for _, definition := range d.SchemaDefinitions {
		key := string(definition.Database) + "\x00" + definition.Marker
		if !names[definition.Database] || !data.ValidMarker(definition.Marker) || !data.ValidCatalogHash(definition.CatalogSHA256) || definitions[key] {
			return ErrConflict
		}
		definitions[key] = true
	}
	for _, check := range d.RestoreInvariants {
		if !names[check.Database] || check.Validate() != nil {
			return ErrConflict
		}
	}
	return nil
}

func uniqueSQLiteMarkers(markers []string) bool {
	if len(markers) == 0 || len(markers) > 128 {
		return false
	}
	seen := map[string]bool{}
	for _, marker := range markers {
		if !data.ValidMarker(marker) || seen[marker] {
			return false
		}
		seen[marker] = true
	}
	return true
}

func validateUnreleasedMapping(ctx context.Context, q dataQuerier, p plan.Plan, d policy.Desired, declaration data.SQLiteDatabase, incarnation data.AppIncarnationID) error {
	var id data.DatabaseID
	if err := q.QueryRowContext(ctx, "SELECT d.id FROM data_databases d JOIN data_engine_kinds k ON k.database_id=d.id WHERE d.incarnation_id=? AND d.name=? AND k.engine='sqlite'", incarnation, declaration.Name).Scan(&id); err != nil {
		return err
	}
	permit, err := readReplicaPermit(ctx, q, id)
	if err != nil {
		return err
	}
	for _, definition := range d.SchemaDefinitions {
		if definition.Database != declaration.Name {
			continue
		}
		var catalog string
		if err = q.QueryRowContext(ctx, "SELECT catalog_sha256 FROM data_schema_definitions WHERE database_id=? AND marker=?", id, definition.Marker).Scan(&catalog); err != nil || catalog != definition.CatalogSHA256 {
			return errors.Join(ErrConflict, err)
		}
	}
	proposal := data.AllocationProposal{Database: permit.Database, Replica: permit.Replica}
	destinations := 0
	for _, destination := range d.BackupDestinations {
		if destination.Reference == declaration.BackupDestination && destination == permit.Replica.Destination && proposal.Valid(declaration, destination) {
			destinations++
		}
	}
	if destinations != 1 {
		return ErrConflict
	}
	if p.Lifecycle == plan.PrepareData {
		if len(p.DataAllocations) != len(d.Databases) || len(p.DataMounts) != 0 || !slices.Contains(p.DataAllocations, proposal) {
			return ErrConflict
		}
		return nil
	}
	expected := data.Mount{Database: permit.Database, HostPath: filepath.Join(string(permit.Database.Root), permit.Database.RelativeDirectory), ContainerPath: permit.Database.MountPath, BindingID: permit.Replica.BindingID}
	if len(p.DataAllocations) != 0 || len(p.DataMounts) != len(d.Databases) || !slices.Contains(p.DataMounts, expected) {
		return ErrConflict
	}
	return nil
}

func readUnreleasedDelivery(ctx context.Context, q dataQuerier, r CredentialRecord) (string, error) {
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT canonical FROM data_credential_records WHERE kind='plan' AND json_extract(canonical,'$.plan_id')=?", r.PlanID).Scan(&raw); err != nil {
		return "", err
	}
	var stored CredentialRecord
	if len(raw) > 8192 || json.Unmarshal(raw, &stored) != nil {
		return "", &IntegrityError{}
	}
	canonical, _ := json.Marshal(stored)
	expected := r
	expected.ID, expected.Kind, expected.ReceivedAt = stored.ID, "plan", time.Time{}
	if !bytes.Equal(raw, canonical) || !sqliteSameJSON(stored, expected) || !data.ValidID(stored.ID) {
		return "", &IntegrityError{}
	}
	p := backupcredentials.Plan{Requester: r.Requester, Kind: backupcredentials.Kind, ID: r.PlanID, Version: r.Version, ExpiresAt: r.ExpiresAt, Scope: backupcredentials.Scope{App: r.App, CredentialRef: r.CredentialRef, TargetHash: r.TargetHash, PolicyHash: r.PolicyHash, Binding: string(r.BindingID), Epoch: string(r.EpochID), Destination: string(r.Destination)}}
	if !p.Valid() || r.PlanHash != p.ID || r.ReceivedAt.IsZero() {
		return "", ErrConflict
	}
	var count int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM data_credential_records WHERE kind='plan' AND json_extract(canonical,'$.plan_id')=?", r.PlanID).Scan(&count); err != nil || count != 1 {
		return "", errors.Join(ErrConflict, err)
	}
	return r.TargetHash, nil
}

type sqliteInitHistory struct {
	startedAt   time.Time
	settledAt   time.Time
	Operation   string `json:"operation"`
	Plan        string `json:"plan"`
	Fence       string `json:"fence"`
	Terminal    string `json:"terminal"`
	Sequence    int    `json:"sequence"`
	EventSHA256 string `json:"event_sha256"`
}

func (c *sqliteCurrent) unreleasedHistory(ctx context.Context, tx *sql.Tx, p plan.Plan, declared sqliteDeclaration, in sqliteprep.Input) error {
	entries, err := sqliteInitEntries(ctx, tx, c.scope.DatabaseID)
	if err != nil {
		return err
	}
	successes := 0
	var successStarted time.Time
	for _, entry := range entries {
		initialized, outer, err := c.validateInitEntry(ctx, tx, entry, declared)
		if err != nil {
			return err
		}
		c.unreleased.ProducerOperations = append(c.unreleased.ProducerOperations, outer)
		if entry.Terminal == "succeeded" {
			successes++
			successStarted = entry.startedAt
			c.unreleased.Initialization = &initialized
		}
	}
	for _, entry := range entries {
		if successStarted.IsZero() || entry.Terminal != "not_initialized" || !entry.settledAt.After(successStarted) {
			continue
		}
		return ErrConflict
	}
	activations, err := sqliteUnreleasedActivationProducers(ctx, tx, c.scope.App, c.scope.BindingID)
	if err != nil {
		return err
	}
	c.unreleased.ProducerOperations = append(c.unreleased.ProducerOperations, activations...)
	var history, starts int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_writer_history WHERE database_id=?", c.scope.DatabaseID).Scan(&history); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_writer_starts WHERE incarnation_id=?", c.scope.IncarnationID).Scan(&starts); err != nil || starts != 0 || successes > 1 || history != successes {
		return errors.Join(ErrConflict, err)
	}
	if err = c.unreleasedPending(ctx, tx, in); err != nil {
		return err
	}
	if p.Lifecycle == plan.PrepareData {
		producer, err := sqliteAllocationProducer(ctx, tx, p.Hash)
		if err != nil {
			return err
		}
		c.unreleased.ProducerOperations = append(c.unreleased.ProducerOperations, producer)
	} else if c.unreleased.Initialization == nil {
		return ErrConflict
	}
	if successes == 1 {
		return nil
	}
	var incarnationHistory int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_writer_history h JOIN data_databases d ON d.id=h.database_id WHERE d.incarnation_id=?", c.scope.IncarnationID).Scan(&incarnationHistory); err != nil || incarnationHistory != 0 {
		return errors.Join(ErrConflict, err)
	}
	allocation, err := readAllocation(ctx, tx, c.scope.DatabaseID)
	if err != nil || allocation == nil {
		return errors.Join(ErrConflict, err)
	}
	allocationRaw, _ := json.Marshal(allocation)
	projection := struct {
		Kind            string                `json:"kind"`
		Database        data.DatabaseID       `json:"database"`
		Incarnation     data.AppIncarnationID `json:"incarnation"`
		Allocation      string                `json:"allocation"`
		Writers         []string              `json:"writers"`
		Starts          []string              `json:"starts"`
		Initializations []sqliteInitHistory   `json:"initializations"`
	}{"brine-sqlite-never-written-v1", c.scope.DatabaseID, c.scope.IncarnationID, sqliteHash(allocationRaw), []string{}, []string{}, entries}
	raw, _ := json.Marshal(projection)
	c.unreleased.NeverWrittenSHA256 = sqliteHash(raw)
	c.scope.EmptyAllocation = &sqliteprep.EmptyAllocation{AllocationSHA256: sqliteHash(allocationRaw), NeverWrittenSHA256: sqliteHash(raw), DatabaseID: c.scope.DatabaseID, IncarnationID: c.scope.IncarnationID}
	c.scope.Checks = []data.RestoreInvariant{}
	return nil
}

func sqliteAllocationProducer(ctx context.Context, q dataQuerier, planID string) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT o.id FROM operations o WHERE o.plan_id=? AND o.kind='deploy' AND o.state='succeeded' AND EXISTS(SELECT 1 FROM events e WHERE e.operation_id=o.id AND e.kind='step' AND json_extract(e.payload,'$.step')='prepare_data' AND json_extract(e.payload,'$.outcome')='intent') AND EXISTS(SELECT 1 FROM events e WHERE e.operation_id=o.id AND e.kind='step' AND json_extract(e.payload,'$.step')='prepare_data' AND json_extract(e.payload,'$.outcome')='completed') LIMIT 2`, planID)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var id string
	count := 0
	for rows.Next() {
		if err = rows.Scan(&id); err != nil {
			return "", err
		}
		count++
	}
	if err = rows.Err(); err != nil || count != 1 {
		return "", errors.Join(ErrConflict, err)
	}
	return id, nil
}

func (c sqliteCurrent) unreleasedPending(ctx context.Context, q dataQuerier, in sqliteprep.Input) error {
	var count int
	query := `SELECT (SELECT count(*) FROM data_credential_rotations WHERE binding_id=? AND stage NOT IN ('active','verified','superseded'))+(SELECT count(*) FROM data_replica_revisions WHERE binding_id=? AND stage NOT IN ('active','cancelled'))`
	if err := q.QueryRowContext(ctx, query, c.scope.BindingID, c.scope.BindingID).Scan(&count); err != nil || count != 0 {
		return errors.Join(ErrConflict, err)
	}
	rows, err := q.QueryContext(ctx, "SELECT id FROM operations WHERE app=? AND state NOT IN ('succeeded','failed','rolled_back','recovery_required') ORDER BY id LIMIT 65", in.App)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || len(ids) > 64 {
		return errors.Join(ErrConflict, err)
	}
	inputHash, err := sqliteprep.Fingerprint(in)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = c.admitUnreleasedPending(ctx, q, id, in, inputHash); err != nil {
			return err
		}
	}
	return nil
}

func (c sqliteCurrent) admitUnreleasedPending(ctx context.Context, q dataQuerier, id string, in sqliteprep.Input, hash string) error {
	op, err := scanOperation(q.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", id))
	if err != nil {
		return err
	}
	if op.Kind == ops.RestoreTest {
		return nil
	}
	if string(op.Kind) == string(in.Kind) && op.SecretRef == hash {
		_, stored, err := readSQLiteTask(ctx, q, op)
		if err != nil || stored != in {
			return errors.Join(ErrConflict, err)
		}
		return nil
	}
	if !isSQLiteRetireKind(op.Kind) {
		return ErrConflict
	}
	_, retire, err := readSQLiteRetireOperation(ctx, q, id)
	if err != nil {
		return err
	}
	p, err := loadArchivePlan(ctx, q, retire.PlanID)
	if err != nil || p.Removal == nil {
		return errors.Join(ErrConflict, err)
	}
	for _, ref := range p.Removal.Points {
		if ref.DatabaseID != c.scope.DatabaseID {
			continue
		}
		var raw []byte
		if err = q.QueryRowContext(ctx, "SELECT canonical FROM data_sqlite_preparations WHERE id=?", ref.PreparationID).Scan(&raw); err != nil {
			return err
		}
		intent, err := sqliteprep.DecodeIntent(raw)
		if err != nil || intent.Input != in || intent.Fence != ref.Fence {
			return errors.Join(ErrConflict, err)
		}
		return nil
	}
	return ErrConflict
}

func sqliteInitEntries(ctx context.Context, q dataQuerier, database data.DatabaseID) ([]sqliteInitHistory, error) {
	rows, err := q.QueryContext(ctx, `SELECT o.id,o.plan_id,o.fence_id FROM data_init_operations o JOIN data_init_plans p ON p.id=o.plan_id JOIN data_fences f ON f.id=o.fence_id WHERE f.database_id=? OR json_extract(p.canonical,'$.database.database_id')=? ORDER BY o.id LIMIT 65`, database, database)
	if err != nil {
		return nil, err
	}
	entries := []sqliteInitHistory{}
	for rows.Next() {
		var entry sqliteInitHistory
		if err = rows.Scan(&entry.Operation, &entry.Plan, &entry.Fence); err != nil {
			break
		}
		entries = append(entries, entry)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || len(entries) > 64 {
		return nil, errors.Join(ErrConflict, err)
	}
	for index := range entries {
		if err = sqliteInitTerminal(ctx, q, &entries[index]); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func sqliteInitTerminal(ctx context.Context, q dataQuerier, entry *sqliteInitHistory) error {
	rows, err := q.QueryContext(ctx, "SELECT sequence,state,created_at,receipt FROM data_init_events WHERE operation_id=? ORDER BY sequence LIMIT 10", entry.Operation)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	previous := ""
	var previousAt time.Time
	for rows.Next() {
		var sequence int
		var state, at string
		var receipt []byte
		if err = rows.Scan(&sequence, &state, &at, &receipt); err != nil {
			return err
		}
		eventAt, timeErr := time.Parse(time.RFC3339Nano, at)
		if timeErr != nil || eventAt.IsZero() || eventAt.Before(previousAt) || state == "restore_point_verified" && len(receipt) == 0 || state != "restore_point_verified" && receipt != nil || sequence != entry.Sequence+1 || !sqliteInitEdge(previous, state) {
			return &IntegrityError{}
		}
		raw, _ := json.Marshal([]any{sequence, state, at, receipt})
		entry.Sequence, entry.Terminal, entry.EventSHA256 = sequence, state, sqliteHash(raw)
		if entry.Sequence == 1 {
			entry.startedAt = eventAt
		}
		entry.settledAt = eventAt
		previous, previousAt = state, eventAt
	}
	if err = rows.Err(); err != nil || entry.Terminal != "succeeded" && entry.Terminal != "not_initialized" {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func sqliteInitEdge(from, to string) bool {
	if from == "" {
		return to == "intent"
	}
	if from == "succeeded" || from == "not_initialized" {
		return false
	}
	if to == "not_initialized" {
		return true
	}
	if to == "succeeded" {
		return from == "restore_point_verified" || from == "mutation_intent" || from == "mutation_completed"
	}
	return map[string]string{"intent": "quiesced", "quiesced": "restore_point_intent", "restore_point_intent": "restore_point_verified", "restore_point_verified": "mutation_intent", "mutation_intent": "mutation_completed"}[from] == to
}

func (c sqliteCurrent) validateInitEntry(ctx context.Context, q dataQuerier, entry sqliteInitHistory, declared sqliteDeclaration) (datainit.Plan, string, error) {
	p, err := readInitPlan(ctx, q, entry.Plan)
	if err != nil {
		return p, "", err
	}
	if p.Database != c.permit.Database || p.ReplicaEpoch != c.scope.EpochID || p.RemotePrefix != strings.TrimSuffix(c.scope.RemotePrefix, "/") || p.Request.App != c.scope.App || p.TargetHash != c.unreleased.DeclarationTargetSHA256 {
		return p, "", ErrConflict
	}
	var fenceState string
	if err = q.QueryRowContext(ctx, "SELECT state FROM data_fences WHERE id=? AND operation_id=? AND database_id=? AND incarnation_id=?", entry.Fence, entry.Operation, c.scope.DatabaseID, c.scope.IncarnationID).Scan(&fenceState); err != nil || fenceState != "released" {
		return p, "", errors.Join(ErrConflict, err)
	}
	var outer string
	var outerCount int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM operations WHERE kind='data_init_apply' AND secret_ref=? AND requester=? AND app=?", p.ID, p.Requester.String(), p.Request.App).Scan(&outerCount); err != nil || outerCount != 1 {
		return p, "", errors.Join(ErrConflict, err)
	}
	if err = q.QueryRowContext(ctx, "SELECT id FROM operations WHERE kind='data_init_apply' AND secret_ref=? AND requester=? AND app=? AND state IN ('succeeded','failed','recovery_required')", p.ID, p.Requester.String(), p.Request.App).Scan(&outer); err != nil {
		return p, "", err
	}
	origin, d, err := readUnreleasedDeclaration(ctx, q, p.Request.FirstReleasePlan, c.scope.IncarnationID)
	if err != nil || origin.DesiredHash != p.DesiredHash || origin.Target != c.unreleased.DeclarationTarget {
		return p, "", errors.Join(ErrConflict, err)
	}
	copyCurrent := c
	original, err := copyCurrent.declaration(ctx, q, d)
	if err != nil || !sqliteSameJSON(original, declared) {
		return p, "", errors.Join(ErrConflict, err)
	}
	var verifications int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM data_init_events WHERE operation_id=? AND state='restore_point_verified'", entry.Operation).Scan(&verifications); err != nil || verifications > 1 {
		return p, "", errors.Join(ErrConflict, err)
	}
	if verifications == 1 {
		if err = validateInitVerification(ctx, q, p, entry.Operation); err != nil {
			return p, "", err
		}
	}
	if entry.Terminal != "succeeded" {
		return p, outer, nil
	}
	if verifications != 1 {
		return p, "", ErrConflict
	}

	var count int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM data_writer_history WHERE database_id=? AND operation_id=?", c.scope.DatabaseID, entry.Operation).Scan(&count); err != nil || count != 1 || c.accepted[p.Definition.Marker] != p.Definition.CatalogSHA256 {
		return p, "", errors.Join(ErrConflict, err)
	}
	return p, outer, nil
}

func validateInitVerification(ctx context.Context, q dataQuerier, p datainit.Plan, operation string) error {
	var raw, registeredRaw []byte
	if err := q.QueryRowContext(ctx, "SELECT receipt FROM data_init_events WHERE operation_id=? AND state='restore_point_verified'", operation).Scan(&raw); err != nil {
		return err
	}
	var verified datainit.VerifiedRestorePoint
	if len(raw) > 16384 || json.Unmarshal(raw, &verified) != nil || !verified.Admits(p, verified.Receipt.ObservedAt) || verified.Receipt.OperationID != operation+"-empty-verify" {
		return &IntegrityError{}
	}
	canonical, _ := json.Marshal(verified)
	if !bytes.Equal(raw, canonical) {
		return &IntegrityError{}
	}
	if err := q.QueryRowContext(ctx, "SELECT canonical FROM data_restore_points WHERE id=? AND binding_id=? AND epoch_id=?", verified.PointID, p.Database.ReplicaBindingID, p.ReplicaEpoch).Scan(&registeredRaw); err != nil {
		return err
	}
	var registered data.SQLiteRestorePoint
	source := verified.Receipt.Source.Snapshot
	if json.Unmarshal(registeredRaw, &registered) != nil || registered.Validate() != nil || registered.Kind != data.RestorePointSnapshot || registered.Snapshot == nil || registered.ID != verified.PointID || registered.Schema.State != data.VerifiedEmpty || registered.Schema.DatabaseID != p.Database.DatabaseID || registered.Snapshot.ObjectKey != source.ObjectKey || registered.Snapshot.SHA256 != source.SHA256 || registered.Snapshot.Size != source.Size || !registered.RecordedAt.Equal(verified.Receipt.ObservedAt) {
		return &IntegrityError{}
	}
	return nil
}

// validateRetainedUnreleased checks immutable producing evidence only. It does
// not require active ownership, today's admission, packet, or remote freshness.
func validateRetainedUnreleased(ctx context.Context, q dataQuerier, i sqliteprep.Intent) error {
	effective, err := effectiveSQLiteInput(ctx, q, i.Input)
	if err != nil {
		return err
	}
	if i.Scope.ReplicaCommitted {
		if effective.DeclarationPlanID != "" {
			return &IntegrityError{}
		}
		return nil
	}
	if effective.DeclarationPlanID == "" || effective.DeliveredCredentialVersion != i.Scope.CredentialVersion {
		return &IntegrityError{}
	}
	p, d, err := loadPlan(ctx, q, effective.DeclarationPlanID)
	if err != nil || validateUnreleasedDecision(p, d) != nil || validateUnreleasedDesired(d) != nil || p.App != i.Scope.App || p.Kind != plan.Create && p.Kind != plan.Update || len(p.Conflicts) != 0 {
		return errors.Join(ErrConflict, err)
	}
	permit, err := readReplicaPermit(ctx, q, i.Scope.DatabaseID)
	if err != nil {
		return err
	}
	if permit.Database.IncarnationID != i.Scope.IncarnationID || permit.Database.Name != i.Scope.DatabaseName || permit.Replica.BindingID != i.Scope.BindingID || permit.Replica.EpochID != i.Scope.EpochID {
		return ErrConflict
	}
	c := sqliteCurrent{scope: i.Scope, permit: permit}
	declaration, err := c.declaration(ctx, q, d)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(declaration)
	checks := declaration.Checks
	if i.Scope.EmptyAllocation != nil {
		checks = []data.RestoreInvariant{}
	}
	if sqliteHash(raw) != i.Scope.DeclarationSHA256 || !sqliteSameJSON(checks, i.Scope.Checks) {
		return &IntegrityError{}
	}
	record, err := readSQLiteCredential(ctx, q, i.Scope.App, permit.Replica, i.Scope.CredentialVersion)
	if err != nil {
		return err
	}
	host, err := readUnreleasedDelivery(ctx, q, record)
	targetRaw, _ := json.Marshal(p.Target)
	if err != nil || host != "sha256:"+sqliteHash(targetRaw) {
		return errors.Join(ErrConflict, err)
	}
	allocation, err := readPreparationEvidence(ctx, q, i.Scope.DatabaseID)
	if err != nil || allocation == nil || allocation.IncarnationID != i.Scope.IncarnationID || allocation.DirectoryDevice != i.Location.Device || allocation.DirectoryInode != i.Location.Inode {
		return errors.Join(ErrConflict, err)
	}
	if p.Lifecycle == plan.PrepareData {
		_, err = sqliteAllocationProducer(ctx, q, p.Hash)
		return err
	}
	if p.Lifecycle != "" {
		return ErrConflict
	}
	return retainedUnreleasedInitializer(ctx, q, p, i)
}

func retainedUnreleasedInitializer(ctx context.Context, q dataQuerier, p plan.Plan, i sqliteprep.Intent) error {
	var operation, planID string
	err := q.QueryRowContext(ctx, `SELECT o.id,o.plan_id FROM data_init_operations o JOIN data_init_plans p ON p.id=o.plan_id JOIN data_init_events e ON e.operation_id=o.id WHERE json_extract(p.canonical,'$.database.database_id')=? AND e.state='succeeded' AND json_extract(p.canonical,'$.request.first_release_plan')=?`, i.Scope.DatabaseID, p.Hash).Scan(&operation, &planID)
	if err != nil {
		return err
	}
	initialized, err := readInitPlan(ctx, q, planID)
	if err != nil || initialized.DesiredHash != p.DesiredHash || initialized.Database.DatabaseID != i.Scope.DatabaseID || initialized.ReplicaEpoch != i.Scope.EpochID {
		return errors.Join(ErrConflict, err)
	}
	return validateInitVerification(ctx, q, initialized, operation)
}

func validateUnreleasedDecision(p plan.Plan, d policy.Desired) error {
	input := p.DecisionInput()
	if len(input) == 0 {
		return ErrConflict
	}
	if _, err := plan.CompareDecisionInputs(input, input); err != nil {
		return err
	}
	var projection struct {
		Desired json.RawMessage `json:"desired"`
		Plan    plan.Plan       `json:"plan"`
	}
	if json.Unmarshal(input, &projection) != nil {
		return &IntegrityError{}
	}
	desired, err := d.CanonicalBytes()
	if err != nil || !bytes.Equal(desired, projection.Desired) {
		return errors.Join(ErrConflict, err)
	}
	p.Hash = ""
	expected, err := p.CanonicalBytes()
	actual, actualErr := projection.Plan.CanonicalBytes()
	if err != nil || actualErr != nil || !bytes.Equal(expected, actual) {
		return &IntegrityError{}
	}
	return nil
}

func sqliteUnreleasedActivationProducers(ctx context.Context, q dataQuerier, app string, binding data.ReplicaBindingID) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM operations WHERE app=? AND kind='credential_activation' AND secret_ref IN (SELECT json_extract(canonical,'$.plan_id') FROM data_credential_records WHERE binding_id=?) ORDER BY id LIMIT 65`, app, binding)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || len(ids) > 64 {
		return nil, errors.Join(ErrConflict, err)
	}
	return ids, nil
}
