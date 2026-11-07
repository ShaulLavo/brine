package admission

import (
	"encoding/json"
	"strings"
	"testing"
)

func pendingFixture() Pending {
	return Pending{Kind: Update, ID: strings.Repeat("a", 32), Stage: Admitting, Quiescence: Store, Since: "2026-11-06T12:00:00Z"}
}

func TestStateCodec(t *testing.T) {
	for _, s := range []State{{Version: 1, Open: true}, {Version: 1, Pending: ptr(pendingFixture())}} {
		raw, err := Encode(s)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		again, err := Encode(got)
		if err != nil || string(raw) != string(again) {
			t.Fatalf("round trip: %s, %v", again, err)
		}
	}
}

func ptr(p Pending) *Pending { return &p }

func TestCodecRefusesMalformedState(t *testing.T) {
	raw, _ := Encode(State{Version: 1, Pending: ptr(pendingFixture())})
	base := string(raw)
	cases := []string{"", `null`, `{}`, `{"version":1}`, `{"version":null,"open":true}`, `{"version":1,"open":null}`, `{"version":2,"open":true}`, `{"version":1,"open":true,"extra":1}`, `{"version":1,"open":true,"Open":false}`, `{"version":1,"open":true,"open":false}`, `{"version":1,"open":true,"pending":null}`, `{"version":1,"open":false}`, `{"version":1,"open":true} {}`,
		strings.Replace(base, `"id":`, `"unknown":1,"id":`, 1),
		strings.Replace(base, `"kind":"update"`, `"kind":"update","kind":"update"`, 1),
		strings.Replace(base, `"exempt_open":false`, `"exempt_open":null`, 1),
		strings.Replace(base, `"since":"2026-11-06T12:00:00Z"`, `"since":null`, 1),
		strings.Replace(base, `"stage":"admitting"`, `"stage":"surprise"`, 1),
		strings.Replace(base, `"kind":"update"`, `"kind":"surprise"`, 1),
		strings.Replace(base, `"quiescence":"store"`, `"quiescence":"surprise"`, 1),
		strings.Replace(base, strings.Repeat("a", 32), "INVALID", 1),
		strings.Replace(base, `"since":"2026-11-06T12:00:00Z"`, `"since":"yesterday"`, 1),
		strings.Replace(base, `"exempt_open":false`, `"exempt_open":true`, 1),
		strings.Replace(base, `"open":false`, `"open":true`, 1),
	}
	for _, text := range cases {
		t.Run(text, func(t *testing.T) {
			rejectState(t, text)
		})
	}
}

func rejectState(t *testing.T, text string) {
	t.Helper()
	s, err := Decode([]byte(text))
	if err == nil {
		t.Fatalf("accepted %s", text)
	}
	if s.Open || s.Pending != nil {
		t.Fatalf("invalid state returned usable values: %+v", s)
	}
}

func TestRequiredPendingFields(t *testing.T) {
	raw, err := Encode(State{Version: 1, Pending: ptr(pendingFixture())})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	pending := fields["pending"].(map[string]any)
	for _, key := range []string{"kind", "id", "stage", "quiescence", "exempt_open", "since"} {
		rejectPendingField(t, fields, pending, key)
	}
}

func rejectPendingField(t *testing.T, fields, pending map[string]any, key string) {
	t.Helper()
	value := pending[key]
	delete(pending, key)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	rejectState(t, string(raw))
	pending[key] = nil
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	rejectState(t, string(raw))
	pending[key] = value
}

func TestRevokedAttemptIsStillStrictlyDecoded(t *testing.T) {
	p := pendingFixture()
	p.Stage = Migrating
	p.ExemptAttempt = strings.Repeat("b", 32)
	raw, err := Encode(State{Version: 1, Pending: &p})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Decode(raw); err != nil {
		t.Fatal(err)
	}
	rejectState(t, strings.Replace(string(raw), p.ExemptAttempt, "invalid", 1))
}

func TestTimestampGrammarAndBound(t *testing.T) {
	for _, since := range []string{
		"2026-11-06T1:00:00Z", "2026-11-06T12:00:00,5Z",
		"2026-11-06T12:00:00+24:00", "2026-11-06T12:00:00+00:60",
		"2026-11-06T12:00:00." + strings.Repeat("0", MaxStateBytes) + "Z",
	} {
		checkInvalidTimestamp(t, since)
	}
	for _, since := range []string{"2026-11-06T12:00:00Z", "2026-11-06T12:00:00.123456789Z", "2026-11-06T12:00:00+23:59", "2026-11-06T12:00:00.5-02:30"} {
		p := pendingFixture()
		p.Since = since
		raw, err := Encode(State{Version: 1, Pending: &p})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(raw); err != nil {
			t.Fatal(err)
		}
	}
}

func checkInvalidTimestamp(t *testing.T, since string) {
	t.Helper()
	p := pendingFixture()
	p.Since = since
	s := State{Version: 1, Pending: &p}
	if raw, err := Encode(s); err == nil {
		t.Errorf("encoded malformed timestamp, %d bytes", len(raw))
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	rejectState(t, string(raw))
}

func TestStateExactByteBoundary(t *testing.T) {
	raw, err := Encode(State{Version: 1, Open: true})
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte(strings.Repeat(" ", MaxStateBytes-len(raw)))...)
	if _, err := Decode(raw); err != nil {
		t.Fatal(err)
	}
	rejectState(t, string(append(raw, ' ')))
}
