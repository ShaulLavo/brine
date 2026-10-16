//go:build linux

package host

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type nativeStopAttempt struct {
	permit    store.EnginePermit
	native    *engineNativeHandle
	container engineContainer
	unit      systemd.Unit
	issuedAt  time.Time
	completed bool
}

func bindNativeStopAttempt(ctx context.Context, session localexec.Session, stateRoot, home string, permit store.EnginePermit) (*nativeStopAttempt, error) {
	if permit.Runtime.State != data.EngineInitialized || permit.Allocation == nil || verifyEngineService(ctx, home, permit) != nil {
		return nil, errEngineSource
	}
	unit, err := systemd.ParseUnit(engineContainerName(permit.Runtime.BindingID) + ".service")
	if err != nil || verifyEngineLoadedService(ctx, session, home, unit) != nil {
		return nil, errEngineSource
	}
	containers, err := listEngineContainers(ctx, session)
	if err != nil {
		return nil, errEngineSource
	}
	var selected *engineContainer
	for i := range containers {
		if containers[i].Name != engineContainerName(permit.Runtime.BindingID) {
			continue
		}
		if selected != nil {
			return nil, errEngineSource
		}
		selected = &containers[i]
	}
	if selected == nil {
		return nil, errEngineSource
	}
	native, err := bindEngineNative(ctx, session, *selected, permit, engineMaterialPaths(stateRoot, permit.Runtime))
	if err != nil {
		return nil, err
	}
	return &nativeStopAttempt{permit: permit, native: native, container: *selected, unit: unit}, nil
}

// The durable fence and preparation intent precede this call. The caller must
// release its launch guard so the manager's ExecStopPost can acquire it. After
// stop returns, reacquire the actual launch/lifetime locks and reread the intent.
func (a *nativeStopAttempt) stop(ctx context.Context, session localexec.Session, home string) error {
	if a == nil || a.completed || !a.issuedAt.IsZero() || a.native == nil || a.native.alive() != nil || verifyEngineLoadedService(ctx, session, home, a.unit) != nil {
		return errEngineSource
	}
	a.issuedAt = time.Now().UTC()
	stop, cancel := context.WithTimeout(ctx, time.Duration(a.permit.Runtime.StopSeconds+15)*time.Second)
	defer cancel()
	if systemd.New(session).Stop(stop, a.unit) != nil || !a.native.exited() {
		return errEngineSource
	}
	a.completed = true
	return nil
}

type nativeStoppedState struct {
	ID    string `json:"Id"`
	State struct {
		Running       *bool      `json:"Running"`
		PID           *int       `json:"Pid"`
		ExitCode      *int       `json:"ExitCode"`
		OOMKilled     *bool      `json:"OOMKilled"`
		FinishedAt    *time.Time `json:"FinishedAt"`
		StoppedByUser *bool      `json:"StoppedByUser"`
		Error         *string    `json:"Error"`
	} `json:"State"`
}

func decodeNativeStoppedState(raw []byte, id string, issued, observed time.Time) (time.Time, error) {
	row, err := decodeNativeQuiescentState(raw, id, observed)
	if err != nil || row.State.StoppedByUser == nil || !*row.State.StoppedByUser || row.State.FinishedAt.Before(issued) {
		return time.Time{}, errEngineSource
	}
	return row.State.FinishedAt.UTC(), nil
}

func decodeNativeQuiescentState(raw []byte, id string, observed time.Time) (nativeStoppedState, error) {
	var rows []nativeStoppedState
	if len(raw) == 0 || len(raw) > 256<<10 || json.Unmarshal(raw, &rows) != nil || len(rows) != 1 || rows[0].ID != id {
		return nativeStoppedState{}, errEngineSource
	}
	s := rows[0].State
	if s.Running == nil || *s.Running || s.PID == nil || *s.PID != 0 || s.ExitCode == nil || *s.ExitCode != 0 || s.OOMKilled == nil || *s.OOMKilled || s.Error == nil || *s.Error != "" || s.FinishedAt == nil || s.FinishedAt.IsZero() || s.FinishedAt.After(observed) {
		return nativeStoppedState{}, errEngineSource
	}
	return rows[0], nil
}

func (a *nativeStopAttempt) observation(ctx context.Context, session localexec.Session) (store.NativeStopObservation, error) {
	if a == nil || !a.completed || a.native == nil || !a.native.exited() || a.issuedAt.IsZero() {
		return store.NativeStopObservation{}, errEngineSource
	}
	stopped, err := (archiveHost{session: session}).serviceStopped(ctx, a.unit.String(), false)
	properties, propertyErr := systemd.New(session).Show(ctx, a.unit)
	if err != nil || !stopped || propertyErr != nil || properties.ActiveState != "inactive" || properties.SubState != "dead" {
		return store.NativeStopObservation{}, errEngineSource
	}
	containers, err := listEngineContainers(ctx, session)
	if err != nil || !engineBindingAbsent(containers, a.permit) {
		return store.NativeStopObservation{}, errEngineSource
	}
	found := false
	for _, container := range containers {
		if container.ID != a.container.ID {
			continue
		}
		if found || container.Name != a.container.Name || container.Image != a.container.Image || container.Path != a.container.Path || container.Config.Labels["BRINE_ENGINE_BINDING"] != string(a.permit.Runtime.BindingID) {
			return store.NativeStopObservation{}, errEngineSource
		}
		found = true
	}
	if !found {
		return store.NativeStopObservation{}, errEngineSource
	}
	result, err := session.Execute(ctx, "/usr/bin/podman", []string{"container", "inspect", a.container.ID}, nil, false)
	if err != nil {
		return store.NativeStopObservation{}, errEngineSource
	}
	finished, err := decodeNativeStoppedState([]byte(result.Stdout), a.container.ID, a.issuedAt, time.Now().UTC())
	if err != nil {
		return store.NativeStopObservation{}, err
	}
	return store.NativeStopObservation{Mode: "clean_exit", ContainerID: a.container.ID, FinishedAt: finished, SourceAbsent: true, RestartJobsAbsent: true}, nil
}

func (a *nativeStopAttempt) close() {
	if a != nil && a.native != nil {
		a.native.close()
		a.native = nil
	}
}

func decodeNativeStoppedReadback(raw []byte, id string, finished, observed time.Time) (time.Time, error) {
	if finished.IsZero() {
		return time.Time{}, errEngineSource
	}
	row, err := decodeNativeQuiescentState(raw, id, observed)
	if err != nil || !row.State.FinishedAt.Equal(finished) {
		return time.Time{}, errEngineSource
	}
	return row.State.FinishedAt.UTC(), nil
}
