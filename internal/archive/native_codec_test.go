package archive

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeArchiveCodecRejectsUnknownDuplicateAndNullNestedRecords(t *testing.T) {
	p, _ := nativeArchiveFixture(t)
	original := p.Candidate.Databases[0]
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Database
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`"head":{`, `"allocation":{`, `"retention":[{`, `"transition_trees":[{`} {
		invalid := strings.Replace(string(raw), marker, marker+`"caller_grant":true,`, 1)
		if invalid == string(raw) {
			t.Fatal("fixture marker absent", marker)
		}
		if err = json.Unmarshal([]byte(invalid), &decoded); err == nil {
			t.Fatal("unknown nested grant admitted", marker)
		}
		preserved, marshalErr := json.Marshal(decoded)
		if marshalErr != nil || !bytes.Equal(preserved, raw) {
			t.Fatal("failed decode changed previous database", marshalErr)
		}
	}
	duplicate := strings.Replace(string(raw), `"version":2`, `"version":2,"version":2`, 1)
	if err = json.Unmarshal([]byte(duplicate), &decoded); err == nil {
		t.Fatal("duplicate nested head admitted")
	}
	for _, marker := range []string{`"retention":[`, `"transition_trees":[`} {
		invalid := strings.Replace(string(raw), marker, marker+`null,`, 1)
		if err = json.Unmarshal([]byte(invalid), &decoded); err == nil {
			t.Fatal("null nested record admitted", marker)
		}
	}
}
