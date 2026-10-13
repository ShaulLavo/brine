//go:build linux

package host

import (
	"testing"
)

func TestEngineNamespaceMappingRejectsAmbiguityAndOverflow(t *testing.T) {
	for _, raw := range []string{"", "0 0 0", "0 0 2\n1 10 1", "0 0 2\n10 1 1", "4294967295 0 2", "0 4294967295 2", "0 0 4294967296", "-1 0 1", "0 0 1 trailing", "0 0 1\ncaller"} {
		if _, err := parseEngineIDMap([]byte(raw), 65534); err == nil {
			t.Fatal("invalid authoritative map admitted")
		}
	}
	mapping, err := parseEngineIDMap([]byte("0 10000 2\n1000 1001 1\n"), 65534)
	if err != nil || mapping.namespaceID(10000) != 0 || mapping.namespaceID(10001) != 1 || mapping.namespaceID(1001) != 1000 || mapping.namespaceID(0) != 65534 {
		t.Fatal("boundary mapping incorrect", err)
	}
}
