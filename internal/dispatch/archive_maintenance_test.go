package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestPublicArchiveRequestsAreClosedAndTyped(t *testing.T) {
	id, key, plan := strings.Repeat("a", 32), strings.Repeat("b", 32), "sha256:"+strings.Repeat("c", 64)
	inputs := map[string]string{
		"archive_status":              `{"archive_id":"` + id + `"}`,
		"archive_maintenance_plan":    `{"archive_id":"` + id + `","kind":"purge_archive"}`,
		"archive_maintenance_apply":   `{"archive_id":"` + id + `","plan_id":"` + plan + `","idempotency_key":"` + key + `"}`,
		"archive_maintenance_resolve": `{"operation_id":"original-operation","idempotency_key":"` + key + `"}`,
		"archive_credentials_plan":    `{"archive_id":"` + id + `","binding_id":"` + id + `","epoch_id":"` + key + `","expires_at":null}`,
	}
	for op, raw := range inputs {
		request, err := EncodeRequest(Request{SchemaVersion: SchemaVersion, Op: op, RequestID: "archive-test", Args: json.RawMessage(raw)})
		if err != nil {
			t.Fatal("valid request refused", op, err)
		}
		if _, err = DecodeRequest(request); err != nil {
			t.Fatal(op, err)
		}
		bad := strings.Replace(raw, "{", `{"host_command":"rm -rf /",`, 1)
		if _, err = operations[op].decode([]byte(bad)); err == nil {
			t.Fatal("raw host body accepted", op)
		}
		bad = strings.Replace(raw, "{", `{"archive_id":null,`, 1)
		if _, err = operations[op].decode([]byte(bad)); err == nil {
			t.Fatal("duplicate/foreign selector accepted", op)
		}
		class, known := ClassOf(op)
		if !known || op == "archive_status" && class != ReadOnly || op != "archive_status" && class != Mutating {
			t.Fatal("authority class mismatch", op)
		}
	}
}
func TestPublicArchivedCredentialWireRedactsAndBindsOriginalScope(t *testing.T) {
	id, key, plan := strings.Repeat("a", 32), strings.Repeat("b", 32), "sha256:"+strings.Repeat("c", 64)
	wire, err := EncodeArchiveCredentialSet(id, id, key, plan, []byte(`{"access_key_id":"PLANTED_ACCESS","secret_access_key":"PLANTED_SECRET"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(wire)
	decoded, err := decodeArchiveCredentialSet(wire)
	if err != nil {
		t.Fatal(err)
	}
	args := decoded.(ArchiveCredentialSetArgs)
	defer args.Clear()
	safe, _ := json.Marshal(args)
	if strings.Contains(string(safe), "PLANTED") || strings.Contains(fmt.Sprintf("%v %#v", args, args), "PLANTED") {
		t.Fatal("archive secret entered metadata")
	}
	server := NewServer("fixture", nil).WithJobs(nil, func(context.Context, Class) error { return result.New(result.DispatchOperationRefused, nil) })
	req, _ := EncodeRequest(Request{SchemaVersion: SchemaVersion, Op: "archive_credentials_set", RequestID: "archive-private", Args: wire})
	defer clear(req)
	response, err := server.Handle(context.Background(), strings.NewReader(string(req)))
	if err == nil || response.OK {
		t.Fatal("private archive mutation escaped authorization")
	}
	encoded, _ := json.Marshal(response)
	if strings.Contains(string(encoded), "PLANTED") {
		t.Fatal("private credential escaped failure output")
	}
	for _, bad := range []string{
		strings.Replace(string(wire), `"packet":`, `"packet":null,"packet":`, 1),
		strings.Replace(string(wire), `"epoch_id":`, `"runtime":{},"epoch_id":`, 1),
		strings.Replace(string(wire), `"access_key_id":`, `"activate":true,"access_key_id":`, 1),
	} {
		if _, err = decodeArchiveCredentialSet([]byte(bad)); err == nil {
			t.Fatal("open/private mixed packet admitted")
		}
	}
}
func TestPublicArchiveResponsesRefuseForeignAndOpenState(t *testing.T) {
	now := time.Now().UTC()
	s := archive.MaintenanceStatus{ArchiveID: data.ArchiveID(strings.Repeat("a", 32)), State: "unverified", ObservedAt: now, RetiredAt: now, RetainUntil: now.Add(30 * 24 * time.Hour)}
	raw, _ := json.Marshal(result.Success("brine host archive_status", s))
	if _, err := DecodeResponse(raw, "archive_status"); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(raw), `"state":"unverified"`, `"state":"active"`, 1)
	if _, err := DecodeResponse([]byte(bad), "archive_status"); err == nil {
		t.Fatal("live activation response accepted for retained archive")
	}
	bad = strings.Replace(string(raw), `"state":"unverified"`, `"secret":"PLANTED","state":"unverified"`, 1)
	if _, err := DecodeResponse([]byte(bad), "archive_status"); err == nil {
		t.Fatal("secret body accepted in metadata status")
	}
}
