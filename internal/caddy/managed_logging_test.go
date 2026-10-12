package caddy

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/spec"
)

func loggingFixture(t testing.TB) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/managed-logging-protected.json")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func fixtureLogger(value map[string]any, name string) map[string]any {
	return value["logging"].(map[string]any)["logs"].(map[string]any)[name].(map[string]any)
}

func fixtureServer(value map[string]any) map[string]any {
	return value["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["srv0"].(map[string]any)
}

func verifyLoggingFixture(t testing.TB, value map[string]any) error {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return VerifyManagedLogging(raw, []spec.Domain{"127.0.0.1"})
}

func TestManagedLoggingActualAdaptedFixtures(t *testing.T) {
	if err := verifyLoggingFixture(t, loggingFixture(t)); err != nil {
		t.Fatal("protected actual Caddy fixture refused", err)
	}
	raw, err := os.ReadFile("testdata/managed-logging-unprotected.json")
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(VerifyManagedLogging(raw, []spec.Domain{"other.example.net", "web.example.com"}), ErrManagedLoggingUnsafe) {
		t.Fatal("actual implicit default logger admitted")
	}
}

func TestManagedLoggingDefaultAndAccessRefusals(t *testing.T) {
	cases := map[string]func(map[string]any){
		"missing default":    func(v map[string]any) { delete(v["logging"].(map[string]any)["logs"].(map[string]any), "default") },
		"default plain JSON": func(v map[string]any) { fixtureLogger(v, "default")["encoder"] = map[string]any{"format": "json"} },
		"access plain JSON":  func(v map[string]any) { fixtureLogger(v, "log0")["encoder"] = map[string]any{"format": "json"} },
		"credential logging": func(v map[string]any) { fixtureServer(v)["logs"].(map[string]any)["should_log_credentials"] = true },
		"handler tracing":    func(v map[string]any) { fixtureServer(v)["logs"].(map[string]any)["trace"] = true },
		"unknown wrapper": func(v map[string]any) {
			fixtureLogger(v, "default")["encoder"].(map[string]any)["wrap"] = map[string]any{"format": "console"}
		},
		"unknown encoder option": func(v map[string]any) { fixtureLogger(v, "default")["encoder"].(map[string]any)["future"] = true },
		"query-only filter": func(v map[string]any) {
			fixtureLogger(v, "default")["encoder"].(map[string]any)["fields"] = map[string]any{"request>uri": map[string]any{"filter": "query", "actions": []any{map[string]any{"type": "delete", "parameter": "token"}}}}
		},
		"shadow error logger": func(v map[string]any) {
			v["logging"].(map[string]any)["logs"].(map[string]any)["shadow"] = map[string]any{"include": []string{"http.log.error"}, "encoder": map[string]any{"format": "json"}}
		},
		"shadow access logger": func(v map[string]any) {
			v["logging"].(map[string]any)["logs"].(map[string]any)["shadow"] = map[string]any{"include": []string{"http.log.access.log0"}, "encoder": map[string]any{"format": "json"}}
		},
		"shadow reverse proxy debug logger": func(v map[string]any) {
			v["logging"].(map[string]any)["logs"].(map[string]any)["shadow"] = map[string]any{"include": []string{"http.handlers.reverse_proxy"}, "level": "DEBUG", "encoder": map[string]any{"format": "json"}}
		},
		"logger alias":           func(v map[string]any) { fixtureLogger(v, "default")["Encoder"] = map[string]any{"format": "json"} },
		"unknown logging option": func(v map[string]any) { fixtureLogger(v, "default")["unknown"] = true },
		"null logger selector":   func(v map[string]any) { fixtureLogger(v, "default")["include"] = nil },
		"server logs alias":      func(v map[string]any) { fixtureServer(v)["logs"].(map[string]any)["Trace"] = false },
		"empty mapped names": func(v map[string]any) {
			fixtureServer(v)["logs"].(map[string]any)["logger_names"] = map[string]any{"127.0.0.1": []string{}}
		},
		"missing mapped logger": func(v map[string]any) { delete(v["logging"].(map[string]any)["logs"].(map[string]any), "log0") },
		"dynamic names": func(v map[string]any) {
			fixtureServer(v)["logs"].(map[string]any)["logger_names"] = map[string]any{"127.0.0.1": []string{"{host}"}}
		},
		"route logger override": func(v map[string]any) {
			fixtureServer(v)["routes"] = []any{map[string]any{"handle": []any{map[string]any{"handler": "vars", "access_logger_names": []string{"other"}}}}}
		},
		"route extra log field": func(v map[string]any) {
			fixtureServer(v)["routes"] = []any{map[string]any{"handle": []any{map[string]any{"handler": "log_append", "key": "copied_uri", "value": "{http.request.orig_uri}"}}}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) { checkLoggingRefusal(t, mutate) })
	}
}

func TestManagedLoggingUnrelatedRouteDoesNotOverrideManagedLogging(t *testing.T) {
	value := loggingFixture(t)
	routes := fixtureServer(value)["routes"].([]any)
	fixtureServer(value)["routes"] = append(routes, map[string]any{
		"match":  []any{map[string]any{"host": []string{"unrelated.example.com"}}},
		"handle": []any{map[string]any{"handler": "log_append", "key": "path", "value": "{http.request.orig_uri}"}},
	})
	if err := verifyLoggingFixture(t, value); err != nil {
		t.Fatal("unrelated host route blocked managed logging", err)
	}
}

func TestManagedLoggingNamespacePrecedence(t *testing.T) {
	cases := []struct {
		include, exclude []string
		want             bool
	}{
		{nil, nil, true},
		{[]string{"http"}, nil, true},
		{[]string{"http.log.access.log"}, nil, false},
		{nil, []string{"http.log.access"}, false},
		{[]string{"http.log.access.log0"}, []string{"http"}, true},
		{[]string{"http"}, []string{"http.log.access.log0"}, false},
		{[]string{"http.log.access.log0"}, []string{"*"}, false},
		{nil, []string{"."}, true},
		{[]string{"*"}, nil, false},
	}
	for _, test := range cases {
		if got := managedLoggerReceives(managedLog{Include: test.include, Exclude: test.exclude}, "http.log.access.log0"); got != test.want {
			t.Fatalf("include=%v exclude=%v got=%t want=%t", test.include, test.exclude, got, test.want)
		}
	}
}

func TestManagedLoggingHostVariantsAndWildcards(t *testing.T) {
	value := loggingFixture(t)
	fixtureServer(value)["routes"] = []any{map[string]any{"match": []any{map[string]any{"host": []string{"*.EXAMPLE.com"}}}}}
	logs := fixtureServer(value)["logs"].(map[string]any)
	logs["logger_names"] = map[string]any{"*.example.com": []string{"log0"}}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyManagedLogging(raw, []spec.Domain{"web.example.com"}); err != nil {
		t.Fatal("protected wildcard host refused", err)
	}
	logs["logger_names"].(map[string]any)["WEB.example.com"] = []string{"shadow"}
	value["logging"].(map[string]any)["logs"].(map[string]any)["shadow"] = map[string]any{"include": []string{"http.log.access.shadow"}, "encoder": map[string]any{"format": "json"}}
	raw, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(VerifyManagedLogging(raw, []spec.Domain{"web.example.com"}), ErrManagedLoggingUnsafe) {
		t.Fatal("host casing shadow admitted")
	}
}

func TestManagedLoggingSkippedAccessStillChecksFallback(t *testing.T) {
	value := loggingFixture(t)
	logs := fixtureServer(value)["logs"].(map[string]any)
	logs["skip_hosts"] = []string{"127.0.0.1"}
	fixtureLogger(value, "default")["exclude"] = []string{"http.log.access"}
	if !errors.Is(verifyLoggingFixture(t, value), ErrManagedLoggingUnsafe) {
		t.Fatal("unprotected fallback namespace admitted")
	}
}

func TestManagedLoggingDecodedOverrideAndDuplicateKeys(t *testing.T) {
	value := loggingFixture(t)
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), `"handler":"rewrite"`, `"handler":"rewrite","access_logger_\u006eames":["shadow"]`, 1))
	if !errors.Is(VerifyManagedLogging(raw, []spec.Domain{"127.0.0.1"}), ErrManagedLoggingUnsafe) {
		t.Fatal("encoded dynamic override admitted")
	}
	for _, raw := range []string{`{"logging":{},"\u006cogging":{}}`, `{"logging":{"logs":{"default":{"encoder":{},"encoder":{}}}}}`} {
		if !errors.Is(VerifyManagedLogging([]byte(raw), []spec.Domain{"example.com"}), ErrManagedLoggingUnsafe) {
			t.Fatal("duplicate or encoded duplicate key admitted")
		}
	}
}

