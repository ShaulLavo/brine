package restic

import (
	"encoding/json"
	"strings"
	"testing"
)

func backupSummary(t *testing.T) []byte {
	t.Helper()
	fields := map[string]any{"message_type": "summary", "snapshot_id": strings.Repeat("a", 64), "total_duration": 0.5, "backup_start": "2026-10-12T00:00:00Z", "backup_end": "2026-10-12T00:00:01Z"}
	for _, key := range summaryFields[:12] {
		fields[key] = 0
	}
	fields["total_files_processed"], fields["total_bytes_processed"] = 1, 3584
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBackupJSONRefusesPartialOrAmbiguousSuccess(t *testing.T) {
	raw := backupSummary(t)
	if _, err := parseBackup(raw, 3584); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		string(raw) + "\n" + string(raw),
		strings.Replace(string(raw), `"snapshot_id":"`+strings.Repeat("a", 64)+`"`, `"snapshot_id":"latest"`, 1),
		strings.Replace(string(raw), `"total_files_processed":1`, `"total_files_processed":2`, 1),
		strings.Replace(string(raw), `"message_type":"summary"`, `"message_type":"summary","message_type":"summary"`, 1),
		`{"message_type":"status","private_extra":"value"}` + "\n" + string(raw),
		`{"message_type":"error"}` + "\n" + string(raw),
		string(raw[:len(raw)-1]),
	} {
		if _, err := parseBackup([]byte(invalid), 3584); err == nil {
			t.Fatal("ambiguous backup success admitted")
		}
	}
}

func TestCheckRefusesRepairOrPartialEvidence(t *testing.T) {
	valid := `{"message_type":"summary","num_errors":0,"broken_packs":null,"suggest_repair_index":false,"suggest_prune":false}`
	if err := parseCheck([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(valid, `"num_errors":0`, `"num_errors":1`, 1),
		strings.Replace(valid, `"suggest_prune":false`, `"suggest_prune":true`, 1),
		strings.Replace(valid, `"broken_packs":null`, `"broken_packs":["broken"]`, 1),
		strings.Replace(valid, `"suggest_repair_index":false`, `"suggest_repair_index":null`, 1),
	} {
		if err := parseCheck([]byte(invalid)); err == nil {
			t.Fatal("bad repository check admitted")
		}
	}
}
