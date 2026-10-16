//go:build linux

package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestNativeStoppedStateRequiresActualCompleteKnownCleanFacts(t *testing.T) {
	issued, observed := time.Now().UTC().Add(-time.Second), time.Now().UTC()
	id, finished := strings.Repeat("a", 64), issued.Add(500*time.Millisecond)
	raw, err := json.Marshal([]any{map[string]any{"Id": id, "State": map[string]any{"Running": false, "Pid": 0, "ExitCode": 0, "OOMKilled": false, "FinishedAt": finished, "StoppedByUser": true, "Error": ""}}})
	if err != nil {
		t.Fatal(err)
	}
	if actual, err := decodeNativeStoppedState(raw, id, issued, observed); err != nil || !actual.Equal(finished) {
		t.Fatal("complete known clean facts refused", err)
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), `"ExitCode":0`, `"ExitCode":137`, 1),
		strings.Replace(string(raw), `"OOMKilled":false`, `"OOMKilled":true`, 1),
		strings.Replace(string(raw), `"Running":false`, `"Running":true`, 1),
		strings.Replace(string(raw), `"Pid":0`, `"Pid":1`, 1),
		strings.Replace(string(raw), `"StoppedByUser":true`, `"StoppedByUser":false`, 1),
		strings.Replace(string(raw), `"FinishedAt":`, `"other_finished":`, 1),
		strings.Replace(string(raw), `"ExitCode":0`, `"ExitCode":null`, 1),
		strings.Replace(string(raw), id, strings.Repeat("b", 64), 1),
		`[]`,
	} {
		if _, err := decodeNativeStoppedState([]byte(invalid), id, issued, observed); err == nil {
			t.Fatal("unknown or forced stopped state earned clean receipt")
		}
	}
	if _, err := decodeNativeStoppedState(raw, id, finished.Add(time.Second), observed); err == nil {
		t.Fatal("old finished timestamp rebound to new stop")
	}
	if _, err := decodeNativeStoppedState(raw, id, issued, issued); err == nil {
		t.Fatal("future stopped timestamp admitted")
	}
}

func TestNativeStopAttemptNeverTreatsMissingAuthorityAsClean(t *testing.T) {
	ctx, session := context.Background(), localexec.Session{}
	permit, _ := engineStartupFixture(t)
	if _, err := bindNativeStopAttempt(ctx, session, t.TempDir(), t.TempDir(), permit); err == nil {
		t.Fatal("unprotected source bound as live stop authority")
	}
	var absent *nativeStopAttempt
	if absent.stop(ctx, session, t.TempDir()) == nil {
		t.Fatal("nil stop attempt admitted")
	}
	if _, err := absent.observation(ctx, session); err == nil {
		t.Fatal("nil stop attempt produced receipt")
	}
	attempt := &nativeStopAttempt{issuedAt: time.Now().UTC()}
	if attempt.stop(ctx, session, t.TempDir()) == nil {
		t.Fatal("unknown issued stop retried")
	}
	attempt.close()
	if _, err := attempt.observation(ctx, session); err == nil {
		t.Fatal("absent handle produced receipt")
	}
}

func TestRetainedNativeStopReadbackSurvivesPodmanRefresh(t *testing.T) {
	id := strings.Repeat("a", 64)
	finished := time.Now().UTC().Add(-time.Minute)
	raw, err := json.Marshal([]any{map[string]any{"Id": id, "State": map[string]any{"Running": false, "Pid": 0, "ExitCode": 0, "OOMKilled": false, "FinishedAt": finished, "Error": ""}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNativeStoppedState(raw, id, finished, time.Now().UTC()); err == nil {
		t.Fatal("missing stop intent admitted a new clean stop")
	}
	actual, err := decodeNativeStoppedReadback(raw, id, finished, time.Now().UTC())
	if err != nil || !actual.Equal(finished) {
		t.Fatal("retained clean stop was lost after Podman cleared transient flags", err)
	}
	if _, err := decodeNativeStoppedReadback(raw, id, finished.Add(-time.Second), time.Now().UTC()); err == nil {
		t.Fatal("changed exit time reused the original clean stop")
	}
	if _, err := decodeNativeStoppedReadback(raw, id, time.Time{}, time.Now().UTC()); err == nil {
		t.Fatal("missing original receipt supplied readback authority")
	}
}
