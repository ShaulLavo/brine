package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restic"
)

type NativeIncomingRepositoryMetadata struct {
	Material            NativeRepositoryMaterial
	CredentialEpochID   data.ReplicaEpochID
	CreationGrantSHA256 string
	IntentID            string
}

type nativeRepositoryInitEvent struct {
	IntentID       string    `json:"intent_id"`
	IntentSHA256   string    `json:"intent_sha256"`
	LeaseSHA256    string    `json:"lease_sha256"`
	Sequence       uint64    `json:"sequence"`
	Stage          string    `json:"stage"`
	PreviousSHA256 string    `json:"previous_sha256"`
	ObservedAt     time.Time `json:"observed_at"`
}

type NativeInitializedRepositoryResult struct {
	IntentID     string       `json:"intent_id"`
	IntentSHA256 string       `json:"intent_sha256"`
	RepositoryID string       `json:"repository_id"`
	ConfigSHA256 string       `json:"config_sha256"`
	Scope        restic.Scope `json:"scope"`
	VerifiedAt   time.Time    `json:"verified_at"`
}

func (r *NativeIncomingRepository) Metadata(ctx context.Context) (NativeIncomingRepositoryMetadata, error) {
	if r == nil || r.store == nil {
		return NativeIncomingRepositoryMetadata{}, ErrConflict
	}
	tx, err := r.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeIncomingRepositoryMetadata{}, err
	}
	defer func() { _ = tx.Rollback() }()
	intent, err := r.store.readNativeIncomingRepositoryTx(ctx, tx, r)
	if err != nil {
		return NativeIncomingRepositoryMetadata{}, err
	}
	metadata, err := nativeIncomingRepositoryMetadata(ctx, tx, intent)
	if err != nil || !r.active(r.store) {
		return NativeIncomingRepositoryMetadata{}, errors.Join(ErrConflict, err)
	}
	return metadata, tx.Commit()
}

func nativeIncomingRepositoryMetadata(ctx context.Context, q dataQuerier, intent nativeRepositoryIntent) (NativeIncomingRepositoryMetadata, error) {
	policy, _, err := readNativeBackupPolicy(ctx, q, intent.record.BindingID)
	if err != nil || policy.EpochID != intent.record.SourceEpochID {
		return NativeIncomingRepositoryMetadata{}, errors.Join(ErrConflict, err)
	}
	source, err := readReservedEngine(ctx, q, intent.record.BindingID)
	if err != nil {
		return NativeIncomingRepositoryMetadata{}, err
	}
	archive := source.Archive
	if archive.EpochID != policy.EpochID {
		_, archive, err = readNativeEpoch(ctx, q, intent.record.BindingID, policy.EpochID)
		if err != nil {
			return NativeIncomingRepositoryMetadata{}, err
		}
	}
	d := archive.Destination
	location := restic.S3Location{Endpoint: d.Endpoint, Bucket: d.Bucket, Prefix: strings.TrimSuffix(archive.RemotePrefix, "/"), Region: d.Region, PathStyle: d.PathStyle}
	point := restic.TransportPoint{Scope: restic.Scope{BindingID: string(policy.BindingID), EpochID: string(policy.EpochID), RepositoryID: policy.RepositoryID, DestinationSHA256: location.SHA256(), CredentialVersion: policy.CredentialVersion, RecoveryVersion: policy.RecoveryVersion}}
	material, err := readNativeRepositoryMaterial(ctx, q, point)
	if err != nil {
		return NativeIncomingRepositoryMetadata{}, err
	}
	incoming := intent.archive.Destination
	material.Location = restic.S3Location{Endpoint: incoming.Endpoint, Bucket: incoming.Bucket, Prefix: strings.TrimSuffix(intent.archive.RemotePrefix, "/"), Region: incoming.Region, PathStyle: incoming.PathStyle}
	material.Scope.EpochID = string(intent.epochID)
	material.Scope.RepositoryID = ""
	material.Scope.DestinationSHA256 = material.Location.SHA256()
	return NativeIncomingRepositoryMetadata{Material: material, CredentialEpochID: policy.EpochID, CreationGrantSHA256: intent.creationGrantSHA256, IntentID: intent.creationID}, nil
}

