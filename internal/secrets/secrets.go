// Package secrets creates unbound immutable versions, never deployment bindings.
package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
)

const ValueLimit = 32 << 10
const JournalTimeout = 5 * time.Second

type Store interface {
	AcquireHostLock(context.Context) (ops.Lock, error)
	CreateOperation(context.Context, ops.Intent, string, string) (ops.Operation, bool, error)
	SetOperationState(context.Context, string, ops.State) error
	AppendEvent(context.Context, string, ops.Event) (uint64, error)
	EventsAfter(context.Context, string, uint64, int) ([]ops.Event, error)
}
type Runtime interface {
	SecretNames(context.Context) ([]podman.Name, error)
	CreateSecret(context.Context, podman.Name, []byte) error
	SecretExists(context.Context, podman.Name) (bool, error)
}
type Service struct {
	Store      Store
	Podman     Runtime
	LoadPolicy func(context.Context) (policy.Policy, error)
	Requester  string
}
type Stored struct {
	OperationID string `json:"operation_id"`
	VersionName string `json:"version_name"`
	Bound       bool   `json:"bound"`
}

func (s Service) Set(ctx context.Context, app, ref, key string, value []byte) (Stored, error) {
	intent := ops.Intent{Kind: ops.SecretSet, App: app, SecretRef: ref}
	if !ops.ValidIntent(intent) || len(value) == 0 || len(value) > ValueLimit || len(key) == 0 || len(key) > 128 {
		return Stored{}, result.New(result.InvalidUsage, nil)
	}
	if s.Store == nil || s.Podman == nil || s.LoadPolicy == nil || s.Requester == "" {
		return Stored{}, result.New(result.DependencyMissing, nil)
	}
	lock, err := s.Store.AcquireHostLock(ctx)
	if err != nil {
		return Stored{}, err
	}
	defer lock.Release()
	pol, err := s.LoadPolicy(ctx)
	if err != nil {
		return Stored{}, err
	}
	if err = pol.CheckSecret(spec.Name(app), spec.SecretReference(ref)); err != nil {
		return Stored{}, err
	}
	op, existing, err := s.Store.CreateOperation(ctx, intent, s.Requester, key)
	if err != nil {
		return Stored{}, err
	}
	if existing {
		events, e := s.Store.EventsAfter(ctx, op.ID, 0, 128)
		if e != nil {
			return Stored{}, e
		}
		name := ""
		for _, event := range events {
			if event.Kind == "secret_version" {
				var payload ops.SecretVersionPayload
				if json.Unmarshal(event.Payload, &payload) != nil {
					return Stored{}, result.New(result.RecoveryRequired, nil)
				}
				name = payload.Name
			}
		}
		if op.State == ops.Succeeded && name != "" {
			return Stored{OperationID: op.ID, VersionName: name}, nil
		}
		if op.State == ops.Preparing && name != "" {
			return s.reconcile(ctx, op.ID, name)
		}
		// A queued or preparing record without an intent has no proven assignment.
		// Never reinterpret a replay's input as permission to execute it again.
		return Stored{}, result.New(result.RecoveryRequired, nil)
	}
	journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer cancel()
	if err = s.Store.SetOperationState(journal, op.ID, ops.Preparing); err != nil {
		return Stored{}, err
	}
	names, err := s.Podman.SecretNames(ctx)
	if err != nil {
		return Stored{}, s.finish(ctx, op.ID, ops.Failed)
	}
	prefix := "brine-" + app + "-" + ref + "-v"
	var latest uint64
	for _, name := range names {
		suffix, ok := strings.CutPrefix(name.String(), prefix)
		if !ok {
			continue
		}
		n, e := strconv.ParseUint(suffix, 10, 64)
		if e == nil && n > latest && suffix == strconv.FormatUint(n, 10) {
			latest = n
		}
	}
	if latest == ^uint64(0) {
		return Stored{}, s.finish(ctx, op.ID, ops.Failed)
	}
	version := prefix + strconv.FormatUint(latest+1, 10)
	name, err := podman.ParseName(version)
	if err != nil {
		return Stored{}, s.finish(ctx, op.ID, ops.Failed)
	}
	if err = s.event(journal, op.ID, version, "intent"); err != nil {
		return Stored{}, err
	}
	err = s.Podman.CreateSecret(ctx, name, value)
	if err != nil {
		work, stop := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
		defer stop()
		exists, inspectErr := s.Podman.SecretExists(work, name)
		if inspectErr == nil && exists {
			return s.complete(work, op.ID, version)
		}
		var runtime *localexec.Error
		if inspectErr != nil || errors.As(err, &runtime) && (runtime.Kind == localexec.UnknownOutcome || runtime.Kind == localexec.Timeout) {
			eventErr := s.event(work, op.ID, version, "unknown")
			return Stored{}, errors.Join(eventErr, s.finish(work, op.ID, ops.RecoveryRequired))
		}
		return Stored{}, s.finish(work, op.ID, ops.Failed)
	}
	return s.complete(ctx, op.ID, version)
}
func (s Service) event(ctx context.Context, id, name, outcome string) error {
	b, _ := json.Marshal(ops.SecretVersionPayload{Name: name, Outcome: outcome})
	_, err := s.Store.AppendEvent(ctx, id, ops.Event{Kind: "secret_version", Payload: b})
	return err
}
func (s Service) finish(ctx context.Context, id string, state ops.State) error {
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer cancel()
	return errors.Join(result.New(result.RecoveryRequired, nil), s.Store.SetOperationState(work, id, state))
}
func (s Service) complete(ctx context.Context, id, name string) (Stored, error) {
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer cancel()
	if err := s.event(work, id, name, "completed"); err != nil {
		return Stored{}, err
	}
	if err := s.Store.SetOperationState(work, id, ops.Succeeded); err != nil {
		return Stored{}, err
	}
	return Stored{OperationID: id, VersionName: name}, nil
}
func (s Service) reconcile(ctx context.Context, id, version string) (Stored, error) {
	name, err := podman.ParseName(version)
	if err != nil {
		return Stored{}, result.New(result.RecoveryRequired, nil)
	}
	exists, err := s.Podman.SecretExists(ctx, name)
	if err == nil && exists {
		return s.complete(ctx, id, version)
	}
	return Stored{}, errors.Join(s.event(ctx, id, version, "unknown"), s.finish(ctx, id, ops.RecoveryRequired))
}
