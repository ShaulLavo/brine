package store

import (
	"bytes"
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
	effect, err := readPublicationEffectTx(ctx, tx, event.Nonce)
	if err != nil {
		return err
	}
	if effect.Scope.PlanID != attempt.planID || effect.Scope.ApplyOpID != attempt.operationID {
		return &IntegrityError{}
	}
	events, err := readPublicationEffectEventsTx(ctx, tx, effect)
	if err != nil {
		return err
	}
	event.RecordedAt = time.Now().UTC()
	if checkPublicationEffectEvent(effect, events, event) != nil {
		return ErrConflict
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_module_publication_effect_events VALUES(?,?,?,?)", event.Nonce, len(events)+1, event.Stage, raw)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func readPublicationEffectTx(ctx context.Context, tx *sql.Tx, nonce string) (PublicationEffect, error) {
	var raw []byte
	var effect PublicationEffect
	var plan, operation string
	var step datapublish.Step
	if err := tx.QueryRowContext(ctx, "SELECT plan_id,operation_id,step,canonical FROM data_module_publication_effects WHERE nonce=?", nonce).Scan(&plan, &operation, &step, &raw); err != nil {
		return effect, err
	}
	if len(raw) == 0 || len(raw) > 65536 || json.Unmarshal(raw, &effect) != nil {
		return effect, &IntegrityError{}
	}
	canonical, _ := json.Marshal(effect)
	if !bytes.Equal(raw, canonical) || !data.ValidID(nonce) || effect.Nonce != nonce || effect.Scope.PlanID != plan || effect.Scope.ApplyOpID != operation || effect.Step != step || step != datapublish.CheckPreflight && step != datapublish.PublishStage || !data.ValidRoot(effect.RootPath) || effect.RootIdentity.Device == 0 || effect.RootIdentity.Inode == 0 || effect.RecordedAt.IsZero() || effect.RecordedAt.Location() != time.UTC || effect.RecordedAt.After(time.Now().UTC()) {
		return effect, &IntegrityError{}
	}
	return effect, nil
}

func readPublicationEffectEventsTx(ctx context.Context, tx *sql.Tx, effect PublicationEffect) ([]PublicationEffectEvent, error) {
	rows, err := tx.QueryContext(ctx, "SELECT sequence,stage,canonical FROM data_module_publication_effect_events WHERE nonce=? ORDER BY sequence", effect.Nonce)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var events []PublicationEffectEvent
	for rows.Next() {
		var event PublicationEffectEvent
		var raw []byte
		var sequence int
		var stage string
		if rows.Scan(&sequence, &stage, &raw) != nil || sequence != len(events)+1 || len(raw) == 0 || len(raw) > 65536 || json.Unmarshal(raw, &event) != nil || event.Stage != stage {
			return nil, &IntegrityError{}
		}
		canonical, _ := json.Marshal(event)
		if !bytes.Equal(raw, canonical) || checkPublicationEffectEvent(effect, events, event) != nil {
			return nil, &IntegrityError{}
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func checkPublicationEffectEvent(effect PublicationEffect, prior []PublicationEffectEvent, next PublicationEffectEvent) error {
	if next.Nonce != effect.Nonce || next.RecordedAt.Location() != time.UTC || next.RecordedAt.Before(effect.RecordedAt) || next.RecordedAt.After(time.Now().UTC()) || next.Stage != "started" && next.Stage != "stop_intent" && next.Stage != "disposed" && next.Stage != "unknown" {
		return ErrConflict
	}
	if next.Stage == "started" && len(prior) != 0 {
		return ErrConflict
	}
	if next.Stage != "unknown" && (!data.ValidCatalogHash(next.ContainerID) || next.PID <= 1 || next.StartTicks == 0 || next.Cgroup == "") {
		return ErrConflict
	}
	for _, event := range prior {
		if event.Stage == next.Stage || next.RecordedAt.Before(event.RecordedAt) || next.Stage == "stop_intent" && (event.Stage == "unknown" || event.Stage == "disposed") {
			return ErrConflict
		}
	}
	if next.Stage == "stop_intent" || next.Stage == "disposed" {
		if len(prior) == 0 || prior[0].Stage != "started" || next.ContainerID != prior[0].ContainerID || next.PID != prior[0].PID || next.StartTicks != prior[0].StartTicks || next.Cgroup != prior[0].Cgroup {
			return ErrConflict
		}
	}
	return nil
}
