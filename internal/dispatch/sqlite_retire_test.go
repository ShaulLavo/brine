package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestSQLiteRetireStrictCommands(t *testing.T) {
	inspect := `{"archive_id":"` + strings.Repeat("a", 32) + `"}`
	resolve := `{"operation_id":"original","disposition":"abandon","idempotency_key":"fixture"}`
	for op, raw := range map[string]string{"sqlite_retire_inspect": inspect, "sqlite_retire_resolve": resolve} {
		checkSQLiteRetireRequest(t, op, raw)
	}
	for _, raw := range []string{`{"archive_id":null}`, `{"archive_id":"bad"}`, strings.Replace(inspect, "archive_id", "ArchiveID", 1), strings.TrimSuffix(inspect, "}") + `,"archive_id":"` + strings.Repeat("b", 32) + `"}`, strings.TrimSuffix(inspect, "}") + `,"extra":true}`} {
		rejectSQLiteRetireRequest(t, "sqlite_retire_inspect", raw)
	}
	for _, raw := range []string{strings.Replace(resolve, `"abandon"`, `"purge"`, 1), strings.Replace(resolve, `"abandon"`, `null`, 1), strings.Replace(resolve, `"fixture"`, `null`, 1), strings.Replace(resolve, `"original"`, `null`, 1), strings.TrimSuffix(resolve, "}") + `,"extra":true}`, strings.Replace(resolve, `"disposition"`, `"Disposition"`, 1)} {
		rejectSQLiteRetireRequest(t, "sqlite_retire_resolve", raw)
	}
	if OperationTimeout("sqlite_retire_inspect") != OperationTimeout("native_retire_inspect") || CallTimeout("sqlite_retire_resolve") != CallTimeout("native_retire_resolve") {
		t.Fatal("retire budgets drifted")
	}
}
func checkSQLiteRetireRequest(t *testing.T, op, raw string) {
	t.Helper()
	b, err := json.Marshal(Request{SchemaVersion: 1, Op: op, RequestID: "fixture", Args: json.RawMessage(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeRequest(b); err != nil {
		t.Fatal(op, err)
	}
	class, _ := ClassOf(op)
	if (class == ReadOnly) != (op == "sqlite_retire_inspect") {
		t.Fatal(class)
	}
}
func rejectSQLiteRetireRequest(t *testing.T, op, raw string) {
	t.Helper()
	b, err := json.Marshal(Request{SchemaVersion: 1, Op: op, RequestID: "fixture", Args: json.RawMessage(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeRequest(b); err == nil {
		t.Fatal("accepted", raw)
	}
}
func TestSQLiteRetireStrictInspectionResponse(t *testing.T) {
	i := archive.Inspection{Preparation: &archive.PreparationInspection{Stage: archive.Intent, Sequence: 1, CanAbandon: true, ClosedReason: "continue_unavailable"}, Disposition: archive.Disposition{ArchiveID: data.ArchiveID(strings.Repeat("a", 32))}, Trees: []archive.TreeInspection{{Index: 0, Location: "source"}}}
	raw, err := json.Marshal(result.Success("brine host sqlite_retire_inspect", i))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeResponse(raw, "sqlite_retire_inspect"); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{strings.Replace(string(raw), `"can_reconcile":false`, `"can_reconcile":true`, 1), strings.Replace(string(raw), `"stage":""`, `"stage":"foreign"`, 1), strings.Replace(string(raw), `"closed_reason":"continue_unavailable"`, `"closed_reason":"foreign"`, 1), strings.Replace(string(raw), `"can_abandon":true`, `"can_abandon":null`, 1), strings.Replace(string(raw), `"complete":false`, `"complete":null`, 1), strings.Replace(string(raw), `"stage":"intent"`, `"stage":"intent","extra":1`, 1), strings.Replace(string(raw), `"location":"source"`, `"location":"source","location":"archive"`, 1), strings.Replace(string(raw), `"can_reconcile":false`, `"can_reconcile":null`, 1)} {
		if _, err = DecodeResponse([]byte(mutation), "sqlite_retire_inspect"); err == nil {
			t.Fatal("accepted corrupt inspection", mutation)
		}
	}
}
func TestSQLiteRetireSummaryWire(t *testing.T) {
	summary := archive.RemovalSummary{ArchiveID: data.ArchiveID(strings.Repeat("a", 32)), ExpiresAt: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), RetainDays: 30, Units: []target.Unit{}, Databases: []archive.RemovalDatabaseSummary{{Name: "main", DatabaseID: data.DatabaseID(strings.Repeat("b", 32)), Arm: "never_written"}}}
	p := apps.ConfigPlan{Archive: &summary, PlanID: "sha256:" + strings.Repeat("c", 64), Kind: plan.Update, Lifecycle: plan.RemoveApp, Conflicts: []plan.Diagnostic{}}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = apps.DecodeConfigPlan(raw); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{strings.Replace(string(raw), `"retain_days":30`, `"retain_days":null`, 1), strings.Replace(string(raw), `"arm":"never_written"`, `"arm":"never_written","credentials":"private"`, 1), strings.Replace(string(raw), `"lifecycle":"remove_app"`, `"lifecycle":"start_app"`, 1), strings.Replace(string(raw), `"kind":"update"`, `"kind":"no-op"`, 1)} {
		if _, err = apps.DecodeConfigPlan([]byte(mutation)); err == nil {
			t.Fatal("accepted corrupt summary", mutation)
		}
	}
}