func checkLoggingRefusal(t *testing.T, mutate func(map[string]any)) {
	t.Helper()
	value := loggingFixture(t)
	mutate(value)
	if !errors.Is(verifyLoggingFixture(t, value), ErrManagedLoggingUnsafe) {
		t.Fatal("unsafe or ambiguous logging admitted")
	}
}

func TestManagedLoggingUnrelatedAndDiscardOutputs(t *testing.T) {
	value := loggingFixture(t)
	value["logging"].(map[string]any)["logs"].(map[string]any)["unrelated"] = map[string]any{"include": []string{"admin.api"}, "encoder": map[string]any{"format": "json"}}
	if err := verifyLoggingFixture(t, value); err != nil {
		t.Fatal("unrelated logger blocked managed admission", err)
	}
	fixtureLogger(value, "log0")["writer"] = map[string]any{"output": "discard"}
	delete(fixtureLogger(value, "log0"), "encoder")
	if err := verifyLoggingFixture(t, value); err != nil {
		t.Fatal("unambiguous discarded access output refused", err)
	}
}

func TestManagedLoggingMalformedAndBoundedInput(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", "{} {}", `{"logging":{},"logging":{}}`, `{"logging":{"logs":null}}`, strings.Repeat(" ", maxSetBytes+1)} {
		if err := VerifyManagedLogging([]byte(raw), []spec.Domain{"example.com"}); !errors.Is(err, ErrManagedLoggingUnsafe) {
			t.Fatal("malformed logging admitted")
		}
	}
	value := loggingFixture(t)
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(VerifyManagedLogging(raw, nil), ErrManagedLoggingUnsafe) || !errors.Is(VerifyManagedLogging(raw, []spec.Domain{"missing.example.com"}), ErrManagedLoggingUnsafe) {
		t.Fatal("missing managed identity admitted")
	}
	if strings.Contains(ErrManagedLoggingUnsafe.Error(), "NON_SECRET") {
		t.Fatal("logging refusal exposed values")
	}
}
