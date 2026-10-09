package logs

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
)

type inventory struct{ snapshot target.Snapshot }

func (i inventory) Collect(context.Context) (target.Snapshot, error) { return i.snapshot, nil }

type executor struct {
	commands []localexec.Command
	output   localexec.Result
	err      error
}

func (e *executor) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	e.commands = append(e.commands, c)
	return e.output, e.err
}
func owned(unit string) inventory {
	return inventory{target.Snapshot{Apps: target.Known([]target.App{{Name: "api", QuadletUnits: target.Known([]target.Unit{{Name: unit}})}})}}
}

const journal = `{"__REALTIME_TIMESTAMP":"1791542008366482","PRIORITY":"6","MESSAGE":"ready"}` + "\n"

func TestReaderArgv(t *testing.T) {
	for _, unit := range []string{"api.container", "brine-api.container"} {
		e := &executor{output: localexec.Result{Stdout: journal}}
		r := Reader{Inventory: owned(unit), Executor: e}
		lines, err := r.Read(context.Background(), Request{App: "api", Tail: 5, Since: "2026-10-01T00:00:00Z"})
		if err != nil || len(lines) != 1 || lines[0].Message != "ready" || lines[0].Priority != 6 || lines[0].Timestamp != "2026-10-09T10:33:28.366482Z" {
			t.Fatalf("%+v %v", lines, err)
		}
		c := e.commands[0]
		want := []string{"--user", "-u", strings.TrimSuffix(unit, ".container") + ".service", "-n", "5", "-o", "json", "--no-pager", "--all", "--since", "2026-10-01T00:00:00Z"}
		if c.Path != "journalctl" || !reflect.DeepEqual(c.Args, want) || c.Timeout != ReadTimeout || c.Mutation {
			t.Fatalf("%+v", c)
		}
	}
}
func TestRefusals(t *testing.T) {
	for _, tt := range []struct {
		request Request
		inv     inventory
		code    result.Code
	}{
		{Request{App: "other", Tail: 5}, owned("api.container"), result.LogsOwnershipRefused},
		{Request{App: "api", Tail: 5}, owned("unrelated.container"), result.LogsOwnershipRefused},
		{Request{App: "api", Tail: 5}, inventory{}, result.LogsOwnershipRefused},
		{Request{App: "api;id", Tail: 5}, owned("api.container"), result.InvalidUsage},
		{Request{App: "../api", Tail: 5}, owned("api.container"), result.InvalidUsage},
		{Request{App: "api.service", Tail: 5}, owned("api.container"), result.InvalidUsage},
		{Request{App: "api", Tail: 1001}, owned("api.container"), result.InvalidUsage},
		{Request{App: "api", Tail: 0}, owned("api.container"), result.InvalidUsage},
		{Request{App: "api", Tail: 5, Since: "--file=/etc/shadow"}, owned("api.container"), result.InvalidUsage},
	} {
		e := &executor{}
		_, err := (Reader{Inventory: tt.inv, Executor: e}).Read(context.Background(), tt.request)
		if err == nil || result.Classify(err).Code() != tt.code || len(e.commands) != 0 {
			t.Fatalf("%+v %v calls=%d", tt.request, err, len(e.commands))
		}
	}
}
func TestCapsAndMalformed(t *testing.T) {
	for _, output := range []localexec.Result{
		{Stdout: strings.Repeat("x", MaxBytes+1)}, {Stdout: journal, Truncated: true}, {Stdout: strings.Repeat(journal, 6)},
		{Stdout: `{ "__REALTIME_TIMESTAMP":"9223372036854775807", "PRIORITY":"6", "MESSAGE":"secret" }`},
		{Stdout: journal + "{bad"}, {Stdout: `{"MESSAGE":"secret"}`}, {Stdout: `{"__REALTIME_TIMESTAMP":"0","PRIORITY":"99","MESSAGE":"secret"}`},
	} {
		lines, err := (Reader{Inventory: owned("api.container"), Executor: &executor{output: output}}).Read(context.Background(), Request{App: "api", Tail: 5})
		if err == nil || lines != nil {
			t.Fatalf("accepted bad output: %v", err)
		}
	}
}
func TestDebianFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/debian13.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines, err := parse(string(data), 5)
	if err != nil || len(lines) != 1 || lines[0].Priority != 6 {
		t.Fatalf("%+v %v", lines, err)
	}
}
func TestRedaction(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"API_KEY=planted-secret-value\n-----BEGIN PRIVATE KEY-----\nabc", "API_KEY=[REDACTED]\n[REDACTED]"},
		{"\x1b(Bready", "ready"},
		{"\x1b]0;window title\x07ready", "ready"},
		{"Bearer short-secret", "Bearer [REDACTED]"},
		{"https://user:short-secret@example.test", "https://user:[REDACTED]@example.test"},
		{"ready", "ready"}, {"\x1b[31mready\x1b[0m", "ready"},
		{"API_KEY=short-secret OK=yes", "API_KEY=[REDACTED] OK=yes"},
		{`password="words with spaces"`, `password=[REDACTED]`},
		{"Authorization: Bearer short-secret", "Authorization: [REDACTED]"},
		{"token: abcdef", "token: [REDACTED]"},
		{strings.Repeat("a", 64), "[REDACTED]"},
		{"-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----", "[REDACTED]"},
	} {
		var r redactor
		if got := r.clean(tt.input); got != tt.want {
			t.Fatalf("%q -> %q want %q", tt.input, got, tt.want)
		}
	}
	var r redactor
	for _, s := range []string{"-----BEGIN RSA PRIVATE KEY-----", "short-private-content", "-----END RSA PRIVATE KEY-----"} {
		if got := r.clean(s); got != "[REDACTED]" {
			t.Fatalf("block leaked %q", got)
		}
	}
}
func TestBinaryMessage(t *testing.T) {
	raw := `{"__REALTIME_TIMESTAMP":"1791542008366482","PRIORITY":"6","MESSAGE":[65,80,73,95,75,69,89,61,120]}`
	lines, err := parse(raw, 1)
	if err != nil || lines[0].Message != "API_KEY=[REDACTED]" {
		t.Fatalf("%+v %v", lines, err)
	}
}
func FuzzPlantedSecrets(f *testing.F) {
	f.Add("plain text")
	f.Add("\x1b[31m")
	f.Add("-----BEGIN PRIVATE KEY-----")
	f.Fuzz(func(t *testing.T, prefix string) {
		if len(prefix) > 4096 {
			t.Skip()
		}
		const secret = "planted-secret-value"
		input := prefix + "\nAPI_KEY=" + secret + "\nAuthorization: Bearer " + secret + "\n" + strings.Repeat("abcdef01", 8)
		var r redactor
		got := r.clean(input)
		if strings.Contains(got, secret) || strings.Contains(got, strings.Repeat("abcdef01", 8)) {
			t.Fatalf("planted secret survived")
		}
		if _, err := json.Marshal(got); err != nil {
			t.Fatal(err)
		}
	})
}

