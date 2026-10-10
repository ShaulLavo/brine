package backupcredentials

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type rotationFake struct {
	record                                                data.CredentialRotation
	binding                                               data.ReplicaBinding
	fenced, stopped, running, locked                      bool
	stops, starts, commits, verifies                      int
	failStage                                             data.RotationStage
	failReload, uncertainStart, uncertainStop, failRemote bool
}

func (f *rotationFake) ReadRotation(context.Context, string) (data.CredentialRotation, error) {
	if f.record.Stage == "" {
		return data.CredentialRotation{}, ErrRotationNotFound
	}
	return f.record, nil
}
func (f *rotationFake) WriteRotation(_ context.Context, previous data.RotationStage, r data.CredentialRotation) error {
	if r.Stage == f.failStage {
		f.failStage = ""
		return errors.New("interruption")
	}
	if f.record.Stage != previous || !r.Follows(previous) {
		return ErrInvalid
	}
	f.record = r
	return nil
}
func (f *rotationFake) Inspect(context.Context, data.ReplicaBindingID) (data.ReplicaBinding, bool, error) {
	return f.binding, f.fenced, nil
}
func (f *rotationFake) Prepare(_ context.Context, r Receipt, current data.ReplicaBinding) (data.ReplicaBinding, error) {
	current.CredentialVersion = r.Version
	current.CredentialFile = "/fixture/v2.env"
	current.UnitSHA256 = strings.Repeat("d", 64)
	return current, nil
}
func (f *rotationFake) Stop(context.Context, data.ReplicaBinding) error {
	f.stops++
	if f.uncertainStop {
		return ErrActivationUnknown
	}
	f.stopped = true
	f.running = false
	return nil
}
func (f *rotationFake) Stopped(context.Context, data.ReplicaBinding) (bool, error) {
	return f.stopped, nil
}
func (f *rotationFake) Acquire(context.Context, data.ReplicaBinding) (func() error, error) {
	if !f.stopped || f.locked {
		return nil, ErrActivationUnknown
	}
	f.locked = true
	return func() error { f.locked = false; return nil }, nil
}
func (f *rotationFake) Commit(_ context.Context, before, after data.ReplicaBinding) error {
	if !f.locked || f.binding != before && f.binding != after {
		return ErrInvalid
	}
	f.commits++
	f.binding = after
	return nil
}
func (f *rotationFake) Reload(context.Context) error {
	if f.failReload {
		f.failReload = false
		return errors.New("interruption")
	}
	return nil
}
func (f *rotationFake) Permit(context.Context, data.ReplicaBinding) error {
	if f.fenced {
		return ErrInvalid
	}
	return nil
}
func (f *rotationFake) Start(context.Context, data.ReplicaBinding) error {
	if f.locked {
		return ErrInvalid
	}
	f.starts++
	if f.uncertainStart {
		return ErrActivationUnknown
	}
	f.running = true
	return nil
}
func (f *rotationFake) Running(context.Context, data.ReplicaBinding) (bool, error) {
	return f.running, nil
}
func (f *rotationFake) VerifyRemote(context.Context, Receipt, data.ReplicaBinding) error {
	f.verifies++
	if f.failRemote {
		return ErrRemoteAccess
	}
	return nil
}
func rotationFixture() (Rotator, *rotationFake, Receipt) {
	p := Plan{Requester: "fixture", Kind: Kind, Scope: fixtureScope(), Version: 2}
	p.ID = p.identity()
	receipt := Receipt{Requester: p.Requester, PlanID: p.ID, Scope: p.Scope, Version: p.Version, File: fileName(p.Scope.CredentialRef, p.Version, "env"), ReceivedAt: time.Now().UTC()}
	//nolint:gosec // Binding holds reference-only fixture paths, never credential material.
	f := &rotationFake{running: true, binding: data.ReplicaBinding{DatabaseID: data.DatabaseID(strings.Repeat("c", 32)), BindingID: data.ReplicaBindingID(p.Scope.Binding), EpochID: data.ReplicaEpochID(p.Scope.Epoch), CredentialVersion: 1, CredentialFile: "/fixture/v1.env", UnitSHA256: strings.Repeat("c", 64), Destination: data.Destination{CredentialRef: p.Scope.CredentialRef, Reference: data.BackupDestinationRef(p.Scope.Destination)}, Committed: true}}
	return Rotator{Journal: f, Host: f}, f, receipt
}
func TestRotationReconcilesCommitBeforeReplicaStart(t *testing.T) {
	for _, stage := range []data.RotationStage{data.RotationCommitted, ""} {
		t.Run(string(stage), func(t *testing.T) {
			rotator, f, receipt := rotationFixture()
			f.failStage = stage
			f.failReload = stage == ""
			if _, err := rotator.Activate(context.Background(), receipt); !errors.Is(err, ErrActivationUnknown) {
				t.Fatal("interruption hidden", err)
			}
			if f.binding.CredentialVersion != 2 || f.starts != 0 || f.stops != 1 {
				t.Fatal("commit/start boundary wrong")
			}
			result, err := rotator.Activate(context.Background(), receipt)
			if err != nil || !result.Activated || result.ActivationStatus != "verified" || f.starts != 1 || f.stops != 1 || f.binding.EpochID != data.ReplicaEpochID(receipt.Scope.Epoch) {
				t.Fatal("rotation not reconciled exactly once", err)
			}
			if _, err := rotator.Activate(context.Background(), receipt); err != nil || f.starts != 1 || f.stops != 1 {
				t.Fatal("verified retry restarted replica", err)
			}
		})
	}
}
func TestRotationNeverBlindlyStartsTwice(t *testing.T) {
	rotator, f, receipt := rotationFixture()
	f.uncertainStart = true
	if _, err := rotator.Activate(context.Background(), receipt); !errors.Is(err, ErrActivationUnknown) {
		t.Fatal(err)
	}
	if _, err := rotator.Activate(context.Background(), receipt); !errors.Is(err, ErrActivationUnknown) || f.starts != 1 {
		t.Fatal("uncertain start repeated", err)
	}
	f.running = true
	if result, err := rotator.Activate(context.Background(), receipt); err != nil || !result.Activated || f.starts != 1 {
		t.Fatal("running activation not reconciled", err)
	}
}
func TestRotationFenceAllowsStorageButNoActivation(t *testing.T) {
	rotator, f, receipt := rotationFixture()
	f.fenced = true
	result, err := rotator.Activate(context.Background(), receipt)
	if err != nil || result.Activated || result.ActivationStatus != "fenced" || f.stops != 0 || f.starts != 0 || f.record.Stage != "" {
		t.Fatal("held fence allowed activation", err)
	}
}

