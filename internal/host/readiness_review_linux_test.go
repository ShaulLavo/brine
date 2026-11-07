package host

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/readiness"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/sqlitestatus"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestReviewSQLiteKnownDegradationCannotBecomeHealthy(t *testing.T) {
	for _, kind := range []string{"held_fence", "renewal_due", "uncommitted"} {
		reviewSQLiteKnownDegradation(t, kind)
	}
}
func reviewSQLiteKnownDegradation(t *testing.T, kind string) {
	t.Helper()
	now := time.Now().UTC()
	until := now.Add(time.Hour)
	binding, epoch := strings.Repeat("b", 32), strings.Repeat("c", 32)
	source := restore.SQLiteSource{Kind: restore.LitestreamLTX, LTX: &restore.LTXSource{Recoverability: true, BindingID: binding, Epoch: epoch, TXID: 7}}
	facts := sqlitestatus.Facts{App: "example", Database: "main", DatabaseID: strings.Repeat("a", 32), BindingID: binding, EpochID: epoch, Committed: true, Credential: sqlitestatus.Credential{State: "available", Version: 1}, Lifecycle: sqlitestatus.Lifecycle{State: "fresh", VerifiedAt: &now, FreshUntil: &until}, LastVerified: &sqlitestatus.Verified{OperationID: "fixture", Source: source, VerifiedAt: now, InvariantCheck: "passed"}}
	if kind == "held_fence" {
		facts.Fences = []sqlitestatus.Fence{{ID: strings.Repeat("d", 32), OwnerOperation: "fixture"}}
	}
	if kind == "uncommitted" {
		facts.Committed = false
	}
	if kind == "renewal_due" {
		expiry := now.Add(time.Minute)
		facts.Credential.ExpiresAt = &expiry
	}
	txid := uint64(7)
	observations := sqlitestatus.Observations{Service: sqlitestatus.Service{State: "active", ObservedAt: now}, Remote: restore.RemoteProbe{State: restore.RemoteReachable, ObservedAt: now}, Watermark: sqlitestatus.Watermark{State: "known", TXID: &txid, ObservedAt: now}}
	value := sqlitestatus.Compose(now, facts, observations)
	if !value.Valid() || value.State != "degraded" {
		t.Fatal("bad status fixture", value)
	}
	stale := sqliteReadinessStale(value)
	t.Logf("kind=%s state=%s reasons=%v stale=%v", kind, value.State, value.Reasons, stale.Value)
	if stale.Value == nil || !*stale.Value {
		t.Error("valid known degraded SQLite status becomes known non-stale backup")
	}
}

type reviewHostFS struct{}

func (reviewHostFS) ReadFile(_ context.Context, path string) ([]byte, error) {
	switch path {
	case "/etc/os-release":
		return []byte("ID=debian\nVERSION_ID=13\n"), nil
	case "/etc/machine-id":
		return []byte(strings.Repeat("1", 32)), nil
	case "/etc/ssh/ssh_host_ed25519_key.pub":
		return []byte(readinessReviewHostKey()), nil
	case "/etc/passwd":
		return []byte("root:x:0:0::/root:/bin/sh\n"), nil
	case "/sys/fs/cgroup/cgroup.controllers":
		return []byte("cpu memory"), nil
	default:
		return nil, fs.ErrNotExist
	}
}

func (reviewHostFS) ReadDir(_ context.Context, path string) ([]fs.DirEntry, error) {
	if path == "/home" || path == "/" {
		return []fs.DirEntry{}, nil
	}
	return nil, fs.ErrNotExist
}
func (reviewHostFS) Readlink(context.Context, string) (string, error) { return "", fs.ErrNotExist }

type reviewCapture struct{}

