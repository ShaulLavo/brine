//go:build linux

package host

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestNativeBackupsAcrossOperatorPolicyEdits(t *testing.T) {
	for _, edit := range []string{"unchanged", "allow_live_restore", "destination", "remove_root", "add_root"} {
		t.Run(edit, func(t *testing.T) { checkNativeBackupPolicyEdit(t, edit) })
	}
}

func checkNativeBackupPolicyEdit(t *testing.T, edit string) {
	t.Helper()
	f := freshNativeMaterialFixture(t)
	ctx := t.Context()
	id, hash := strings.Repeat("1", 32), strings.Repeat("2", 64)
	backup := store.NativeBackupPolicy{Format: "brine-native-backup-policy-v1", BindingID: f.plan.Managed.Database.BindingID, EpochID: f.plan.Managed.Archive.EpochID, Revision: 1, OperationID: "backup-activation", RevisionPlanID: id, DeploymentPlanID: f.plan.Hash, DesiredSHA256: strings.TrimPrefix(f.plan.DesiredHash, "sha256:"), PolicySHA256: strings.TrimPrefix(f.desired.PolicyHash, "sha256:"), RepositoryID: hash, CredentialReference: f.desired.BackupDestinations[0].CredentialRef, CredentialVersion: 1, CredentialReceiptID: id, RecoveryReference: string(f.desired.SpacetimeDB.RecoveryPasswordRef), RecoveryVersion: 1, RecoveryDeliveryID: id, RecoverySHA256: hash, Backup: *f.desired.SpacetimePolicy, UnitSHA256: hash, TimerSHA256: hash, ActivatedAt: time.Now().UTC()}
	db, err := sql.Open("sqlite", filepath.Join(f.stateRoot, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	raw, err := json.Marshal(backup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO data_native_backup_policies VALUES(?,?,?,?)", backup.BindingID, backup.Revision, backup.EpochID, raw); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(string(raw), "/srv/brine-data", string(f.desired.SpacetimeDB.PersistentRoot), 1) + "\n[[allowed_registries]]\nhost=\"ghcr.io\"\nrepository_prefixes=[\"shaullavo\"]\n"
	switch edit {
	case "allow_live_restore":
		source = "allow_live_restore = true\n" + source
	case "destination":
		source = strings.Replace(source, "https://storage.example.com", "https://elsewhere.example.com", 1)
	case "remove_root":
		source = strings.Replace(source, string(f.desired.SpacetimeDB.PersistentRoot), "/srv/revoked-root", 1)
	case "add_root":
		source = strings.Replace(source, "persistent_roots = [", "persistent_roots = [\"/srv/other-root\", ", 1)
	}
	current, err := policy.Parse([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if (edit == "unchanged") != (current.Hash() == f.desired.PolicyHash) {
		t.Fatal("fixture did not make the expected operator policy edit")
	}
	f.service.Policy = &fakePolicy{p: current}
	launcher := &fakeLaunch{}
	operations := nativeBackupOperations{service: f.service, jobs: jobs.Service{Store: f.service.Store, Requester: f.service.Requester, Launcher: launcher}}
	for _, kind := range []ops.Kind{ops.NativePrepare, ops.NativeCapture} {
		key := strings.Repeat("3", 32)
		if kind == ops.NativeCapture {
			key = strings.Repeat("4", 32)
		}
		var accepted jobs.Accepted
		if kind == ops.NativePrepare {
			accepted, err = operations.Prepare(ctx, dispatch.NativePrepareArgs{App: f.plan.App, Database: string(f.desired.SpacetimeDB.Name), IdempotencyKey: key})
		} else {
			accepted, err = operations.Capture(ctx, dispatch.NativeCaptureArgs{App: f.plan.App, Database: string(f.desired.SpacetimeDB.Name), IdempotencyKey: key})
		}
		if edit != "unchanged" {
			if err == nil || result.Classify(err).Code() != result.PolicyRefused || len(launcher.ids) != 0 {
				t.Fatalf("%s admitted %s before reactivation: %v", kind, edit, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s refused after %s edit: %v", kind, edit, err)
			continue
		}
		input, err := f.service.Store.ReadNativeTaskInput(ctx, accepted.OperationID)
		if err != nil || input.Kind != kind || input.BindingID != backup.BindingID {
			t.Fatalf("accepted task lost its original binding: %v", err)
		}
	}
	retained, err := f.service.Store.ReadNativeBackupPolicy(ctx, backup.BindingID)
	if err != nil || retained != backup {
		t.Fatal("operator edit rewrote the active backup policy", err)
	}
}
