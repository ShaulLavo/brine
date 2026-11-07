package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

func TestReviewDeclarationMatchesActualDatabase(t *testing.T) {
	fx := newPrepFixture(t)
	ctx := context.Background()
	release, err := fx.s.CurrentRelease(ctx, fx.app)
	if err != nil {
		t.Fatal(err)
	}
	_, desired, err := fx.s.LoadPlan(ctx, release.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	in := fixture(t)
	in.Desired = desired
	in.Desired.Databases[0].Filename = "different.db"
	fx.commitRelease(in, "changed-declaration")
	host := newPrepHost(fx, true)
	_, _, err = fx.prepare(host, nil, prepID("1"), "")
	if err == nil || host.called("barrier") {
		t.Fatal("preparation accepted a release declaring a different database filename")
	}
}

type reviewUnavailableSource struct{ sqliteprep.StoppedSource }

func (s reviewUnavailableSource) SQLiteSourceHold() *SQLiteSourceHold {
	return sqliteHoldOf(s.StoppedSource)
}
func (s reviewUnavailableSource) Recheck(context.Context) (sqliteprep.StoppedBundle, error) {
	return sqliteprep.StoppedBundle{}, errPrepFault
}

type reviewFinalAuthorityJournal struct{ *SQLitePreparationJournal }

func (j *reviewFinalAuthorityJournal) CompletePrepare(ctx context.Context, h sqliteprep.History, p sqliteprep.PreparedPoint, held sqliteprep.StoppedSource) (sqliteprep.Receipt, error) {
	return j.SQLitePreparationJournal.CompletePrepare(ctx, h, p, reviewUnavailableSource{held})
}
func (j *reviewFinalAuthorityJournal) CompleteAbandon(ctx context.Context, task sqliteprep.Task, in sqliteprep.Input, a sqliteprep.Abandonment, held sqliteprep.StoppedSource) (sqliteprep.Receipt, error) {
	return j.SQLitePreparationJournal.CompleteAbandon(ctx, task, in, a, reviewUnavailableSource{held})
}
func TestReviewCompletionRequiresPhysicalSourceAuthority(t *testing.T) {
	fx := newPrepFixture(t)
	j := &reviewFinalAuthorityJournal{fx.journal()}
	task, _, err := fx.prepare(newPrepHost(fx, true), j, prepID("1"), "")
	if err == nil || fx.state(task.OperationID) == ops.Succeeded {
		t.Fatal("atomic prepare succeeded although its held source refuses Recheck")
	}
}
func TestReviewAbandonRequiresPhysicalSourceAuthority(t *testing.T) {
	fx := newPrepFixture(t)
	fx.prepared(prepID("1"))
	task, in := fx.preflight(sqliteprep.AbandonTask, prepID("1"), "")
	j := &reviewFinalAuthorityJournal{fx.journal()}
	_, err := fx.service(newPrepHostStopped(fx), j).Abandon(context.Background(), task, in)
	if err == nil || fx.state(task.OperationID) == ops.Succeeded {
		t.Fatal("abandon released the fence although its held source refuses Recheck")
	}
}

type reviewReleaseHoldJournal struct {
	*SQLitePreparationJournal
	hold *SQLiteSourceHold
}

func (j *reviewReleaseHoldJournal) Claim(ctx context.Context, i sqliteprep.Intent, o sqliteprep.HostObservation, held sqliteprep.StoppedSource) error {
	j.hold = sqliteHoldOf(held)
	return j.SQLitePreparationJournal.Claim(ctx, i, o, held)
}
func (j *reviewReleaseHoldJournal) Append(ctx context.Context, i sqliteprep.Intent, e sqliteprep.Event) error {
	if e.Stage == sqliteprep.CoverageIntent {
		_ = j.hold.Release()
	}
	return j.SQLitePreparationJournal.Append(ctx, i, e)
}
func TestReviewAppendRequiresRetainedLifetimeLock(t *testing.T) {
	fx := newPrepFixture(t)
	host := newPrepHost(fx, false)
	j := &reviewReleaseHoldJournal{SQLitePreparationJournal: fx.journal()}
	_, _, err := fx.prepare(host, j, prepID("1"), "")
	if err == nil {
		t.Fatal("control must end in recovery")
	}
	if host.called("upload") {
		t.Fatal("coverage intent committed and upload ran after lifetime hold was released")
	}
}

func TestReviewConsumerReadRequiresRealTaskJoin(t *testing.T) {
	fx := newPrepFixture(t)
	prepare, _ := fx.prepared(prepID("1"))
	p := fx.preparation(prepID("1"))
	task, _ := fx.preflight(sqliteprep.AbandonTask, prepID("1"), "")
	c := sqliteprep.Consumer{PreparationID: prepID("1"), OperationID: task.OperationID, Requester: "deploy:" + strings.Repeat("b", 64), App: fx.app, InputSHA256: strings.Repeat("c", 64), Kind: sqliteprep.AbandonConsumer, PolicySHA256: p.intent.Scope.PolicySHA256, ClaimedAt: time.Now().UTC()}
	raw, err := sqliteprep.Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Exercise independent read validation even if an earlier writer lacked
	// the new SQL identity and completion guards.
	if _, err = fx.s.db.ExecContext(ctx, "DROP TRIGGER IF EXISTS data_sqlite_point_consumers_identity; DROP TRIGGER IF EXISTS data_sqlite_consumer_completions_exact"); err != nil {
		t.Fatal(err)
	}

	if _, err = fx.s.db.ExecContext(ctx, "INSERT INTO data_sqlite_point_consumers VALUES(?,?,'abandon',NULL,?)", c.PreparationID, c.OperationID, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = fx.s.db.ExecContext(ctx, "INSERT INTO data_sqlite_consumer_completions VALUES(?,?,?)", c.PreparationID, c.OperationID, p.intent.Fence.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = fx.s.db.ExecContext(ctx, "UPDATE data_fences SET state='released' WHERE id=?", p.intent.Fence.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = fx.s.ReadSQLiteTaskOutcome(ctx, prepare.OperationID); err == nil {
		t.Fatal("joined historical success accepted a consumer with crossed requester/input and no successful consumer task")
	}
}

func TestReviewSchemaRejectsNonHexSQLiteInput(t *testing.T) {
	fx := newPrepFixture(t)
	for _, digit := range []string{"z", "A"} {
		_, err := fx.s.db.Exec("INSERT INTO operations VALUES(?,NULL,?,?,'queued','t','t','sqlite_prepare',?,?,'')", "invalid-hash-"+digit, prepRequester, "invalid-hash-"+digit, fx.app, strings.Repeat(digit, 64))
		if err == nil {
			t.Fatal("v35 operations constraint accepted a non-lowercase-hex canonical input hash")
		}
	}
}

func TestReviewDeclarationMatchesRegisteredCatalog(t *testing.T) {
	fx := newPrepFixture(t)
	ctx := context.Background()
	release, err := fx.s.CurrentRelease(ctx, fx.app)
	if err != nil {
		t.Fatal(err)
	}
	_, desired, err := fx.s.LoadPlan(ctx, release.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	in := fixture(t)
	in.Desired = desired
	in.Desired.SchemaDefinitions[0].CatalogSHA256 = strings.Repeat("8", 64)
	fx.commitRelease(in, "changed-schema-declaration")
	host := newPrepHost(fx, true)
	_, _, err = fx.prepare(host, nil, prepID("1"), "")
	if err == nil || host.called("barrier") {
		t.Fatal("preparation ignored the release's declared catalog and used the registry's different catalog")
	}
}

type reviewFutureJournal struct{ *SQLitePreparationJournal }

func (j *reviewFutureJournal) CompletePrepare(ctx context.Context, h sqliteprep.History, p sqliteprep.PreparedPoint, held sqliteprep.StoppedSource) (sqliteprep.Receipt, error) {
	p.PreparedAt = p.PreparedAt.Add(time.Hour)
	p.ExpiresAt = p.PreparedAt.Add(sqliteprep.PointLifetime)
	return j.SQLitePreparationJournal.CompletePrepare(ctx, h, p, held)
}
func TestReviewCompletionTimeIsStoreBounded(t *testing.T) {
	fx := newPrepFixture(t)
	j := &reviewFutureJournal{fx.journal()}
	task, _, _ := fx.prepare(newPrepHost(fx, true), j, prepID("1"), "")
	if fx.state(task.OperationID) == ops.Succeeded {
		t.Fatal("store committed prepare with point and receipt one hour in the future")
	}
}

func reviewCredentialScope(t *testing.T, field, value string, admit bool) {
	t.Helper()
	fx := newPrepFixture(t)
	ctx := context.Background()
	p, err := fx.s.ReadReplicaPermit(ctx, fx.reserved.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	p.Replica.CredentialVersion = 2
	p.Replica.CredentialFile = strings.Replace(p.Replica.CredentialFile, "v1.env", "v2.env", 1)
	if err = fx.s.CommitReplicaBinding(ctx, p.Replica); err != nil {
		t.Fatal(err)
	}
	if field == "$.app" && value == "same" {
		value = fx.app
	}
	id := prepID("9")
	query := "INSERT INTO data_credential_records SELECT ?,binding_id,kind,CAST(json_set(canonical,'$.id',?,'$.version',2,?,?) AS BLOB) FROM data_credential_records WHERE kind='receipt' AND json_extract(canonical,'$.version')=1"
	if _, err = fx.s.db.ExecContext(ctx, query, id, id, field, value); err != nil {
		t.Fatal(err)
	}
	host := newPrepHost(fx, true)
	_, _, err = fx.prepare(host, nil, prepID("1"), "")
	if admit && err != nil {
		t.Fatalf("correct delivered-version control refused: %v", err)
	}
	if !admit && (err == nil || host.called("barrier")) {
		t.Fatalf("crossed credential %s admitted, error %v, effects %v", field, err, host.calls)
	}
}
func TestReviewCredentialCrossedApp(t *testing.T) { reviewCredentialScope(t, "$.app", "other", false) }
func TestReviewCredentialCrossedDestination(t *testing.T) {
	reviewCredentialScope(t, "$.destination", "foreign", false)
}
func TestReviewCredentialCorrectVersionControl(t *testing.T) {
	reviewCredentialScope(t, "$.app", "same", true)
}
func TestReviewCredentialCrossedBindingControl(t *testing.T) {
	reviewCredentialScope(t, "$.binding_id", prepID("8"), false)
}
func TestReviewCredentialCrossedEpochControl(t *testing.T) {
	reviewCredentialScope(t, "$.epoch_id", prepID("8"), false)
}

func reviewSQLAbandonConsumer(t *testing.T) (*prepFixture, sqliteprep.Consumer) {
	t.Helper()
	fx := newPrepFixture(t)
	fx.prepared(prepID("1"))
	p := fx.preparation(prepID("1"))
	task, _ := fx.preflight(sqliteprep.AbandonTask, prepID("1"), "")
	digest, err := sqliteprep.Fingerprint(*p.point)
	if err != nil {
		t.Fatal(err)
	}
	return fx, sqliteprep.Consumer{PreparationID: prepID("1"), OperationID: task.OperationID, Requester: task.Requester, App: task.App, InputSHA256: task.InputSHA256, Kind: sqliteprep.AbandonConsumer, PreparedSHA256: digest, PolicySHA256: p.intent.Scope.PolicySHA256, ClaimedAt: time.Now().UTC()}
}

func insertReviewConsumer(fx *prepFixture, c sqliteprep.Consumer) error {
	raw, err := sqliteprep.Encode(c)
	if err != nil {
		return err
	}
	_, err = fx.s.db.ExecContext(context.Background(), "INSERT INTO data_sqlite_point_consumers VALUES(?,?,'abandon',NULL,?)", c.PreparationID, c.OperationID, raw)
	return err
}

func TestReviewSQLRejectsCrossedConsumerIdentity(t *testing.T) {
	fx, c := reviewSQLAbandonConsumer(t)
	c.Requester = "deploy:" + strings.Repeat("b", 64)
	if err := insertReviewConsumer(fx, c); err == nil {
		t.Fatal("SQL accepted a consumer whose requester differs from its real task")
	}
}

func TestReviewSQLRequiresSucceededConsumerCompletion(t *testing.T) {
	fx, c := reviewSQLAbandonConsumer(t)
	if err := insertReviewConsumer(fx, c); err != nil {
		t.Fatal(err)
	}
	_, err := readSQLitePreparation(context.Background(), fx.s.db, c.PreparationID)
	if err == nil {
		t.Fatal("active abandon row must not be read as completed consumption")
	}
	var fence string
	if err = fx.s.db.QueryRow("SELECT fence_id FROM data_sqlite_preparations WHERE id=?", c.PreparationID).Scan(&fence); err != nil {
		t.Fatal(err)
	}
	if _, err = fx.s.db.Exec("INSERT INTO data_sqlite_consumer_completions VALUES(?,?,?)", c.PreparationID, c.OperationID, fence); err == nil {
		t.Fatal("SQL accepted a completion for an unfinished consumer operation")
	}
}

func TestReviewSchemaRejectsNonHexInputIdentity(t *testing.T) {
	fx := newPrepFixture(t)
	fx.preflight(sqliteprep.PrepareTask, prepID("1"), "")
	for _, digit := range []string{"z", "A"} {
		_, err := fx.s.db.Exec("INSERT INTO data_sqlite_task_inputs SELECT ?,kind,app,database_id,preparation_id,supersedes,canonical FROM data_sqlite_task_inputs LIMIT 1", strings.Repeat(digit, 64))
		if err == nil {
			t.Fatal("v35 input constraint accepted a non-lowercase-hex identity")
		}
	}
}