func TestDeliveryResumesSavedCredentialWithoutAnotherVersion(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil { //nolint:gosec // Directory traversal requires private execute permission.
		t.Fatal(err)
	}
	service := Service{Requester: "fixture", Journal: &memoryJournal{plans: map[string]Plan{}}, Files: Files{Root: root}, Scope: func(context.Context, string) (Scope, error) { return fixtureScope(), nil }}
	packet, err := ReadPacket(strings.NewReader(`{"access_key_id":"fixture-access","secret_access_key":"fixture-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Clear()
	plan, err := service.Plan(context.Background(), "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	service.Activate = func(_ context.Context, r Receipt) (Receipt, error) {
		calls++
		if calls == 1 {
			return Receipt{}, ErrActivationUnknown
		}
		r.Activated = true
		r.ActivationStatus = "verified"
		return r, nil
	}
	if _, err := service.Deliver(context.Background(), plan, packet); !errors.Is(err, ErrActivationUnknown) {
		t.Fatal(err)
	}
	original, err := service.Files.Receipt(plan.Scope.CredentialRef, plan.Version)
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.Deliver(context.Background(), plan, packet)
	if err != nil || !got.Activated || got.Version != original.Version || !got.ReceivedAt.Equal(original.ReceivedAt) {
		t.Fatal("delivery not resumed", err)
	}
	next, err := service.Files.Next(plan.Scope.CredentialRef)
	if err != nil || next != 2 {
		t.Fatal("another version installed", err, next)
	}
	different, err := ReadPacket(strings.NewReader(`{"access_key_id":"other-access","secret_access_key":"other-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer different.Clear()
	if _, err := service.Deliver(context.Background(), plan, different); !errors.Is(err, ErrStale) || calls != 2 {
		t.Fatal("different packet resumed activation", err)
	}
	service.Scope = func(context.Context, string) (Scope, error) { return Scope{}, ErrAdmissionRefresh }
	if _, err := service.Deliver(context.Background(), plan, packet); !errors.Is(err, ErrAdmissionRefresh) {
		t.Fatal("refresh guidance collapsed", err)
	}
	if _, err := service.Plan(context.Background(), "hello", nil); !errors.Is(err, ErrAdmissionRefresh) {
		t.Fatal("plan refresh guidance collapsed", err)
	}
}

func TestRotationNeverBlindlyStopsTwice(t *testing.T) {
	rotator, f, receipt := rotationFixture()
	f.uncertainStop = true
	for range 2 {
		if _, err := rotator.Activate(context.Background(), receipt); !errors.Is(err, ErrActivationUnknown) {
			t.Fatal(err)
		}
	}
	if f.stops != 1 || f.starts != 0 {
		t.Fatal("uncertain stop reissued")
	}
	f.stopped = true
	f.running = false
	if result, err := rotator.Activate(context.Background(), receipt); err != nil || !result.Activated || f.stops != 1 || f.starts != 1 {
		t.Fatal("independently stopped state not reconciled", err)
	}
}
func TestRotationRemoteFailureDoesNotRepeatActivation(t *testing.T) {
	rotator, f, receipt := rotationFixture()
	f.failRemote = true
	for range 2 {
		if _, err := rotator.Activate(context.Background(), receipt); !errors.Is(err, ErrRemoteAccess) {
			t.Fatal(err)
		}
	}
	if f.record.Stage != data.RotationActive || f.stops != 1 || f.starts != 1 {
		t.Fatal("remote failure confused activation state")
	}
	f.failRemote = false
	if result, err := rotator.Activate(context.Background(), receipt); err != nil || !result.Activated || f.starts != 1 || f.stops != 1 {
		t.Fatal("remote access not reconciled without restart", err)
	}
}

func TestRotationAlreadyCurrentProvesRunningWithoutRestart(t *testing.T) {
	rotator, f, receipt := rotationFixture()
	f.binding.CredentialVersion = receipt.Version
	f.binding.CredentialFile = "/fixture/v2.env"
	f.binding.UnitSHA256 = strings.Repeat("d", 64)
	result, err := rotator.Activate(context.Background(), receipt)
	if err != nil || !result.Activated || f.stops != 0 || f.starts != 0 || f.record.Stage != "" || f.verifies != 1 {
		t.Fatal("already committed version unnecessarily rotated", err)
	}
}

func TestActivatedReceiptStrictHealthAndStatus(t *testing.T) {
	_, _, receipt := rotationFixture()
	receipt.Activated = true
	receipt.ActivationStatus = "verified"
	health := receipt.Health(receipt.ReceivedAt.Add(time.Minute), time.Minute)
	receipt.CredentialHealth = &health
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReceipt(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"activation_status":"verified"`, `"activation_status":null`, 1),
		strings.Replace(string(raw), `"activated":true`, `"activated":null`, 1),
		strings.Replace(string(raw), `"age_seconds":60`, `"age_seconds":null`, 1),
		strings.Replace(string(raw), `"age_seconds":60`, `"age_seconds":-1`, 1),
		strings.Replace(string(raw), `"health":{`, `"health":{"secret":"PLANTED",`, 1),
		strings.Replace(string(raw), `"activation_status":"verified"`, `"activation_status":"stored"`, 1),
	} {
		if _, err := DecodeReceipt([]byte(bad)); err == nil {
			t.Fatal("invalid activated receipt accepted")
		}
	}
}
