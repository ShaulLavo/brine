package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
)

type NativeInitialStageIntent struct {
	Format            string                `json:"format"`
	Nonce             string                `json:"nonce"`
	WorkspaceID       string                `json:"workspace_id"`
	CopyID            string                `json:"copy_id"`
	PointID           string                `json:"point_id"`
	OperationID       string                `json:"operation_id"`
	BindingID         data.ReplicaBindingID `json:"binding_id"`
	EpochID           data.ReplicaEpochID   `json:"epoch_id"`
	AllocationID      data.DatabaseID       `json:"allocation_id"`
	PreparationSHA256 string                `json:"preparation_sha256"`
	StopSHA256        string                `json:"stop_sha256"`
	RuntimeSHA256     string                `json:"runtime_sha256"`
	SourceSealSHA256  string                `json:"source_seal_sha256"`
	CreatedAt         time.Time             `json:"created_at"`
}

type NativeInitialStageObservation struct {
	Stage           string                        `json:"stage"`
	Parent          *data.EngineDirectoryIdentity `json:"parent"`
	Directory       *data.EngineDirectoryIdentity `json:"directory"`
	SourceSHA256    string                        `json:"source_sha256"`
	ContainerID     string                        `json:"container_id"`
	PID             uint32                        `json:"pid"`
	StartTicks      uint64                        `json:"start_ticks"`
	Cgroup          string                        `json:"cgroup"`
	ChildPID        uint32                        `json:"child_pid"`
	ChildStartTicks uint64                        `json:"child_start_ticks"`
	Code            string                        `json:"code"`
	At              time.Time                     `json:"at"`
}

type NativeInitialStageEvent struct {
	IntentSHA256   string                        `json:"intent_sha256"`
	PreviousSHA256 string                        `json:"previous_sha256"`
	Sequence       uint64                        `json:"sequence"`
	Observation    NativeInitialStageObservation `json:"observation"`
}

// NativeInitialStageJournal is a private inventory, never consumer-absence or owner proof.
type NativeInitialStageJournal struct {
	Intent NativeInitialStageIntent
	Events []NativeInitialStageEvent
}

func (j NativeInitialStageJournal) IsDisposed() bool {
	return len(j.Events) > 0 && j.Events[len(j.Events)-1].Observation.Stage == "disposed"
}

type NativeInitialStage struct {
	state   *Store
	stopped *NativeStoppedPreparation
	intent  NativeInitialStageIntent
	raw     []byte
}

func (s *Store) BeginNativeInitialStage(ctx context.Context, stopped *NativeStoppedPreparation, nonce, workspace, copyID, seal string) (*NativeInitialStage, error) {
	if s.readOnly || !stopped.active(s) || stopped.completedPointSHA256 != "" || !data.ValidID(nonce) || !data.ValidID(workspace) || !data.ValidID(copyID) || !data.ValidCatalogHash(seal) {
		return nil, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = checkNativeStoppedCapability(ctx, tx, stopped); err != nil {
		return nil, err
	}
	permit, err := readEnginePermit(ctx, tx, stopped.preparation.intent.BindingID)
	if err != nil || permit.Runtime.State != data.EngineUnpublished || permit.Allocation == nil {
		return nil, ErrConflict
	}
	if err = checkNativeUntouchedHistory(ctx, tx, permit.Runtime); err != nil {
		return nil, err
	}
	v := NativeInitialStageIntent{Format: "brine-native-initial-stage-v1", Nonce: nonce, WorkspaceID: workspace, CopyID: copyID, PointID: stopped.PointID(), OperationID: stopped.OperationID(), BindingID: permit.Runtime.BindingID, EpochID: permit.Runtime.EpochID, AllocationID: permit.Allocation.DatabaseID, PreparationSHA256: nativePointHash(stopped.preparation.raw), StopSHA256: stopped.stopSHA256, RuntimeSHA256: stopped.preparation.intent.RuntimeSHA256, SourceSealSHA256: seal, CreatedAt: time.Now().UTC()}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM data_native_initial_stage_intents WHERE operation_id=?", v.OperationID).Scan(&count); err != nil || count >= 128 {
		return nil, ErrConflict
	}
	raw, _ := json.Marshal(v)
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_initial_stage_intents VALUES(?,?,?,?)", nonce, v.PointID, v.OperationID, raw); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &NativeInitialStage{state: s, stopped: stopped, intent: v, raw: raw}, nil
}

