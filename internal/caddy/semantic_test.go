package caddy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Caddy 2.6.2's adapted multi-domain golden, independent of expectedRoute.
const adaptedGolden = `{"apps":{"http":{"servers":{"srv0":{"listen":[":443"],"routes":[{"match":[{"host":["other.example.net","web.example.com"]}],"handle":[{"handler":"subroute","routes":[{"handle":[{"handler":"headers","response":{"set":{"X-Content-Type-Options":["nosniff"]}}},{"handler":"headers","response":{"set":{"Referrer-Policy":["no-referrer"]}}},{"handler":"reverse_proxy","upstreams":[{"dial":"127.0.0.1:20001"}]}]}]}],"terminal":true}]}}}}}`

func TestAdaptedGoldenAndOperatorRoutes(t *testing.T) {
	next := map[string]Site{"hello.caddy": fixtureSite(t)}
	if err := checkAdapted([]byte(adaptedGolden), next, nil); err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(adaptedGolden), &config); err != nil {
		t.Fatal(err)
	}
	servers := config["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)
	servers["operator"] = map[string]any{"listen": []string{":8080"}, "routes": []any{map[string]any{"handle": []any{map[string]any{"handler": "static_response", "body": "ok"}}}}}
	content, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkAdapted(content, next, nil); err != nil {
		t.Fatal("unrelated operator route refused", err)
	}
}

func TestSemanticAdaptRefusalsBlockPublication(t *testing.T) {
	cases := map[string]string{
		"missing host":      strings.Replace(adaptedGolden, `"other.example.net",`, "", 1),
		"wrong upstream":    strings.Replace(adaptedGolden, "127.0.0.1:20001", "127.0.0.1:20002", 1),
		"remote upstream":   strings.Replace(adaptedGolden, "127.0.0.1:20001", "192.0.2.1:20001", 1),
		"conditional host":  strings.Replace(adaptedGolden, `"match":[{`, `"match":[{"path":["/only"],`, 1),
		"nonterminal route": strings.Replace(adaptedGolden, `"terminal":true`, `"terminal":false`, 1),
		"wrong headers":     strings.Replace(adaptedGolden, "nosniff", "sniff", 1),
		"malformed JSON":    "{",
		"null JSON":         "null",
		"whitespace JSON":   "  ",
		"trailing JSON":     adaptedGolden + "{}",
		"oversized JSON":    strings.Repeat(" ", maxSetBytes) + adaptedGolden,
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(adaptedGolden), &config); err != nil {
		t.Fatal(err)
	}
	server := config["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["srv0"].(map[string]any)
	routes := server["routes"].([]any)
	server["routes"] = append(routes, routes[0])
	duplicate, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	cases["duplicate route"] = string(duplicate)
	server["routes"] = []any{map[string]any{"handle": []any{map[string]any{"handler": "subroute", "routes": routes}}}}
	nested, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	cases["nested host"] = string(nested)
	for name, adapted := range cases {
		t.Run(name, func(t *testing.T) {
			m, root, main, state, v, r := setup(t)
			v.adapted = []byte(adapted)
			result, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
			if err == nil || result.Outcome != Unchanged || result.Stage != "adapt" {
				t.Fatal(result, err)
			}
			requireCurrent(t, root, "gen-0")
			if len(v.paths) != 1 || v.adaptCalls != 1 || r.calls != 0 || v.paths[0] != v.adaptPaths[0] {
				t.Fatal("validate/adapt/publication sequence differs")
			}
		})
	}
}

func TestAdaptErrorsDoNotPublish(t *testing.T) {
	for _, cause := range []error{errors.New("adapter refused"), context.DeadlineExceeded, context.Canceled} {
		m, root, main, state, v, r := setup(t)
		v.adaptErr = cause
		result, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
		if !errors.Is(err, cause) || result.Outcome != Unchanged || result.Stage != "adapt" {
			t.Fatal(result, err)
		}
		requireCurrent(t, root, "gen-0")
		if r.calls != 0 {
			t.Fatal("reload after adapt failure")
		}
	}
	m, _, main, state, v, _ := setup(t)
	v.err = errors.New("invalid candidate")
	if _, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t))); err == nil || v.adaptCalls != 0 {
		t.Fatal("adapt ran before successful validation")
	}
}

