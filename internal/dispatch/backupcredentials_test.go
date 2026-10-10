package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestBackupCredentialPrivateRequest(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	packet := []byte(`{"access_key_id":"PLANTED_KEY","secret_access_key":"PLANTED_SECRET","session_token":"PLANTED_SESSION"}`)
	args, err := EncodeBackupCredentialSet("hello", id, packet)
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