func TestHardTailBoundary(t *testing.T) {
	lines, err := parse(strings.Repeat(journal, MaxTail), MaxTail)
	if err != nil || len(lines) != MaxTail {
		t.Fatalf("count=%d error=%v", len(lines), err)
	}
	_, err = parse(strings.Repeat(journal, MaxTail+1), MaxTail)
	if result.Classify(err).Code() != result.LogsLimitExceeded {
		t.Fatal(err)
	}
}
func TestRedactionExpansionCap(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"__REALTIME_TIMESTAMP": "1791542008366482", "PRIORITY": "6", "MESSAGE": strings.Repeat("API_KEY=x ", 11000)})
	if len(raw) > MaxBytes {
		t.Fatal("fixture exceeds input cap")
	}
	lines, err := parse(string(raw), 1)
	if lines != nil || result.Classify(err).Code() != result.LogsLimitExceeded {
		t.Fatalf("error=%v", err)
	}
}
func TestAmbiguousOwnership(t *testing.T) {
	inv := owned("api.container")
	*(*inv.snapshot.Apps.Value)[0].QuadletUnits.Value = append(*(*inv.snapshot.Apps.Value)[0].QuadletUnits.Value, target.Unit{Name: "brine-api.container"})
	e := &executor{}
	_, err := (Reader{Inventory: inv, Executor: e}).Read(context.Background(), Request{App: "api", Tail: 5})
	if result.Classify(err).Code() != result.LogsOwnershipRefused || len(e.commands) != 0 {
		t.Fatalf("%v", err)
	}
}
func TestSubprocessFailureDoesNotPublish(t *testing.T) {
	for _, err := range []error{&localexec.Error{Kind: localexec.Timeout}, &localexec.Error{Kind: localexec.Failed}, context.Canceled} {
		e := &executor{output: localexec.Result{Stdout: journal + "API_KEY=planted-secret", Stderr: "Bearer planted-secret"}, err: err}
		lines, got := (Reader{Inventory: owned("api.container"), Executor: e}).Read(context.Background(), Request{App: "api", Tail: 5})
		if got == nil || lines != nil || strings.Contains(got.Error(), "planted-secret") {
			t.Fatal("subprocess failure leaked output")
		}
	}
}
func TestDecodeLinesBoundary(t *testing.T) {
	for _, raw := range []string{`null`, `[null]`, `[{"timestamp":"2026-10-09T00:00:00Z","priority":6,"message":null}]`, `[{"timestamp":"2026-10-09T00:00:00Z","priority":6,"message":"ready","extra":"secret"}]`, `[{"Timestamp":"2026-10-09T00:00:00Z","priority":6,"message":"ready"}]`, `[{"timestamp":"2026-10-09T00:00:00Z","priority":6,"priority":7,"message":"ready"}]`} {
		if _, err := DecodeLines([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestEscapedQuotedSecrets(t *testing.T) {
	for _, message := range []string{
		`{"password":"prefix\"words short-secret-suffix"}`,
		`{"password":"prefix\\\" words short-secret-suffix"}`,
		`password='prefix\' words short-secret-suffix'`,
		`{"password":"prefix` + string(rune(92)) + `u0022 words short-secret-suffix"}`,
	} {
		var r redactor
		got := r.clean(message)
		for range 3 {
			got = r.clean(got)
		}
		if strings.Contains(got, "short-secret-suffix") || strings.Contains(got, "prefix") {
			t.Fatalf("escaped secret leaked: %q", got)
		}
	}
}
func FuzzEscapedQuotedSecrets(f *testing.F) {
	f.Add("prefix", `\"`)
	f.Add(`\\`, "words")
	f.Fuzz(func(t *testing.T, prefix, escaped string) {
		if len(prefix)+len(escaped) > 4096 {
			t.Skip()
		}
		const suffix = "planted-short-secret-suffix"
		value, _ := json.Marshal(prefix + escaped + `" words ` + suffix)
		raw := `{"password":` + string(value) + `}`
		var r redactor
		got := r.clean(raw)
		for range 3 {
			got = r.clean(got)
		}
		if strings.Contains(got, suffix) {
			t.Fatal("escaped JSON secret survived")
		}
	})
}
func TestFullJournalMessages(t *testing.T) {
	for _, tt := range []struct {
		message string
		want    result.Code
	}{
		{strings.Repeat("ordinary log words. ", 300), ""},
		{strings.Repeat("ordinary log words. ", 10000), result.LogsLimitExceeded},
	} {
		raw, _ := json.Marshal(map[string]string{"__REALTIME_TIMESTAMP": "1791542008366482", "PRIORITY": "6", "MESSAGE": tt.message})
		e := &executor{output: localexec.Result{Stdout: string(raw)}}
		lines, err := (Reader{Inventory: owned("api.container"), Executor: e}).Read(context.Background(), Request{App: "api", Tail: 1})
		if tt.want == "" {
			if err != nil || len(lines) != 1 || lines[0].Message != tt.message {
				t.Fatal("long message was lost")
			}
		} else if err == nil || lines != nil || result.Classify(err).Code() != tt.want {
			t.Fatalf("oversize: %v", err)
		}
		if !strings.Contains(strings.Join(e.commands[0].Args, " "), "--all") {
			t.Fatal("journalctl must request full messages")
		}
	}
	for _, message := range []string{`null`, ""} {
		raw := `{"__REALTIME_TIMESTAMP":"1791542008366482","PRIORITY":"6"`
		if message != "" {
			raw += `,"MESSAGE":` + message
		}
		raw += `}`
		if lines, err := parse(raw, 1); err == nil || lines != nil {
			t.Fatal("omitted/null message accepted")
		}
	}
}
func TestStrictSince(t *testing.T) {
	for _, since := range []string{"2026-10-09T1:00:00Z", "2026-10-09T00:00:00,123Z", "2026-10-09T00:00:00+24:60", "2026-10-09T00:00:00+24:00", "2026-10-09T00:00:00+01:60"} {
		e := &executor{output: localexec.Result{Stdout: journal}}
		_, err := (Reader{Inventory: owned("api.container"), Executor: e}).Read(context.Background(), Request{App: "api", Tail: 1, Since: since})
		if err == nil || result.Classify(err).Code() != result.InvalidUsage || len(e.commands) != 0 {
			t.Fatalf("accepted invalid since %q", since)
		}
	}
	e := &executor{output: localexec.Result{Stdout: journal}}
	_, err := (Reader{Inventory: owned("api.container"), Executor: e}).Read(context.Background(), Request{App: "api", Tail: 1, Since: "2026-10-09T02:00:00.123456+02:00"})
	if err != nil {
		t.Fatal(err)
	}
	args := e.commands[0].Args
	if args[len(args)-1] != "2026-10-09T00:00:00.123456Z" {
		t.Fatalf("non-normalized since: %q", args[len(args)-1])
	}
}

func TestTruncatedCaptureMarker(t *testing.T) {
	e := &executor{output: localexec.Result{Stdout: journal, Truncated: true}}
	lines, err := (Reader{Inventory: owned("api.container"), Executor: e}).Read(context.Background(), Request{App: "api", Tail: 1})
	if lines != nil || err == nil || result.Classify(err).Code() != result.LogsTruncated {
		t.Fatalf("missing explicit truncated refusal: %v", err)
	}
}