func TestStaleAdaptedHostsCannotSurviveUpdateOrRemoval(t *testing.T) {
	for _, remove := range []bool{false, true} {
		m, root, main, state, v, r := setup(t)
		first, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
		if err != nil {
			t.Fatal(err)
		}
		v.adapted = []byte(adaptedGolden)
		change := Remove("hello")
		if !remove {
			app := fixtureApp(t)
			app.Domains = app.Domains[:1]
			app.Domains[0] = "changed.example.com"
			site, err := NewSite(app, fixturePolicy(t), 20003)
			if err != nil {
				t.Fatal(err)
			}
			change = Put(site)
		}
		result, err := m.Apply(context.Background(), main, first.Next, change)
		if err == nil || result.Outcome != Unchanged {
			t.Fatal("old adapted host retained", result, err)
		}
		requireCurrent(t, root, "gen-1")
		if r.calls != 1 {
			t.Fatal("reload after semantic refusal")
		}
	}
	old := map[string]Site{"hello.caddy": fixtureSite(t)}
	for _, json := range []string{adaptedGolden, `{"apps":{"http":{"servers":{"srv0":{"errors":{"routes":[{"match":[{"host":["WEB.EXAMPLE.COM."]}]}]}}}}}}`} {
		if err := checkAdapted([]byte(json), map[string]Site{}, old); err == nil {
			t.Fatal("stale host retained outside ordinary routes")
		}
	}
	if err := checkAdapted([]byte(`{"apps":{"http":{"servers":{}}}}`), nil, old); err != nil {
		t.Fatal("empty removal refused", err)
	}
}

func TestSiteManifestBoundToCommittedGeneration(t *testing.T) {
	for _, kind := range []string{"missing", "wrong site", "wrong filename", "zero"} {
		t.Run(kind, func(t *testing.T) {
			m, root, main, state, v, r := setup(t)
			first, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing":
				first.Next.Sites = nil
			case "wrong site":
				first.Next.Sites["hello.caddy"] = otherSite(t)
			case "wrong filename":
				first.Next.Sites = map[string]Site{"other.caddy": fixtureSite(t)}
			case "zero":
				first.Next.Sites["hello.caddy"] = Site{}
			}
			before := diskImage(t, root)
			if _, err := m.Apply(context.Background(), main, first.Next, Put(otherSite(t))); err == nil {
				t.Fatal("unbound site manifest accepted")
			}
			if !equalDisk(before, diskImage(t, root)) || len(v.paths) != 1 || v.adaptCalls != 1 || r.calls != 1 {
				t.Fatal("manifest refusal changed host")
			}
		})
	}
	m, root, main, state, _, _ := setup(t)
	first, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
	if err != nil {
		t.Fatal(err)
	}
	observed, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if observed.Sites != nil {
		t.Fatal("Observe invented route authorization")
	}
	if _, err := m.Apply(context.Background(), main, observed, Put(otherSite(t))); err == nil {
		t.Fatal("disk hashes alone authorized routes")
	}
	second, err := m.Apply(context.Background(), main, first.Next, Put(otherSite(t)))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Next.Sites) != 1 || len(second.Next.Sites) != 2 {
		t.Fatal("later generation mutated old manifest")
	}
	if _, err := os.Stat(filepath.Join(root, "gen-1/hello.caddy")); err != nil {
		t.Fatal(err)
	}
}

func TestOtherRootImportsRefuse(t *testing.T) {
	m, root, main, state, v, r := setup(t)
	// An external absolute import can declare snippets whose names are Brine paths.
	main = append(main, []byte("import /operator/*.caddy\n")...)
	before := diskImage(t, root)
	if _, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t))); err == nil {
		t.Fatal("uninspectable external import accepted")
	}
	if !equalDisk(before, diskImage(t, root)) || len(v.paths) != 0 || r.calls != 0 {
		t.Fatal("external import refusal touched host")
	}
}

