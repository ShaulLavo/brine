//go:build linux

package host

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/sqlitestatus"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type sqliteBackupStatusReader struct {
	stateDir                  string
	policy                    PolicyLoader
	executor                  localexec.Executor
	cli                       restore.CLI
	probe                     func(context.Context, restore.Destination, restore.Credentials) restore.RemoteProbe
	listingBudget, timeBudget time.Duration
}

func newSQLiteBackupStatusReader(dir string) dispatch.SQLiteBackupStatusReader {
	return &sqliteBackupStatusReader{stateDir: dir, policy: DiskPolicy{}, executor: localexec.ExecRunner{}, probe: restore.ProbeRemote, listingBudget: 15 * time.Second, timeBudget: 30 * time.Second}
}
func (r *sqliteBackupStatusReader) SQLiteBackupStatus(ctx context.Context, a dispatch.SQLiteBackupStatusArgs) (sqlitestatus.Status, error) {
	if ctx.Err() != nil {
		return sqlitestatus.Status{}, ctx.Err()
	}
	if !a.Valid() {
		return sqlitestatus.Status{}, result.New(result.InvalidUsage, nil)
	}
	state, err := store.OpenReadOnly(ctx, r.stateDir)
	if err != nil {
		if ctx.Err() != nil {
			return sqlitestatus.Status{}, ctx.Err()
		}
		return sqlitestatus.Status{}, result.New(result.PolicyRefused, nil)
	}
	defer func() { _ = state.Close() }()
	facts, err := state.ReadSQLiteStatus(ctx, a.App, a.Database)
	if err != nil {
		if ctx.Err() != nil {
			return sqlitestatus.Status{}, ctx.Err()
		}
		return sqlitestatus.Status{}, result.New(result.PolicyRefused, nil)
	}
	r.observeLifecycle(ctx, &facts)
	now := time.Now().UTC()
	credentials, credentialState := r.credentials(facts, now)
	facts.Facts.Credential.State = credentialState
	observations := sqlitestatus.Observations{Service: r.service(ctx, string(facts.Replica.BindingID)), Remote: restore.RemoteProbe{State: restore.RemoteNotChecked, ObservedAt: now}, Watermark: sqlitestatus.Watermark{State: "not_checked", ObservedAt: now}}
	if credentialState == "available" {
		binding := sqliteStatusBinding(facts)
		child, cancel := context.WithTimeout(ctx, r.listingBudget)
		observations.Remote = r.probe(child, binding.Destination, credentials)
		cancel()
		if ctx.Err() == nil {
			observations.Watermark = r.watermark(ctx, binding, credentials)
		}
	}
	if ctx.Err() != nil {
		return sqlitestatus.Status{}, ctx.Err()
	}
	status := sqlitestatus.Compose(time.Now().UTC(), facts.Facts, observations)
	if !status.Valid() {
		return sqlitestatus.Status{}, result.New(result.PolicyRefused, nil)
	}
	return status, nil
}
func sqliteStatusBinding(f store.SQLiteStatusFacts) restore.Binding {
	b, d := f.Replica, f.Replica.Destination
	return restore.Binding{ID: string(b.BindingID), Epoch: string(b.EpochID), CredentialRef: d.CredentialRef, Destination: restore.Destination{Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, Prefix: strings.TrimSuffix(b.RemotePrefix, "/"), PathStyle: d.PathStyle}}
}
func (r *sqliteBackupStatusReader) observeLifecycle(ctx context.Context, f *store.SQLiteStatusFacts) {
	pol, err := r.policy.Load(ctx)
	if err != nil {
		return
	}
	evidence, ok := pol.BackupRetention(f.Replica.Destination.Reference)
	if !ok || !evidence.Valid() || evidence.Destination != f.Replica.Destination.Reference || evidence.Endpoint != f.Replica.Destination.Endpoint || evidence.Bucket != f.Replica.Destination.Bucket || evidence.BasePrefix != f.Replica.Destination.BasePrefix {
		return
	}
	at, err := time.Parse(time.RFC3339Nano, evidence.VerifiedAt)
	if err != nil {
		return
	}
	until := at.Add(time.Duration(evidence.FreshnessSeconds) * time.Second) // #nosec G115 -- Valid caps freshness to 30 days.
	lifecycle := sqlitestatus.Lifecycle{State: "unsafe", VerifiedAt: &at, FreshUntil: &until}
	if evidence.Admits(f.Replica.Destination, time.Now().UTC()) {
		lifecycle.State = "fresh"
	}
	if evidence.NoObjectExpiration && time.Now().UTC().After(until) {
		lifecycle.State = "stale"
	}
	f.Facts.Lifecycle = lifecycle
}
func (r *sqliteBackupStatusReader) credentials(f store.SQLiteStatusFacts, now time.Time) (restore.Credentials, string) {
	record := f.Credential
	if record == nil {
		return restore.Credentials{}, "missing"
	}
	c := restore.Credentials{ReceivedAt: record.ReceivedAt}
	if record.ExpiresAt != nil {
		c.ExpiresAt = *record.ExpiresAt
		if !now.Before(c.ExpiresAt) {
			return c, "expired"
		}
	}
	// Files.Read never creates absent directories. Files.Receipt does, so the
	// scoped delivery receipt above comes only from the protected control store.
	packet, err := (backupcredentials.Files{Root: filepath.Join(r.stateDir, "credentials")}).Read(record.CredentialRef, record.Version)
	if err != nil {
		return c, "unreadable"
	}
	defer packet.Clear()
	for _, entry := range packet.Environment() {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "AWS_ACCESS_KEY_ID":
			c.AccessKey = value
		case "AWS_SECRET_ACCESS_KEY":
			c.SecretKey = value
		case "AWS_SESSION_TOKEN":
			c.SessionToken = value
		}
	}
	return c, "available"
}
func (r *sqliteBackupStatusReader) service(ctx context.Context, binding string) sqlitestatus.Service {
	out := sqlitestatus.Service{State: "unknown", ObservedAt: time.Now().UTC()}
	name, err := replication.ServiceName(binding)
	if err != nil {
		return out
	}
	unit, err := systemd.ParseUnit(name)
	if err != nil {
		return out
	}
	session, err := localexec.NewSession(r.executor, uint32(os.Geteuid()), r.stateDir, 5*time.Second) // #nosec G115 -- OS uid fits uint32.
	if err != nil {
		return out
	}
	properties, err := systemd.New(session).Show(ctx, unit)
	out.ObservedAt = time.Now().UTC()
	if err != nil {
		return out
	}
	switch properties.ActiveState {
	case "active":
		out.State = "active"
	case "inactive":
		out.State = "inactive"
	case "failed":
		out.State = "failed"
	}
	return out
}
func (r *sqliteBackupStatusReader) watermark(ctx context.Context, b restore.Binding, c restore.Credentials) sqlitestatus.Watermark {
	out := sqlitestatus.Watermark{State: "failed", ObservedAt: time.Now().UTC()}
	child, cancel := context.WithTimeout(ctx, r.timeBudget)
	defer cancel()
	txid, err := r.readWatermark(child, b, c)
	out.ObservedAt = time.Now().UTC()
	if child.Err() != nil {
		out.State = "timed_out"
		return out
	}
	if err == nil {
		out.State = "known"
		out.TXID = &txid
	}
	return out
}
func (r *sqliteBackupStatusReader) readWatermark(ctx context.Context, b restore.Binding, c restore.Credentials) (txid uint64, err error) {
	// Every call owns a different parent. Remove only this exact, empty directory,
	// never another caller's workspace or a substituted parent.
	root, err := os.OpenRoot(r.stateDir)
	if err != nil {
		return 0, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
		if err != nil {
			txid = 0
		}
	}()
	name := "sqlite-status-" + rand.Text()
	if err := root.Mkdir(name, 0700); err != nil {
		return 0, err
	}
	info, err := root.Lstat(name)
	defer func() {
		err = errors.Join(err, removeSQLiteStatusParent(root, name, info))
		if err != nil {
			txid = 0
		}
	}()
	if err != nil {
		return 0, err
	}
	return restore.RemoteWatermark(ctx, filepath.Join(r.stateDir, name), r.cli, b, c)
}
func removeSQLiteStatusParent(root *os.Root, name string, created os.FileInfo) error {
	current, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if created == nil || !current.IsDir() || !os.SameFile(created, current) {
		return store.ErrInvalid
	}
	return root.Remove(name)
}