func (a *NativeInitialStage) Record(ctx context.Context, observed NativeInitialStageObservation) error {
	if a == nil || a.state == nil || a.state.readOnly || !a.stopped.active(a.state) {
		return ErrConflict
	}
	tx, cancel, err := a.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = checkNativeStoppedCapability(ctx, tx, a.stopped); err != nil {
		return err
	}
	journal, raw, err := readNativeInitialStage(ctx, tx, a.intent.Nonce)
	if err != nil || !bytes.Equal(raw, a.raw) {
		return ErrConflict
	}
	event := NativeInitialStageEvent{IntentSHA256: nativePointHash(raw), PreviousSHA256: nativePointHash(raw), Sequence: uint64(len(journal.Events)) + 1, Observation: observed}
	if len(journal.Events) > 0 {
		previous, _ := json.Marshal(journal.Events[len(journal.Events)-1])
		event.PreviousSHA256 = nativePointHash(previous)
	}
	if err = checkNativeInitialStageEvent(journal, event); err != nil {
		return err
	}
	encoded, _ := json.Marshal(event)
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_initial_stage_events VALUES(?,?,?)", a.intent.Nonce, event.Sequence, encoded); err != nil {
		return err
	}
	return tx.Commit()
}

func readNativeInitialStage(ctx context.Context, q dataQuerier, nonce string) (NativeInitialStageJournal, []byte, error) {
	var raw []byte
	var j NativeInitialStageJournal
	if err := q.QueryRowContext(ctx, "SELECT canonical FROM data_native_initial_stage_intents WHERE nonce=?", nonce).Scan(&raw); err != nil {
		return j, nil, err
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &j.Intent) != nil {
		return j, nil, &IntegrityError{}
	}
	canonical, _ := json.Marshal(j.Intent)
	v := j.Intent
	intent, preparation, err := readNativePreparationIntent(ctx, q, v.PointID)
	if err != nil || !bytes.Equal(raw, canonical) || v.Format != "brine-native-initial-stage-v1" || v.Nonce != nonce || !data.ValidID(v.Nonce) || !data.ValidID(v.WorkspaceID) || !data.ValidID(v.CopyID) || v.OperationID != intent.OperationID || v.BindingID != intent.BindingID || v.EpochID != intent.EpochID || v.AllocationID != intent.DatabaseID || intent.SourceKind != "unpublished" || v.PreparationSHA256 != nativePointHash(preparation) || !data.ValidCatalogHash(v.StopSHA256) || v.RuntimeSHA256 != intent.RuntimeSHA256 || !data.ValidCatalogHash(v.SourceSealSHA256) || v.CreatedAt.IsZero() || v.CreatedAt.Location() != time.UTC || v.CreatedAt.Before(intent.CreatedAt) {
		return j, nil, &IntegrityError{}
	}
	stops, err := readNativePreparationEvents(ctx, q, intent, preparation)
	if err != nil || len(stops) < 2 || stops[1].Stage != "stopped" || nativePointHash(stops[1].Proof) != v.StopSHA256 {
		return j, nil, &IntegrityError{}
	}
	rows, err := q.QueryContext(ctx, "SELECT canonical FROM data_native_initial_stage_events WHERE nonce=? ORDER BY sequence", nonce)
	if err != nil {
		return j, nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var encoded []byte
		var event NativeInitialStageEvent
		if scanErr := rows.Scan(&encoded); scanErr != nil {
			return j, nil, errors.Join(&IntegrityError{}, scanErr)
		}
		if len(encoded) > 16384 {
			return j, nil, errors.Join(&IntegrityError{}, errors.New("initial_stage_event_size"))
		}
		if decodeErr := json.Unmarshal(encoded, &event); decodeErr != nil {
			return j, nil, errors.Join(&IntegrityError{}, decodeErr)
		}
		canonical, _ := json.Marshal(event)
		if !bytes.Equal(encoded, canonical) {
			return j, nil, errors.Join(&IntegrityError{}, errors.New("initial_stage_event_canonical"))
		}
		if checkNativeInitialStageEvent(j, event) != nil {
			return j, nil, errors.Join(&IntegrityError{}, errors.New("initial_stage_event_chain"))
		}
		j.Events = append(j.Events, event)
	}
	return j, raw, rows.Err()
}

func checkNativeInitialStageEvent(j NativeInitialStageJournal, e NativeInitialStageEvent) error {
	intent, _ := json.Marshal(j.Intent)
	previous := nativePointHash(intent)
	last := NativeInitialStageObservation{Stage: "intent", At: j.Intent.CreatedAt}
	if len(j.Events) > 0 {
		old := j.Events[len(j.Events)-1]
		raw, _ := json.Marshal(old)
		previous = nativePointHash(raw)
		last = old.Observation
	}
	v := e.Observation
	if len(j.Events) >= 32 || e.Sequence != uint64(len(j.Events))+1 || e.IntentSHA256 != nativePointHash(intent) || e.PreviousSHA256 != previous || v.At.IsZero() || v.At.Location() != time.UTC || v.At.Before(last.At) || v.At.After(time.Now().UTC()) || !nativeInitialStageTransition(last.Stage, v.Stage) || !nativeInitialStageIdentity(last, v) {
		return ErrConflict
	}
	if v.Stage == "unknown" {
		if !nativeInitialStageCode(v.Code) {
			return ErrConflict
		}
		return nil
	}
	if v.Code != "" {
		return ErrConflict
	}
	if v.Stage == "workspace_created" && (v.Parent == nil || v.Parent.Device == 0 || v.Parent.Inode == 0) || v.Stage == "copy_ready" && (v.Directory == nil || v.Parent == nil || v.Directory.Device == 0 || v.Directory.Inode == 0 || v.Directory.Device != v.Parent.Device) || v.Stage == "source_ready" && !data.ValidCatalogHash(v.SourceSHA256) || v.Stage == "helper_started" && (!data.ValidCatalogHash(v.ContainerID) || v.PID <= 1 || v.StartTicks == 0 || len(v.Cgroup) == 0 || len(v.Cgroup) > 4096) || v.Stage == "observation" && (v.ChildPID <= 1 || v.ChildStartTicks == 0) {
		return ErrConflict
	}
	return nil
}