func TestAdaptContextCancellationDoesNotPublish(t *testing.T) {
	m, root, main, state, v, r := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var atCancel map[string]string
	v.adaptFn = func(ctx context.Context, _ string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > commandTimeout {
			t.Fatal("adaptation has no bounded context")
		}
		atCancel = diskImage(t, root)
		cancel()
		return []byte(adaptedGolden), nil
	}
	result, err := m.Apply(ctx, main, state, Put(fixtureSite(t)))
	if !errors.Is(err, context.Canceled) || result.Outcome != Unchanged {
		t.Fatal(result, err)
	}
	requireCurrent(t, root, "gen-0")
	if !equalDisk(atCancel, diskImage(t, root)) || r.calls != 0 {
		t.Fatal("writes after canceled adaptation")
	}
}

func TestDuplicateExpectedHostsRefuse(t *testing.T) {
	first := fixtureSite(t)
	app := fixtureApp(t)
	app.Name = "second"
	second, err := NewSite(app, fixturePolicy(t), 20002)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkAdapted([]byte(adaptedGolden), map[string]Site{"hello.caddy": first, "second.caddy": second}, nil); err == nil {
		t.Fatal("duplicate desired host accepted")
	}
}

func FuzzAdaptedJSON(f *testing.F) {
	f.Add([]byte(adaptedGolden))
	f.Add([]byte(`{"apps":{"http":{"servers":{}}}}`))
	f.Add([]byte("  "))
	f.Add([]byte(`{"host":null}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		site := fixtureSite(t)
		// Arbitrary adapter output must never panic or authorize a missing site.
		_ = checkAdapted(data, map[string]Site{"hello.caddy": site}, nil)
	})
}

func TestLoggingHostFieldDoesNotBlockPublication(t *testing.T) {
	m, root, main, state, v, r := setup(t)
	// Independently adapted by Caddy 2.6.2 from the renderer golden plus the
	// operator's logging filter. No production expectation builder is used.
	adapted, err := os.ReadFile("testdata/logging.json")
	if err != nil {
		t.Fatal(err)
	}
	v.adapted = adapted
	main = append([]byte("{\n log default {\n  format filter {\n   wrap json\n   fields {\n    host delete\n   }\n  }\n }\n}\n"), main...)
	result, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
	if err != nil || result.Outcome != Applied {
		t.Fatal("unrelated logging host field blocked publication", result, err)
	}
	requireCurrent(t, root, "gen-1")
	if r.calls != 1 || v.adaptCalls != 1 {
		t.Fatal("route was not published")
	}
}

func TestHandlerHostHeaderIsNotRoutingMatcher(t *testing.T) {
	var config map[string]any
	if err := json.Unmarshal([]byte(adaptedGolden), &config); err != nil {
		t.Fatal(err)
	}
	servers := config["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)
	servers["operator"] = map[string]any{"routes": []any{map[string]any{"handle": []any{map[string]any{
		"handler": "static_response", "body": "ok", "headers": map[string]any{"host": []string{"web.example.com"}},
	}}}}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkAdapted(data, map[string]Site{"hello.caddy": fixtureSite(t)}, nil); err != nil {
		t.Fatal("handler payload treated as matcher", err)
	}
}

func TestStaleHostsInRouteMatcherStructuresRefuse(t *testing.T) {
	old := map[string]Site{"hello.caddy": fixtureSite(t)}
	cases := []string{
		`{"routes":[{"match":[{"not":[{"host":["web.example.com"]}]}]}]}`,
		`{"routes":[{"match":[{"not":[{"not":[{"host":["web.example.com"]}]}]}]}]}`,
		`{"routes":[{"handle":[{"handler":"subroute","routes":[{"match":[{"not":[{"host":["web.example.com"]}]}]}]}]}]}`,
		`{"errors":{"routes":[{"match":[{"not":[{"host":["WEB.EXAMPLE.COM."]}]}]}]}}`,
		`{"errors":{"routes":[{"handle":[{"handler":"subroute","routes":[{"match":[{"host":["web.example.com"]}]}]}]}]}}`,
		`{"routes":[{"handle":[{"handler":"subroute","errors":{"routes":[{"match":[{"host":["web.example.com"]}]}]}}]}]}`,
	}
	for _, server := range cases {
		data := []byte(`{"apps":{"http":{"servers":{"srv0":` + server + `}}}}`)
		if err := checkAdapted(data, nil, old); err == nil {
			t.Fatal("stale matcher host retained", server)
		}
	}
}
