package liverestore

import (
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/datainit"
	"strings"
	"testing"
	"time"
)

func TestResolutionReviewCanonicalAndBounded(t *testing.T) {
	requester, err := datainit.ParseRequester("deploy:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := Resolution{Format: "brine-native-live-restore-resolution-v1", OriginalOperationID: "original", ApplyPlanID: "sha256:" + strings.Repeat("b", 64), Requester: requester, EventSHA256: strings.Repeat("c", 64), InputSHA256: strings.Repeat("d", 64), ObservationSHA256: strings.Repeat("e", 64), IssuedAt: now, ExpiresAt: now.Add(MaxReviewLifetime)}
	raw, err := json.Marshal(r)
	got, decodeErr := DecodeResolution(raw)
	if err != nil || decodeErr != nil || got.ID() != r.ID() || r.ID() == "" {
		t.Fatal("canonical resolution failed", err, decodeErr)
	}
	changed := r
	changed.ObservationSHA256 = strings.Repeat("f", 64)
	if changed.ID() == r.ID() {
		t.Fatal("changed layout kept review")
	}
	changed.ExpiresAt = now.Add(MaxReviewLifetime + time.Second)
	if changed.ID() != "" {
		t.Fatal("unbounded review accepted")
	}
	for _, bad := range []string{strings.Replace(string(raw), `{"format":`, `{"source_path":"/unsafe","format":`, 1), strings.Replace(string(raw), `"input_sha256":`, `"input_sha256":null,"input_sha256":`, 1)} {
		if _, err := DecodeResolution([]byte(bad)); err == nil {
			t.Fatal("raw authority or duplicate field admitted")
		}
	}
}
