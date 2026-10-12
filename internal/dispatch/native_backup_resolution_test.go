package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeBackupResolutionDispatchRefusesPathsKeysAndEvidence(t *testing.T) {
	for _, operation := range []string{"native_backup_inspect", "native_backup_review"} {
		codec := operations[operation].decode
		if _, err := codec([]byte(`{"operation_id":"original"}`)); err != nil {
			t.Fatal(operation, err)
		}
		for _, raw := range []string{`{"operation_id":"../secret"}`, `{"operation_id":"original","source":"/private"}`, `{"operation_id":"original","force":true}`, `{"operation_id":null}`} {
			if _, err := codec([]byte(raw)); err == nil {
				t.Fatal(operation, "raw authority admitted")
			}
		}
	}
	raw, _ := json.Marshal(NativeBackupResolveArgs{ReviewID: strings.Repeat("1", 64), IdempotencyKey: strings.Repeat("2", 32)})
	if _, err := decodeNativeBackupResolve(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNativeBackupResolve([]byte(strings.Replace(string(raw), `"review_id":`, `"repository":"s3:unrelated","review_id":`, 1))); err == nil {
		t.Fatal("raw repository admitted")
	}
}
