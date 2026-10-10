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
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestBackupCredentialsJournalAndSecureStartupReader(t *testing.T) {
	ctx := context.Background()
	state, d, f := persistentHostFixture(t)
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
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
	snapshotRaw, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(snapshotRaw)
	if err != nil {
		t.Fatal(err)
	}
	requester := "deploy:" + strings.Repeat("c", 64)
	root := t.TempDir()
	if err = os.Mkdir(filepath.Join(root, "credentials"), 0700); err != nil {
		t.Fatal(err)
	}
	service := backupCredentialService(Service{Store: state, Requester: requester, Policy: &fakePolicy{p: pol}, Inventory: &fakeInventory{snapshot: snapshot, store: state}}, root)
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
	if err = os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.ReadCredentialEnvironment(ctx, file); err == nil {
		t.Fatal("foreign credential mode admitted")
	}

	if err = os.Chmod(file, 0600); err != nil {
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
	// The journal refuses a client-chosen requester even when its plan hash is valid.
	forged := p
	forged.Requester = "deploy:" + strings.Repeat("d", 64)
	if err = service.Journal.RecordPlan(ctx, forged); err == nil {
		t.Fatal("client requester journaled")
	}
}