func nativeInitialStageIdentity(a, b NativeInitialStageObservation) bool {
	if a.Parent != nil && (b.Parent == nil || *a.Parent != *b.Parent) || a.Directory != nil && (b.Directory == nil || *a.Directory != *b.Directory) || a.SourceSHA256 != "" && a.SourceSHA256 != b.SourceSHA256 || a.ContainerID != "" && a.ContainerID != b.ContainerID || a.PID != 0 && (a.PID != b.PID || a.StartTicks != b.StartTicks || a.Cgroup != b.Cgroup) || a.ChildPID != 0 && (a.ChildPID != b.ChildPID || a.ChildStartTicks != b.ChildStartTicks) {
		return false
	}
	return (b.Parent == nil || b.Parent.Device != 0 && b.Parent.Inode != 0) && (b.Directory == nil || b.Directory.Device != 0 && b.Directory.Inode != 0) && (b.ContainerID == "" || data.ValidCatalogHash(b.ContainerID)) && (b.SourceSHA256 == "" || data.ValidCatalogHash(b.SourceSHA256)) && ((b.PID == 0 && b.StartTicks == 0 && b.Cgroup == "") || b.PID > 1 && b.StartTicks > 0 && len(b.Cgroup) <= 4096) && ((b.ChildPID == 0 && b.ChildStartTicks == 0) || b.ChildPID > 1 && b.ChildStartTicks > 0)
}

func nativeInitialStageTransition(from, to string) bool {
	if from == "disposed" {
		return false
	}
	if to == "unknown" {
		return from != "unknown"
	}
	if to == "helper_disposed" {
		return from != "helper_disposed" && from != "retirement_intent"
	}
	if to == "stop_intent" {
		return from == "launch_intent" || from == "helper_started" || from == "input_issued" || from == "observation" || from == "unknown"
	}
	if to == "retirement_intent" {
		return from == "helper_disposed" || from == "observation"
	}
	if to == "disposed" {
		return from == "retirement_intent"
	}
	next := map[string]string{"intent": "workspace_created", "workspace_created": "copy_ready", "copy_ready": "source_ready", "source_ready": "image_inspect", "image_inspect": "image_ready", "image_ready": "material_ready", "material_ready": "lease_ready", "lease_ready": "launch_intent", "launch_intent": "helper_started", "helper_started": "input_issued", "helper_disposed": "observation"}
	return next[from] == to
}

func nativeInitialStageCode(code string) bool {
	switch code {
	case "workspace_create", "source_copy", "source_empty", "image_inspect", "material_read", "lease_check", "container_start", "helper_bind", "input_attach", "helper_result", "helper_stop", "helper_disposal", "source_changed", "workspace_disposal":
		return true
	case "container_start_failed", "container_start_unknown", "container_start_output":
		return true
	default:
		return datapublish.GuestStageFailureCode(code)
	}
}

func (i *NativeBackupInspection) InitialStages(ctx context.Context) ([]NativeInitialStageJournal, error) {
	if i == nil || i.state == nil {
		return nil, ErrConflict
	}
	tx, err := i.state.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := i.check(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT nonce FROM data_native_initial_stage_intents WHERE operation_id=? ORDER BY nonce", i.original.ID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
		if len(ids) > 128 {
			err = ErrConflict
			break
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	result := make([]NativeInitialStageJournal, 0, len(ids))
	for _, id := range ids {
		j, _, err := readNativeInitialStage(ctx, tx, id)
		if err != nil || j.Intent.OperationID != i.original.ID || j.Intent.BindingID != i.binding {
			return nil, ErrConflict
		}
		result = append(result, j)
	}
	return result, tx.Commit()
}

func (i *NativeBackupInspection) RequireInitialStagesSettled(ctx context.Context) error {
	stages, err := i.InitialStages(ctx)
	if err != nil {
		return err
	}
	for _, stage := range stages {
		if !stage.IsDisposed() {
			return ErrConflict
		}
	}
	return nil
}
