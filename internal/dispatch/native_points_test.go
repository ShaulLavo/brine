package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestNativePointInspectionWireIsReferenceOnlyAndReadOnly(t *testing.T) {
	class, ok := ClassOf("native_point")
	if !ok || class != ReadOnly {
		t.Fatal("point inspection is not read-only")
	}
	raw := `{"kind":"verified","point_id":"` + strings.Repeat("a", 32) + `"}`
	if _, err := decodeNativePoint([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(raw, `"kind":"verified"`, `"kind":"online"`, 1),
		strings.Replace(raw, `"point_id":`, `"requester":"forged","point_id":`, 1),
		strings.Replace(raw, `"point_id":`, `"point_id":null,"point_id":`, 1),
		strings.Replace(raw, strings.Repeat("a", 32), "../source", 1),
	} {
		if _, err := decodeNativePoint([]byte(invalid)); err == nil {
			t.Fatal("point inspection accepted authority or path input")
		}
	}
	for _, op := range []string{"native_capture", "native_restore_test", "native_prepare"} {
		if _, ok := ClassOf(op); ok {
			t.Fatal("unfinished native effect dispatch admitted")
		}
	}
}

func TestNativePointInspectionResponseIsClosedAndDoesNotGrantEffects(t *testing.T) {
	now := time.Now().UTC()
	view := nativecapture.PointInspection{Kind: "verified", PointID: strings.Repeat("a", 32), PointSHA256: strings.Repeat("b", 64), OperationID: "capture-operation", BindingID: strings.Repeat("c", 32), EpochID: strings.Repeat("d", 32), CheckpointSHA256: strings.Repeat("e", 64), SourceSHA256: strings.Repeat("f", 64), ObservedAt: now, RetainUntil: now.Add(30 * 24 * time.Hour), Admission: "unavailable"}
	raw, err := json.Marshal(result.Success("brine host native_point", view))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeResponse(raw, "native_point"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), `"admission":"unavailable"`, `"admission":"available"`, 1),
		strings.Replace(string(raw), `"point_id":`, `"password":"private","point_id":`, 1),
		strings.Replace(string(raw), `"point_id":`, `"point_id":null,"point_id":`, 1),
	} {
		if _, err := DecodeResponse([]byte(invalid), "native_point"); err == nil {
			t.Fatal("point response accepted unknown fields or effect authority")
		}
	}
}
