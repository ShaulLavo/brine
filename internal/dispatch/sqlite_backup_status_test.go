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

func TestSQLiteStatusStrictRequestAndResponse(t *testing.T) {
	for _, raw := range []string{`{"app":"example","database":"","path":"/live"}`, `{"app":"example","app":"foreign","database":""}`, `{"app":"example","database":"../data"}`, `{"app":"example"}`} {
		if _, err := decodeSQLiteBackupStatus(json.RawMessage(raw)); err == nil {
			t.Fatal("accepted unsafe status request", raw)
		}
	}
	if class, ok := ClassOf("sqlite_backup_status"); !ok || class != ReadOnly {
		t.Fatal("status is not read only")
	}
	now := time.Now().UTC()
	s := sqlitestatus.Compose(now, sqlitestatus.Facts{App: "example", Database: "main", DatabaseID: strings.Repeat("a", 32), BindingID: strings.Repeat("b", 32), EpochID: strings.Repeat("c", 32), Credential: sqlitestatus.Credential{State: "missing"}}, sqlitestatus.Observations{Service: sqlitestatus.Service{State: "unknown", ObservedAt: now}, Remote: restore.RemoteProbe{State: restore.RemoteNotChecked, ObservedAt: now}, Watermark: sqlitestatus.Watermark{State: "not_checked", ObservedAt: now}})
	raw, err := json.Marshal(result.Success("brine host sqlite_backup_status", s))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeResponse(raw, "sqlite_backup_status"); err != nil {
		t.Fatal("valid status refused", err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"service":{"state":"unknown"`, `"service":{"secret":"PLANTED","state":"unknown"`, 1),
		strings.Replace(string(raw), `"loss":"unknown"`, `"loss":"none"`, 1),
		strings.Replace(string(raw), `"local_position":null`, `"local_position":0`, 1),
		strings.Replace(string(raw), `"state":"not_checked","observed_at"`, `"state":"PLANTED_PROVIDER_MESSAGE","observed_at"`, 1),
	} {
		if _, err := DecodeResponse([]byte(bad), "sqlite_backup_status"); err == nil {
			t.Fatal("accepted untruthful or expanded machine status")
		}
	}
}