func (reviewCapture) CaptureStdout(_ context.Context, _ int, path string, args ...string) (localexec.Capture, error) {
	if path == "uname" {
		return localexec.Capture{Stdout: "aarch64"}, nil
	}
	return localexec.Capture{}, fs.ErrNotExist
}
func reviewCollector() *inventory.Collector {
	return &inventory.Collector{FS: reviewHostFS{}, Runner: reviewCapture{}, IdentityKey: []byte("fixture-review-key"), LitestreamHashExecutable: func(context.Context, string) (string, error) { return "", fs.ErrNotExist }}
}
func TestReviewForeignControlStoreMustNotAuthorizeCommittedDomains(t *testing.T) {
	dir, state, snapshot := reviewStatelessStore(t)
	defer func() { _ = state.Close() }()
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	foreign := snapshot.Identity
	collector := reviewCollector()
	actual, err := collector.CollectReadiness(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if actual.Identity == foreign {
		t.Fatal("fixture identity unexpectedly matches")
	}
	r := readinessReader(dir, func(context.Context) (dispatch.Inventory, error) { return collector, nil })
	r.Runtime = nil
	r.Policy = &fakePolicy{p: readinessReviewPolicy(t)}
	r.Network = nil
	before := diagnosticFiles(t, dir)
	report, err := r.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	if _, err := readiness.DecodeReport(raw); err != nil {
		t.Fatal(err)
	}
	t.Logf("stored=%s actual=%s committed_domains=%+v backups=%+v", foreign.ID, actual.Identity.ID, report.Facts.CommittedDomains, report.Backups)
	if !reflect.DeepEqual(before, diagnosticFiles(t, dir)) {
		t.Fatal("fixture live DB changed")
	}
	if report.Facts.CommittedDomains.Value != nil || report.Backups.Value != nil {
		t.Fatal("foreign-target store accepted as actual host committed routes and backups")
	}
}
func TestReviewSQLiteUnknownControls(t *testing.T) {
	s := sqlitestatus.Status{Committed: true, LastVerified: &sqlitestatus.Verified{VerifiedAt: time.Now()}, Credential: sqlitestatus.Credential{State: "available"}, Lifecycle: sqlitestatus.Lifecycle{State: "fresh"}, Service: sqlitestatus.Service{State: "unknown"}, Remote: restore.RemoteProbe{State: restore.RemoteNotChecked}, Watermark: sqlitestatus.Watermark{State: "not_checked"}}
	if got := sqliteReadinessStale(s); got.Status != "unknown" || got.Value != nil {
		t.Fatal(got)
	}
}

func reviewStatelessStore(t *testing.T) (string, *store.Store, target.Snapshot) {
	t.Helper()
	dir := t.TempDir()
	state, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	snapshotRaw, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(snapshotRaw)
	if err != nil {
		t.Fatal(err)
	}
	policyRaw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(bytes.ReplaceAll(policyRaw, []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if err != nil {
		t.Fatal(err)
	}
	appRaw, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(bytes.ReplaceAll(appRaw, []byte("example/hello"), []byte("team/hello")))
	if err != nil {
		t.Fatal(err)
	}
	desired, err := policy.Normalize(app, pol)
	if err != nil {
		t.Fatal(err)
	}
	input := plan.Input{Desired: desired, Snapshot: snapshot, Image: plan.Image{Digest: strings.Split(string(desired.Image), "@")[1], Platform: target.Platform{OS: "linux", Arch: snapshot.Arch}, ManifestDigest: target.Known("sha256:" + strings.Repeat("b", 64))}, State: plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{}}}
	planned, err := plan.Build(input)
	if err != nil || planned.Kind != plan.Create {
		t.Fatal(planned, err)
	}
	id, err := state.SavePlan(t.Context(), planned, desired)
	if err != nil {
		t.Fatal(err)
	}
	release := ops.Release{ID: "review-release", PlanID: id, Image: planned.Image, HostPort: planned.HostPort, Secrets: planned.Secrets, Units: []target.Unit{{Name: "hello.container", Hash: "sha256:" + strings.Repeat("b", 64)}}, CaddyFile: target.CaddyFile{Name: "hello.caddy", Hash: "sha256:" + strings.Repeat("d", 64)}, CaddyGeneration: 2}
	if err := state.CommitRelease(t.Context(), "hello", release); err != nil {
		t.Fatal(err)
	}

	return dir, state, snapshot
}

func TestReviewPreparedDatabaseBesideStatelessReleaseIsNotEmpty(t *testing.T) {
	dir, state, observed := reviewStatelessStore(t)
	defer func() { _ = state.Close() }()
	request := store.DataReservation{App: "hello", PolicyHash: "sha256:" + strings.Repeat("a", 64), Database: data.SQLiteDatabase{Name: "main", PersistentRoot: "/never-open-source", MountPath: "/data", Filename: "app.db", BackupDestination: "primary"}, Destination: data.Destination{Reference: "primary", Endpoint: "https://storage.invalid", Region: "region-1", Bucket: "backups", BasePrefix: "brine", CredentialRef: "primary"}}
	reserved, err := state.ReserveDatabase(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	before := diagnosticFiles(t, dir)
	backups, err := (readinessControl{dir: dir}).Backups(t.Context(), observed.Identity)
	t.Logf("prepared database=%s backups=%v err=%v", reserved.Database.DatabaseID, backups, err)
	if !reflect.DeepEqual(before, diagnosticFiles(t, dir)) {
		t.Fatal("fixture files changed")
	}
	if err == nil && backups != nil && len(backups) == 0 {
		t.Fatal("prepared database disappeared behind unrelated stateless release")
	}
}

func TestReviewWithdrawnManagedRouteIsNotCommittedTLSRoute(t *testing.T) {
	r := newNativeWithdrawalRig(t)
	route, err := r.state.ReadManagedRouteState(t.Context(), r.original.App)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := r.facts(t)
	if err != nil || len(facts.Routing.Files) != 0 {
		t.Fatal("fixture does not prove effective withdrawal", facts.Routing, err)
	}
	var seq int
	var name, file string
	if err := r.db.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(file)
	before := diagnosticFiles(t, dir)
	domains, err := (readinessControl{dir: dir}).Routes(t.Context(), r.snapshot, r.policy.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, diagnosticFiles(t, dir)) {
		t.Fatal("readiness mutated withdrawal fixture")
	}
	t.Logf("release_route_record_present=%t effective_route_files=%d committed_tls_domains=%v", route.Present, len(facts.Routing.Files), domains)
	for _, domain := range r.desired.Domains {
		if slices.Contains(domains, string(domain)) {
			t.Error("protected withdrawn route still selected for TLS")
		}
	}
}

func readinessReviewPolicy(t testing.TB) policy.Policy {
	t.Helper()
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadinessSemanticStoreUsesMeasuredIdentity(t *testing.T) {
	dir, state, observed := reviewStatelessStore(t)
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	observed.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: 2, Files: []target.CaddyFile{{Name: "hello.caddy", Hash: "sha256:" + strings.Repeat("d", 64)}}})
	before := diagnosticFiles(t, dir)
	reader := readinessControl{dir: dir}
	policyHash := readinessReviewPolicy(t).Hash()
	if domains, err := reader.Routes(t.Context(), observed, policyHash); err != nil || len(domains) == 0 {
		t.Fatal("matching identity lost committed route", domains, err)
	}
	if backups, err := reader.Backups(t.Context(), observed.Identity); err != nil || backups == nil || len(backups) != 0 {
		t.Fatal("matching identity lost known empty backup set", backups, err)
	}
	for _, identity := range []target.Identity{{}, {ID: observed.Identity.ID}, {ID: "foreign", HostKeyFingerprint: observed.Identity.HostKeyFingerprint}} {
		assertReadinessIdentityRefused(t, reader, observed, identity, policyHash)
	}
	if !reflect.DeepEqual(before, diagnosticFiles(t, dir)) {
		t.Fatal("semantic reads changed source store files")
	}
}

func assertReadinessIdentityRefused(t *testing.T, reader readinessControl, observed target.Snapshot, identity target.Identity, policyHash string) {
	t.Helper()
	observed.Identity = identity
	if domains, err := reader.Routes(t.Context(), observed, policyHash); err == nil || domains != nil {
		t.Fatal("unmeasured or crossed identity authorized routes", domains, err)
	}
	if backups, err := reader.Backups(t.Context(), identity); err == nil || backups != nil {
		t.Fatal("unmeasured or crossed identity authorized backups", backups, err)
	}
}

func TestReadinessWithdrawalRequiresExactProtectedEvidence(t *testing.T) {
	r := newNativeWithdrawalRig(t)
	head, err := r.state.ReadManagedRouteState(t.Context(), r.original.App)
	if err != nil {
		t.Fatal(err)
	}
	var seq int
	var name, file string
	if err := r.db.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	reader := readinessControl{dir: filepath.Dir(file)}
	present := r.snapshot
	present.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: head.Generation, Files: []target.CaddyFile{{Name: head.App + ".caddy", Hash: head.FileHash}}})
	domains, err := reader.Routes(t.Context(), present, r.policy.Hash())
	if err != nil || len(domains) != len(r.desired.Domains) {
		t.Fatal("present committed route lost", domains, err)
	}
	unproved := r.snapshot
	unproved.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: r.snapshot.CaddyConfig.Value.Generation + 1, Files: []target.CaddyFile{}})
	if domains, err := reader.Routes(t.Context(), unproved, r.policy.Hash()); err == nil || domains != nil {
		t.Fatal("disk absence substituted for protected withdrawal", domains, err)
	}
	unknown := r.snapshot
	unknown.CaddyConfig = target.Observation[target.CaddyConfigSet]{Status: target.Unknown}
	if domains, err := reader.Routes(t.Context(), unknown, r.policy.Hash()); err == nil || domains != nil {
		t.Fatal("unknown route observation claimed known domains", domains, err)
	}
}

