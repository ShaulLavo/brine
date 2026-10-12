package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
)

type PublicationEffect struct {
	Scope        datapublish.ProofScope       `json:"scope"`
	Nonce        string                       `json:"nonce"`
	Step         datapublish.Step             `json:"step"`
	RootPath     string                       `json:"root_path"`
	RootIdentity data.EngineDirectoryIdentity `json:"root_identity"`
	Runtime      data.EngineRuntime           `json:"runtime"`
	RecordedAt   time.Time                    `json:"recorded_at"`
}

type PublicationEffectEvent struct {
	Nonce       string    `json:"nonce"`
	Stage       string    `json:"stage"`
	ContainerID string    `json:"container_id"`
	PID         uint32    `json:"pid"`
	StartTicks  uint64    `json:"start_ticks"`
	Cgroup      string    `json:"cgroup"`
	RecordedAt  time.Time `json:"recorded_at"`
}

func (s *Store) RecordPublicationEffect(ctx context.Context, attempt *PublicationAttempt, nonce string, step datapublish.Step) error {
	if !attempt.active(s) || !data.ValidID(nonce) || attempt.restored.unchanged(ctx) != nil {
		return ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, attempt)
	if err != nil || current.cursor.State() != datapublish.IntentRecorded || current.cursor.IntendedStep() != step || step != datapublish.CheckPreflight && step != datapublish.PublishStage {
		return errors.Join(ErrConflict, err)
	}
	var existing int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_module_publication_effects WHERE plan_id=? AND step=?", attempt.planID, step).Scan(&existing); err != nil || existing != 0 {
		return errors.Join(ErrConflict, err)
	}
	permit, err := readEnginePermit(ctx, tx, attempt.binding)
	if err != nil {
		return err
	}
	effect := PublicationEffect{Scope: current.cursor.Scope(), Nonce: nonce, Step: step, RootPath: filepath.Join(attempt.restored.parent.Name(), "download"), Runtime: permit.Runtime.Clone(), RecordedAt: time.Now().UTC()}
	if !data.ValidRoot(effect.RootPath) || attempt.restored.unchanged(ctx) != nil {
		return ErrConflict
	}
	actual, err := os.Lstat(effect.RootPath)
	rootProof, proofErr := data.InspectRoot(effect.RootPath)
	if err != nil || proofErr != nil || !os.SameFile(actual, attempt.restored.identity) {
		return errors.Join(ErrConflict, err, proofErr)
	}
	effect.RootIdentity = data.EngineDirectoryIdentity{Device: rootProof.Device, Inode: rootProof.Inode}
	raw, err := json.Marshal(effect)
	if err != nil {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_module_publication_effects VALUES(?,?,?,?,?)", nonce, attempt.planID, attempt.operationID, step, raw)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecordPublicationEffectEvent(ctx context.Context, attempt *PublicationAttempt, event PublicationEffectEvent) error {
	if !attempt.active(s) || !data.ValidID(event.Nonce) || event.Stage != "started" && event.Stage != "stop_intent" && event.Stage != "disposed" && event.Stage != "unknown" {
		return ErrConflict
	}
	if event.Stage != "unknown" && (!data.ValidCatalogHash(event.ContainerID) || event.PID == 0 || event.StartTicks == 0 || event.Cgroup == "") {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = s.readPublicationAttemptTx(ctx, tx, attempt); err != nil {
		return err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_module_publication_effects WHERE nonce=? AND plan_id=? AND operation_id=?", event.Nonce, attempt.planID, attempt.operationID).Scan(&raw); err != nil {
		return err
	}
	var effect PublicationEffect
	if json.Unmarshal(raw, &effect) != nil || effect.Nonce != event.Nonce || effect.Scope.ApplyOpID != attempt.operationID {
		return &IntegrityError{}
	}
	var sequence uint64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0)+1 FROM data_module_publication_effect_events WHERE nonce=?", event.Nonce).Scan(&sequence); err != nil {
		return err
	}
	if event.Stage == "started" && sequence != 1 {
		return ErrConflict
	}
	if event.Stage == "stop_intent" {
		var stops int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_module_publication_effect_events WHERE nonce=? AND stage='stop_intent'", event.Nonce).Scan(&stops); err != nil || stops != 0 {
			return errors.Join(ErrConflict, err)
		}
	}
	event.RecordedAt = time.Now().UTC()
	raw, err = json.Marshal(event)
	if err != nil {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_module_publication_effect_events VALUES(?,?,?,?)", event.Nonce, sequence, event.Stage, raw)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func readPublicationEffectTx(ctx context.Context, tx *sql.Tx, nonce string) (PublicationEffect, error) {
	var raw []byte
	var effect PublicationEffect
	if err := tx.QueryRowContext(ctx, "SELECT canonical FROM data_module_publication_effects WHERE nonce=?", nonce).Scan(&raw); err != nil {
		return effect, err
	}
	if json.Unmarshal(raw, &effect) != nil || effect.Nonce != nonce {
		return effect, &IntegrityError{}
	}
	return effect, nil
}
