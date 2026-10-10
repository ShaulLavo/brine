package backupcredentials

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestPacketRedactsEveryFormattingBoundary(t *testing.T) {
	p, err := DecodePacket([]byte(`{"access_key_id":"PLANTED_ACCESS","secret_access_key":"PLANTED_SECRET","session_token":"PLANTED_SESSION"}`))
	if err != nil {
		t.Fatal(err)
	}
	assertPrivate := func(t *testing.T, output string) {
		t.Helper()
		if strings.Contains(output, "PLANTED") {
			t.Fatal("private packet escaped through generic formatting")
		}
	}
	for _, verb := range []string{"%s", "%v", "%+v", "%#v", "%d", "%x", "%X", "%q", "%f", "%e", "%g", "%b", "%o", "%c", "%t", "%w", "%020d", "%.2f"} {
		t.Run(verb, func(t *testing.T) {
			assertPrivate(t, fmt.Sprintf(verb, p))
			assertPrivate(t, fmt.Sprintf(verb, &p))
		})
	}
	// Dynamic format strings exercise invalid %w without triggering go vet's
	// static error-interface diagnostic: private packets must never be errors.
	format := "wrapped: %w"
	wrapped := fmt.Errorf(format, p)
	assertPrivate(t, wrapped.Error())
	assertPrivate(t, errors.Join(wrapped, fmt.Errorf(format, &p)).Error())
	if _, ok := any(p).(error); ok {
		t.Fatal("packet must not implement error")
	}
	if _, ok := any(&p).(error); ok {
		t.Fatal("packet pointer must not implement error")
	}
	serialized, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivate(t, string(serialized))
	var binary bytes.Buffer
	// Gob may refuse this opaque type; neither its bytes nor error may expose
	// credential values, and it must not gain access to the private payload.
	if err := gob.NewEncoder(&binary).Encode(p); err != nil {
		assertPrivate(t, err.Error())
	}
	assertPrivate(t, binary.String())
	for _, jsonHandler := range []bool{false, true} {
		var output bytes.Buffer
		var handler slog.Handler = slog.NewTextHandler(&output, nil)
		if jsonHandler {
			handler = slog.NewJSONHandler(&output, nil)
		}
		slog.New(handler).Info("packet", "value", p, "pointer", &p, "wrapped", wrapped)
		assertPrivate(t, output.String())
	}
}
