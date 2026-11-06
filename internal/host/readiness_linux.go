//go:build linux

package host

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
	"os"
	"os/user"
	"strconv"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/readiness"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/sqlitestatus"
	"github.com/ShaulLavo/brine/internal/store"
)

func readinessPolicy() readiness.PolicyLoader { return DiskPolicy{} }

func readinessSQLiteBackup(ctx context.Context, dir, app, database string) readiness.Backup {
	out := readiness.Backup{App: app, Database: database, Engine: "sqlite", State: readiness.Unknown[string]("probe_failed"), Stale: readiness.Unknown[bool]("probe_failed")}
	value, err := readinessSQLiteStatus(ctx, dir, dispatch.SQLiteBackupStatusArgs{App: app, Database: database})
	if err != nil {
		return out
	}
	out.State = readiness.Known(value.State)
	out.Stale = sqliteReadinessStale(value)
	return out
}
func readinessSQLiteStatus(ctx context.Context, dir string, args dispatch.SQLiteBackupStatusArgs) (_ sqlitestatus.Status, err error) {
	scratch, err := os.MkdirTemp("", "brine-readiness-")
	if err != nil {
		return sqlitestatus.Status{}, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(scratch)) }()
	reader := newSQLiteBackupStatusReader(dir).(*sqliteBackupStatusReader)
	reader.open = store.OpenSnapshotReadOnly
	reader.workspaceRoot = scratch
	reader.listingBudget, reader.timeBudget = readiness.ProbeTimeout, readiness.ProbeTimeout
	return reader.SQLiteBackupStatus(ctx, args)
}
func sqliteReadinessStale(s sqlitestatus.Status) readiness.Fact[bool] {
	if !s.Committed || s.Credential.RenewalNeeded || len(s.HeldFences) > 0 || s.LastVerified == nil || s.Credential.State == "expired" || s.Credential.State == "missing" || s.Lifecycle.State == "stale" || s.Lifecycle.State == "unsafe" || s.Service.State == "failed" || s.Service.State == "inactive" {
		return readiness.Known(true)
	}
	if s.Remote.State != restore.RemoteReachable && s.Remote.State != restore.RemoteNotChecked {
		return readiness.Known(true)
	}
	if s.Service.State == "unknown" || s.Lifecycle.State == "unknown" || s.Credential.State == "unreadable" || s.Remote.State == restore.RemoteNotChecked || s.Watermark.State != "known" {
		return readiness.Unknown[bool]("observation_unknown")
	}
	return readiness.Known(false)
}

// Readiness bootstrap reads identity and the protected inventory key only. It
// deliberately does not run NewInventory's Podman storage qualification.
func newReadinessInventory(ctx context.Context) (dispatch.Inventory, error) {
	identity, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || identity.Username != "brine" || identity.HomeDir != "/home/brine" || os.Geteuid() == 0 {
		return nil, result.New(result.DispatchOperationRefused, nil)
	}
	key, err := trustedRead(ctx, "/etc/ssh/brine/inventory-key", 32)
	if err != nil || len(key) != 32 {
		return nil, result.New(result.DependencyMissing, nil)
	}
	return &inventory.Collector{FS: inventory.HostFS{}, Runner: localexec.ExecRunner{}, IdentityKey: key}, nil
}
