//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestBackupCredentialsJournalAndSecureStartupReader(t *testing.T) {
	ctx := context.Background()
	state, d, f := persistentHostFixture(t)
	raw, err := os.ReadFile("../policy/testdata/operator.toml") //nolint:gosec // Path is a private test fixture or verified private probe file.
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte(`
[[backup_destinations]]
reference="primary"
endpoint="https://storage.example"
region="region-1"
bucket="backups"
base_prefix="brine"
credential_ref="primary"
`)...)
	pol, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	d.PolicyHash = pol.Hash()
	fact := collectPersistent(t, f, d)
	snapshotRaw, err := os.ReadFile("../target/testdata/ready-arm64.json") //nolint:gosec // Path is a private test fixture or verified private probe file.
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(snapshotRaw)
	if err != nil {
		t.Fatal(err)
	}
	requester := "deploy:" + strings.Repeat("c", 64)
	root := t.TempDir()
	if err = os.Chmod(root, 0700); err != nil { //nolint:gosec // Private directory requires execute permission for traversal.
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "credentials"), 0700); err != nil {
		t.Fatal(err)
	}
	service := backupCredentialService(Service{Store: state, Requester: requester, Policy: &fakePolicy{p: pol}, Inventory: &fakeInventory{snapshot: snapshot, store: state}}, root, "")
	p, err := service.Plan(ctx, "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := service.Journal.LoadPlan(ctx, p.ID)
	if err != nil || reloaded.ID != p.ID || reloaded.Requester != requester {
		t.Fatal("journal plan", err)
	}
	packet, err := backupcredentials.DecodePacket([]byte(`{"access_key_id":"fixture-access","secret_access_key":"fixture-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Clear()
	receipt, err := service.Deliver(ctx, p, packet)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := state.CredentialReceipt(ctx, fact.Database.ReplicaBindingID, receipt.Version)
	if err != nil || stored.Requester != requester || stored.PolicyHash != pol.Hash() {
		t.Fatal("receipt scope", err)
	}
	file, err := service.Files.Path("primary", receipt.Version)
	if err != nil {
		t.Fatal(err)
	}
	reader := launchPermits{State: state, Files: service.Files}
	env, err := reader.ReadCredentialEnvironment(ctx, file)
	if err != nil || len(env) != 2 {
		t.Fatal("secure read", err)
	}
	if err = os.Chmod(file, 0644); err != nil { //nolint:gosec // Intentional unsafe-permission fixture proves fail-closed refusal.
		t.Fatal(err)
	}
	if _, err = reader.ReadCredentialEnvironment(ctx, file); err == nil {
		t.Fatal("foreign credential mode admitted")
	}

	if err = os.Chmod(file, 0600); err != nil { //nolint:gosec // Private directory requires execute permission for traversal.
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Hour)
	service.Now = func() time.Time { return past.Add(-time.Hour) }
	expiredPlan, err := service.Plan(ctx, "hello", &past)
	if err != nil {
		t.Fatal(err)
	}
	expiredPacket, err := backupcredentials.DecodePacket([]byte(`{"access_key_id":"fixture-access","secret_access_key":"fixture-secret","expires_at":"` + past.Format(time.RFC3339Nano) + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer expiredPacket.Clear()
	expiredReceipt, err := service.Deliver(ctx, expiredPlan, expiredPacket)
	if err != nil {
		t.Fatal(err)
	}
	expiredPath, err := service.Files.Path("primary", expiredReceipt.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reader.ReadCredentialEnvironment(ctx, expiredPath); err == nil {
		t.Fatal("expired credential accepted for replica startup")
	}

	if _, err = state.HoldDataFence(ctx, fact.Database.DatabaseID, "fenced-credential-delivery"); err != nil {
		t.Fatal(err)
	}
	service.Now = nil
	fencedPlan, err := service.Plan(ctx, "hello", nil)
	if err != nil {
		t.Fatal("fence blocked credential storage plan", err)
	}
	fencedReceipt, err := service.Deliver(ctx, fencedPlan, packet)
	if err != nil || fencedReceipt.Activated {
		t.Fatal("fenced credential storage failed or activated replica", err)
	}
	second := d.Databases[0]
	second.Name, second.MountPath = "audit", "/audit"
	audit, err := state.ReserveDatabase(ctx, store.DataReservation{App: "hello", PolicyHash: pol.Hash(), Database: second, Destination: d.BackupDestinations[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Plan(ctx, "hello", nil); err == nil {
		t.Fatal("multi-database app accepted missing selector")
	}
	operations := credentialOperations{service: Service{Store: state, Requester: requester, Policy: &fakePolicy{p: pol}, Inventory: &fakeInventory{snapshot: snapshot, store: state}}, stateRoot: root, jobs: jobs.Service{Store: state, Requester: requester, Launcher: credentialTestLauncher{}}}
	auditPlan, err := operations.Plan(ctx, "hello", "audit", nil)
	if err != nil || auditPlan.Scope.Binding != string(audit.Replica.BindingID) {
		t.Fatal("wrong database planned", err)
	}
	if _, err = operations.Set(ctx, "hello", "main", auditPlan.ID, packet); err == nil {
		t.Fatal("plan crossed database selector")
	}
	if _, err = operations.Set(ctx, "hello", "", auditPlan.ID, packet); err == nil {
		t.Fatal("multi-database delivery omitted selector")
	}
	auditReceipt, err := operations.Set(ctx, "hello", "audit", auditPlan.ID, packet)
	if err != nil || auditReceipt.Status != "accepted" {
		t.Fatal("selected database delivery failed", err)
	}
	selectedReceipt, err := (backupcredentials.Files{Root: filepath.Join(root, "credentials")}).Receipt(auditPlan.Scope.CredentialRef, auditPlan.Version)
	if err != nil || selectedReceipt.Scope.Binding != string(audit.Replica.BindingID) {
		t.Fatal("selected binding lost in stored delivery", err)
	}
	retry, err := operations.Set(ctx, "hello", "audit", auditPlan.ID, packet)
	if err != nil || retry.Status != "accepted" {
		t.Fatal("same-plan detached resume refused", err)
	}
	nextVersion, err := (backupcredentials.Files{Root: filepath.Join(root, "credentials")}).Next(auditPlan.Scope.CredentialRef)
	if err != nil || nextVersion != auditPlan.Version+1 {
		t.Fatal("same-plan resume installed another version", err)
	}

	// The journal refuses a client-chosen requester even when its plan hash is valid.
	forged := p
	forged.Requester = "deploy:" + strings.Repeat("d", 64)
	if err = service.Journal.RecordPlan(ctx, forged); err == nil {
		t.Fatal("client requester journaled")
	}
}

type credentialTestLauncher struct{}

func (credentialTestLauncher) Launch(context.Context, systemd.OperationID) error { return nil }

func TestCredentialTaskAdmissionNeverActivatesSynchronously(t *testing.T) {
	ctx := context.Background()
	state, _, _ := persistentHostFixture(t)
	launcher := credentialTestLauncher{}
	service := jobs.Service{Store: state, Requester: "fixture", Launcher: launcher}
	accepted, err := service.Submit(ctx, ops.Intent{Kind: ops.CredentialActivation, App: "hello", SecretRef: "sha256:" + strings.Repeat("e", 64)}, "activation1")
	if err != nil || accepted.Status != "accepted" {
		t.Fatal(err)
	}
	operation, err := state.GetOperation(ctx, accepted.OperationID)
	if err != nil || operation.State != ops.Queued || operation.SecretRef != "sha256:"+strings.Repeat("e", 64) {
		t.Fatal("activation ran inside admission or reference lost", err)
	}
	outcome, err := state.ReadTaskOutcome(ctx, accepted.OperationID)
	if err != nil || outcome != nil {
		t.Fatal("admission manufactured a terminal receipt", err)
	}
}
