package restic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTransportPointCodecIsClosedAndPreservesFailedDestination(t *testing.T) {
	point := TransportPoint{Identity: testIdentity(), Scope: testScope(), SnapshotID: strings.Repeat("a", 64), ManifestSHA256: strings.Repeat("b", 64), ArchiveSHA256: strings.Repeat("c", 64), ArchiveBytes: 3584}
	raw, err := json.Marshal(point)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TransportPoint
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded != point {
		t.Fatal("valid point round trip failed", err)
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), `"archive_bytes":3584`, `"archive_bytes":null`, 1),
		strings.Replace(string(raw), `"snapshot_id":"`+point.SnapshotID+`"`, `"snapshot_id":"latest"`, 1),
		strings.Replace(string(raw), `"recovery_version":1`, `"recovery_version":1,"unknown":true`, 1),
		strings.Replace(string(raw), `"archive_bytes":3584`, `"archive_bytes":3584,"archive_bytes":3584`, 1),
		string(raw) + `{}`,
	} {
		if err := json.Unmarshal([]byte(invalid), &decoded); err == nil {
			t.Fatal("untrusted point admitted")
		}
		if decoded != point {
			t.Fatal("failed decoding changed admitted point")
		}
	}
}