func TestReadinessBindingCoverageIncludesEveryDatabase(t *testing.T) {
	dir := t.TempDir()
	state, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	first, err := state.ReserveDatabase(t.Context(), readinessReviewReservation("main", "/data"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := state.ReserveDatabase(t.Context(), readinessReviewReservation("audit", "/audit"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	before := diagnosticFiles(t, dir)
	copy, err := store.OpenSnapshotReadOnly(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copy.Close() }()
	both := map[data.DatabaseID]data.SQLiteDatabaseBinding{first.Database.DatabaseID: first.Database, second.Database.DatabaseID: second.Database}
	if err := readinessIncarnationCoverage(t.Context(), copy, string(first.Database.IncarnationID), both); err != nil || len(both) != 0 {
		t.Fatal("exact bindings lost coverage", both, err)
	}
	onlyFirst := map[data.DatabaseID]data.SQLiteDatabaseBinding{first.Database.DatabaseID: first.Database}
	if err := readinessIncarnationCoverage(t.Context(), copy, string(first.Database.IncarnationID), onlyFirst); err == nil {
		t.Fatal("another prepared database disappeared behind an existing binding")
	}
	crossed := first.Database
	crossed.ReplicaBindingID = data.ReplicaBindingID(strings.Repeat("f", 32))
	bindings := map[data.DatabaseID]data.SQLiteDatabaseBinding{first.Database.DatabaseID: crossed, second.Database.DatabaseID: second.Database}
	if err := readinessIncarnationCoverage(t.Context(), copy, string(first.Database.IncarnationID), bindings); err == nil {
		t.Fatal("database name substituted for exact binding identity")
	}
	if !reflect.DeepEqual(before, diagnosticFiles(t, dir)) {
		t.Fatal("coverage read changed source store files")
	}
}

func readinessReviewReservation(name, mount string) store.DataReservation {
	return store.DataReservation{App: "hello", PolicyHash: "sha256:" + strings.Repeat("a", 64), Database: data.SQLiteDatabase{Name: data.DatabaseName(name), PersistentRoot: "/never-open-source", MountPath: data.ContainerMountPath(mount), Filename: "app.db", BackupDestination: "primary"}, Destination: data.Destination{Reference: "primary", Endpoint: "https://storage.invalid", Region: "region-1", Bucket: "backups", BasePrefix: "brine", CredentialRef: "primary"}}
}

func readinessReviewHostKey() string {
	blob := []byte{0, 0, 0, 11}
	blob = append(blob, []byte("ssh-ed25519")...)
	blob = append(blob, 0, 0, 0, 32)
	blob = append(blob, make([]byte, 32)...)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " fixture"
}
