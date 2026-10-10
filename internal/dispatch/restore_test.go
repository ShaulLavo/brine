package dispatch

import (
	"strings"
	"testing"
)

func TestRestoreRequestIsStrictAndReadOnly(t *testing.T) {
	class, ok := ClassOf("restore_test")
	if !ok || class != ReadOnly {
		t.Fatal("restore test needs D8 read-only scope")
	}
	valid := `{"app":"hello","database":"main","txid":"7","point":""}`
	if _, err := decodeRestoreTest([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		strings.Replace(valid, `"point":""`, `"point":"p1"`, 1),
		strings.Replace(valid, `"txid":"7"`, `"txid":"0"`, 1),
		strings.Replace(valid, `"txid":"7"`, `"txid":"+7"`, 1),
		strings.Replace(valid, `"txid":"7"`, `"txid":null`, 1),
		strings.Replace(valid, `"database":"main"`, `"database":"main","database":"audit"`, 1),
		strings.Replace(valid, `"point":""`, `"point":"","sql":"SELECT 1"`, 1),
		strings.Replace(valid, `"database":"main",`, ``, 1),
	} {
		if _, err := decodeRestoreTest([]byte(raw)); err == nil {
			t.Fatal("unsafe restore request accepted", raw)
		}
	}
}