func (s *Store) BeginNativeIncomingInitialization(ctx context.Context, r *NativeIncomingRepository) error {
	if s.readOnly || !r.active(s) {
		return ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	intent, err := s.readNativeIncomingRepositoryTx(ctx, tx, r)
	if err != nil {
		return err
	}
	events, err := readNativeRepositoryInitEvents(ctx, tx, intent, r)
	if err != nil || len(events) != 0 {
		return errors.Join(ErrConflict, err)
	}
	if _, err := nativeIncomingRepositoryMetadata(ctx, tx, intent); err != nil {
		return err
	}
	if err := appendNativeRepositoryInitEvent(ctx, tx, intent, r, events, "init_issued"); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *NativeIncomingRepository) AdmitRepository(ctx context.Context, scope restic.Scope, action restic.RepositoryAction, grant string) error {
	if r == nil || r.store == nil || action != restic.CreateRepository && action != restic.ReadRepository {
		return ErrConflict
	}
	metadata, err := r.Metadata(ctx)
	expected := metadata.Material.Scope
	expected.ExpiresAt = scope.ExpiresAt
	if err != nil || expected != scope || grant != metadata.CreationGrantSHA256 || !time.Now().UTC().Before(scope.ExpiresAt) || scope.ExpiresAt.After(metadata.Material.Scope.ExpiresAt) {
		return errors.Join(ErrConflict, err)
	}
	tx, cancel, err := r.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	intent, err := r.store.readNativeIncomingRepositoryTx(ctx, tx, r)
	if err != nil {
		return err
	}
	events, err := readNativeRepositoryInitEvents(ctx, tx, intent, r)
	if err != nil || len(events) == 0 || events[len(events)-1].Stage == "initialized" || action == restic.CreateRepository && (len(events) != 1 || events[0].Stage != "init_issued") {
		return errors.Join(ErrConflict, err)
	}
	if action == restic.CreateRepository {
		if err := appendNativeRepositoryInitEvent(ctx, tx, intent, r, events, "init_unknown"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func readNativeRepositoryInitEvents(ctx context.Context, q dataQuerier, intent nativeRepositoryIntent, r *NativeIncomingRepository) ([]nativeRepositoryInitEvent, error) {
	rows, err := q.QueryContext(ctx, "SELECT sequence,stage,previous_sha256,canonical FROM data_native_repository_init_events WHERE intent_id=? ORDER BY sequence", intent.creationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	events := []nativeRepositoryInitEvent{}
	previous := intent.hash
	for rows.Next() {
		var event nativeRepositoryInitEvent
		var raw []byte
		var sequence uint64
		var stage, parent string
		if err := rows.Scan(&sequence, &stage, &parent, &raw); err != nil {
			return nil, err
		}
		if len(raw) > 16384 || json.Unmarshal(raw, &event) != nil {
			return nil, &IntegrityError{}
		}
		canonical, _ := json.Marshal(event)
		if !bytes.Equal(raw, canonical) || sequence != uint64(len(events))+1 || event.Sequence != sequence || event.Stage != stage || event.PreviousSHA256 != parent || parent != previous || event.IntentID != intent.creationID || event.IntentSHA256 != intent.hash || event.LeaseSHA256 != nativePointHash(r.leaseRaw) || event.ObservedAt.IsZero() || event.ObservedAt.Location() != time.UTC || event.ObservedAt.After(time.Now().UTC()) || !validNativeRepositoryInitStage(events, stage) {
			return nil, &IntegrityError{}
		}
		events = append(events, event)
		previous = nativePointHash(raw)
	}
	return events, rows.Err()
}

func validNativeRepositoryInitStage(events []nativeRepositoryInitEvent, stage string) bool {
	if len(events) == 0 {
		return stage == "init_issued"
	}
	if len(events) > 2 || events[len(events)-1].Stage == "initialized" {
		return false
	}
	return stage == "initialized" || len(events) == 1 && stage == "init_unknown"
}

func appendNativeRepositoryInitEvent(ctx context.Context, tx *sql.Tx, intent nativeRepositoryIntent, r *NativeIncomingRepository, events []nativeRepositoryInitEvent, stage string) error {
	if !validNativeRepositoryInitStage(events, stage) {
		return ErrConflict
	}
	previous := intent.hash
	if len(events) != 0 {
		raw, _ := json.Marshal(events[len(events)-1])
		previous = nativePointHash(raw)
	}
	event := nativeRepositoryInitEvent{IntentID: intent.creationID, IntentSHA256: intent.hash, LeaseSHA256: nativePointHash(r.leaseRaw), Sequence: uint64(len(events)) + 1, Stage: stage, PreviousSHA256: previous, ObservedAt: time.Now().UTC()}
	raw, _ := json.Marshal(event)
	_, err := tx.ExecContext(ctx, "INSERT INTO data_native_repository_init_events VALUES(?,?,?,?,?)", intent.creationID, event.Sequence, stage, previous, raw)
	return err
}

func (s *Store) RecordNativeIncomingInitialized(ctx context.Context, r *NativeIncomingRepository, proof restic.InitializedRepository) (NativeInitializedRepositoryResult, error) {
	if s.readOnly || !r.active(s) {
		return NativeInitializedRepositoryResult{}, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return NativeInitializedRepositoryResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	intent, err := s.readNativeIncomingRepositoryTx(ctx, tx, r)
	if err != nil {
		return NativeInitializedRepositoryResult{}, err
	}
	events, err := readNativeRepositoryInitEvents(ctx, tx, intent, r)
	if err != nil || len(events) == 0 || events[len(events)-1].Stage == "initialized" {
		return NativeInitializedRepositoryResult{}, errors.Join(ErrConflict, err)
	}
	metadata, err := nativeIncomingRepositoryMetadata(ctx, tx, intent)
	expected := metadata.Material.Scope
	expected.ExpiresAt = proof.Scope().ExpiresAt
	if err != nil || expected.ExpiresAt.After(metadata.Material.Scope.ExpiresAt) || !proof.AdmitsCreation(expected, intent.creationGrantSHA256) || proof.VerifiedAt().Before(events[0].ObservedAt) {
		return NativeInitializedRepositoryResult{}, errors.Join(ErrConflict, err)
	}
	result := NativeInitializedRepositoryResult{IntentID: intent.creationID, IntentSHA256: intent.hash, RepositoryID: proof.Scope().RepositoryID, ConfigSHA256: proof.ConfigurationSHA256(), Scope: proof.Scope(), VerifiedAt: proof.VerifiedAt()}
	raw, _ := json.Marshal(result)
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_repository_results VALUES(?,?,?)", intent.creationID, result.RepositoryID, raw)
	if err != nil {
		return NativeInitializedRepositoryResult{}, err
	}
	if err := appendNativeRepositoryInitEvent(ctx, tx, intent, r, events, "initialized"); err != nil {
		return NativeInitializedRepositoryResult{}, err
	}
	if !r.active(s) {
		return NativeInitializedRepositoryResult{}, ErrConflict
	}
	return result, tx.Commit()
}

func (s *Store) ReadNativeIncomingInitialized(ctx context.Context, r *NativeIncomingRepository) (NativeInitializedRepositoryResult, error) {
	if !r.active(s) {
		return NativeInitializedRepositoryResult{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeInitializedRepositoryResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	intent, err := s.readNativeIncomingRepositoryTx(ctx, tx, r)
	if err != nil {
		return NativeInitializedRepositoryResult{}, err
	}
	events, err := readNativeRepositoryInitEvents(ctx, tx, intent, r)
	if err != nil || len(events) == 0 || events[len(events)-1].Stage != "initialized" {
		return NativeInitializedRepositoryResult{}, errors.Join(ErrConflict, err)
	}
	var raw []byte
	var repositoryID string
	err = tx.QueryRowContext(ctx, "SELECT repository_id,canonical FROM data_native_repository_results WHERE intent_id=?", intent.creationID).Scan(&repositoryID, &raw)
	var result NativeInitializedRepositoryResult
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &result) != nil {
		return result, errors.Join(&IntegrityError{}, err)
	}
	canonical, _ := json.Marshal(result)
	metadata, err := nativeIncomingRepositoryMetadata(ctx, tx, intent)
	expected := metadata.Material.Scope
	expected.RepositoryID = repositoryID
	expected.ExpiresAt = result.Scope.ExpiresAt
	if err != nil || !bytes.Equal(raw, canonical) || result.IntentID != intent.creationID || result.IntentSHA256 != intent.hash || result.Scope != expected || result.RepositoryID != repositoryID || !data.ValidCatalogHash(repositoryID) || !data.ValidCatalogHash(result.ConfigSHA256) || result.VerifiedAt.Before(events[0].ObservedAt) || result.VerifiedAt.After(events[len(events)-1].ObservedAt) || !r.active(s) {
		return result, &IntegrityError{}
	}
	return result, tx.Commit()
}
