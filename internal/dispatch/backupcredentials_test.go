package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/result"
	"strings"
	"testing"
)

func TestBackupCredentialPrivateRequest(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	packet := []byte(`{"access_key_id":"PLANTED_KEY","secret_access_key":"PLANTED_SECRET","session_token":"PLANTED_SESSION"}`)
	args, err := EncodeBackupCredentialSet("hello", "", id, packet)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeBackupCredentialSet(args)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", decoded, decoded), "PLANTED") {
		t.Fatal("decoded request leaked")
	}
	raw, err := EncodeRequest(Request{SchemaVersion: 1, Op: "backup_credentials_set", RequestID: "request1", Args: args})
	if err != nil {
		t.Fatal(err)
	}
	response, _ := NewServer("fixture", nil).Handle(context.Background(), bytes.NewReader(raw))
	if response.OK {
		t.Fatal("bypassed authorization")
	}
	b, _ := json.Marshal(response)
	if strings.Contains(string(b), "PLANTED") {
		t.Fatal("response echoed packet")
	}
	for _, bad := range []string{strings.Replace(string(args), `"app":"hello"`, `"app":"hello","app":"hello"`, 1), strings.Replace(string(args), `"session_token":"PLANTED_SESSION"`, `"session_token":"PLANTED_SESSION","provider_token":"PLANTED"`, 1)} {
		if _, err := decodeBackupCredentialSet([]byte(bad)); err == nil {
			t.Fatal("accepted invalid delivery")
		}
	}
}

func TestBackupCredentialDatabaseStrictWire(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	packet := []byte(`{"access_key_id":"fixture-access","secret_access_key":"fixture-secret"}`)
	raw, err := EncodeBackupCredentialSet("hello", "audit", id, packet)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeBackupCredentialSet(raw)
	if err != nil {
		t.Fatal(err)
	}
	args := decoded.(BackupCredentialSetArgs)
	defer args.Packet.Clear()
	if args.Database != "audit" {
		t.Fatal("database lost")
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), `"database":"audit",`, "", 1),
		strings.Replace(string(raw), `"database":"audit"`, `"database":"audit","database":"main"`, 1),
		strings.Replace(string(raw), `"database":"audit"`, `"database":"../audit"`, 1),
		strings.Replace(string(raw), `"database":"audit"`, `"database":null`, 1),
	} {
		if _, err := decodeBackupCredentialSet([]byte(invalid)); err == nil {
			t.Fatal("invalid selector accepted")
		}
	}
	for _, database := range []string{"", "audit"} {
		raw, err := json.Marshal(BackupCredentialPlanArgs{App: "hello", Database: database})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeBackupCredentialPlan(raw)
		if err != nil || decoded.(BackupCredentialPlanArgs).Database != database {
			t.Fatal("plan selector lost", err)
		}
	}
}

func TestBackupCredentialAdmissionRefreshHasExplicitGuidance(t *testing.T) {
	err := result.Classify(credentialFailure(backupcredentials.ErrAdmissionRefresh))
	if err.Code() != result.BackupAdmissionRefreshRequired || !strings.Contains(err.Error(), "explicit approved admission refresh") {
		t.Fatal("admission refresh guidance lost", err)
	}
}
