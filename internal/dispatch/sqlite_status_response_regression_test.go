package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/sqlitestatus"
)

func statusEnvelopeFixture(t *testing.T, source *restore.SQLiteSource) []byte {
	t.Helper()
	now := time.Now().UTC()
	facts := sqlitestatus.Facts{App: "example", Database: "main", DatabaseID: strings.Repeat("a", 32), BindingID: strings.Repeat("b", 32), EpochID: strings.Repeat("c", 32), Credential: sqlitestatus.Credential{State: "missing"}}
	if source != nil {
		facts.LastVerified = &sqlitestatus.Verified{OperationID: "verified-operation", Source: *source, VerifiedAt: now, InvariantCheck: "unknown"}
	}
	status := sqlitestatus.Compose(now, facts, sqlitestatus.Observations{Service: sqlitestatus.Service{State: "unknown", ObservedAt: now}, Remote: restore.RemoteProbe{State: restore.RemoteNotChecked, ObservedAt: now}, Watermark: sqlitestatus.Watermark{State: "not_checked", ObservedAt: now}})
	raw, err := json.Marshal(result.Success("brine host sqlite_backup_status", status))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRejectNullRequiredStatusScalars(t *testing.T) {
	original := string(statusEnvelopeFixture(t, nil))
	if _, err := DecodeResponse([]byte(original), "sqlite_backup_status"); err != nil {
		t.Fatal("valid positive control failed", err)
	}
	for _, field := range []string{`"committed":false`, `"version":0`, `"renewal_needed":false`} {
		name := strings.SplitN(field, ":", 2)[0]
		mutated := strings.Replace(original, field, name+":null", 1)
		if mutated == original {
			t.Fatal("control failed to replace field", name)
		}
		if _, err := DecodeResponse([]byte(mutated), "sqlite_backup_status"); err == nil {
			t.Errorf("required scalar %s was accepted as null", name)
		}
	}
}

func historicalLTXFixture(recoverability bool, barrier *restore.BarrierReceipt) restore.SQLiteSource {
	return restore.SQLiteSource{Kind: restore.LitestreamLTX, LTX: &restore.LTXSource{BindingID: strings.Repeat("b", 32), Epoch: strings.Repeat("c", 32), TXID: 7, Recoverability: recoverability, Barrier: barrier}}
}

func TestRejectInvalidHistoricalSource(t *testing.T) {
	valid := historicalLTXFixture(true, nil)
	if _, err := DecodeResponse(statusEnvelopeFixture(t, &valid), "sqlite_backup_status"); err != nil {
		t.Fatal("valid historical source control failed", err)
	}
	barrier := restore.BarrierReceipt{BindingID: strings.Repeat("b", 32), Epoch: strings.Repeat("c", 32), TXID: 7, ReplicaTXID: 7, ObservedAt: time.Now().UTC(), Succeeded: true}
	exact := historicalLTXFixture(false, &barrier)
	if _, err := DecodeResponse(statusEnvelopeFixture(t, &exact), "sqlite_backup_status"); err != nil {
		t.Fatal("valid exact barrier control failed", err)
	}
	failed := barrier
	failed.Succeeded = false
	foreign := barrier
	foreign.BindingID = "foreign-source"
	wrongTXID := barrier
	wrongTXID.TXID = 8
	wrongEpoch := barrier
	wrongEpoch.Epoch = "foreign-epoch"
	behind := barrier
	behind.ReplicaTXID = 6
	unobserved := barrier
	unobserved.ObservedAt = time.Time{}
	cases := map[string]restore.SQLiteSource{
		"exact_without_barrier":       historicalLTXFixture(false, nil),
		"recoverability_with_barrier": historicalLTXFixture(true, &barrier),
		"failed_barrier":              historicalLTXFixture(false, &failed),
		"foreign_barrier":             historicalLTXFixture(false, &foreign),
		"crossed_txid":                historicalLTXFixture(false, &wrongTXID),
		"crossed_epoch":               historicalLTXFixture(false, &wrongEpoch),
		"replica_behind":              historicalLTXFixture(false, &behind),
		"unobserved_barrier":          historicalLTXFixture(false, &unobserved),
	}
	for name, source := range cases {
		if _, err := DecodeResponse(statusEnvelopeFixture(t, &source), "sqlite_backup_status"); err == nil {
			t.Errorf("invalid historical source accepted: %s", name)
		}
	}
}
