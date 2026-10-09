package caddy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
)

// Routing expectations come from policy-validated control state, independently
// of both the root lexer and Caddy's adapter. Hashes alone cannot bind routing.
func boundSites(state State, files map[string][]byte) (map[string]Site, error) {
	if len(state.Sites) != len(files) {
		return nil, errors.New("caddy: committed site manifest required")
	}
	for name, site := range state.Sites {
		content, err := Render(site)
		if err != nil || string(site.name)+".caddy" != name || !bytes.Equal(content, files[name]) {
			return nil, errors.New("caddy: committed site manifest differs from files")
		}
	}
	sites := maps.Clone(state.Sites)
	if sites == nil {
		sites = map[string]Site{}
	}
	return sites, nil
}

func expectedRoute(site Site) map[string]any {
	hosts := make([]any, len(site.domains))
	for i, domain := range site.domains {
		hosts[i] = string(domain)
	}
	return map[string]any{
		"match": []any{map[string]any{"host": hosts}},
		"handle": []any{map[string]any{"handler": "subroute", "routes": []any{map[string]any{
			"handle": []any{
				map[string]any{"handler": "headers", "response": map[string]any{"set": map[string]any{"X-Content-Type-Options": []any{"nosniff"}}}},
				map[string]any{"handler": "headers", "response": map[string]any{"set": map[string]any{"Referrer-Policy": []any{"no-referrer"}}}},
				map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": fmt.Sprintf("127.0.0.1:%d", site.port)}}},
			},
		}}}},
		"terminal": true,
	}
}

type adaptedConfig struct {
	Apps struct {
		HTTP struct {
			Servers map[string]struct {
				Routes []json.RawMessage `json:"routes"`
				Errors struct {
					Routes []json.RawMessage `json:"routes"`
				} `json:"errors"`
			} `json:"servers"`
		} `json:"http"`
	} `json:"apps"`
}
type adaptedRoute struct {
	Match  []map[string]json.RawMessage `json:"match"`
	Handle []struct {
		Handler string          `json:"handler"`
		Routes  json.RawMessage `json:"routes"`
		Errors  json.RawMessage `json:"errors"`
	} `json:"handle"`
}

// This checks the pinned adapter's complete host routes, including handlers and
// upstreams, rather than merely finding host names somewhere in a JSON tree.
func checkAdapted(data []byte, next, previous map[string]Site) error {
	refused := errors.New("caddy: adapted routes differ from expected Brine sites")
	if len(data) == 0 || len(data) > maxSetBytes || (len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{') {
		return refused
	}
	var config adaptedConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return refused
	}
	protected := map[string]bool{}
	owners := map[string]string{}
	expected := map[string]map[string]any{}
	for _, site := range previous {
		for _, domain := range site.domains {
			protected[string(domain)] = true
		}
	}
	for file, site := range next {
		if site.name == "" {
			return refused
		}
		expected[file] = expectedRoute(site)
		for _, domain := range site.domains {
			host := string(domain)
			if _, exists := owners[host]; exists {
				return refused
			}
			owners[host] = file
			protected[host] = true
		}
	}
	seen := map[string]bool{}
	var checkRoute func(json.RawMessage, bool) error
	checkRoute = func(raw json.RawMessage, top bool) error {
		var route adaptedRoute
		if err := json.Unmarshal(raw, &route); err != nil {
			return refused
		}
		file := ""
		// Only HTTP route matcher sets define routing hosts. Negation embeds
		// more matcher sets; logging and handler payloads are not matchers.
		var checkMatcher func(map[string]json.RawMessage) error
		checkMatcher = func(matcher map[string]json.RawMessage) error {
			if hostsRaw, ok := matcher["host"]; ok {
				var hosts []string
				if err := json.Unmarshal(hostsRaw, &hosts); err != nil {
					return refused
				}
				for _, value := range hosts {
					host := strings.ToLower(strings.TrimSuffix(value, "."))
					if !protected[host] {
						continue
					}
					owner, present := owners[host]
					if !present || !top || (file != "" && file != owner) {
						return refused
					}
					file = owner
				}
			}
			if negatedRaw, ok := matcher["not"]; ok {
				var negated []map[string]json.RawMessage
				if err := json.Unmarshal(negatedRaw, &negated); err != nil {
					return refused
				}
				for _, nested := range negated {
					if err := checkMatcher(nested); err != nil {
						return err
					}
				}
			}
			return nil
		}
		for _, matcher := range route.Match {
			if err := checkMatcher(matcher); err != nil {
				return err
			}
		}

		if file != "" {
			var actual map[string]any
			if err := json.Unmarshal(raw, &actual); err != nil || seen[file] || !reflect.DeepEqual(actual, expected[file]) {
				return refused
			}
			seen[file] = true
		}
		for _, handler := range route.Handle {
			if handler.Handler != "subroute" {
				continue
			}
			var nestedRoutes []json.RawMessage
			if len(handler.Routes) > 0 {
				if err := json.Unmarshal(handler.Routes, &nestedRoutes); err != nil {
					return refused
				}
			}
			for _, nested := range nestedRoutes {
				if err := checkRoute(nested, false); err != nil {
					return err
				}
			}
			if len(handler.Errors) > 0 {
				var errorRoutes struct {
					Routes []json.RawMessage `json:"routes"`
				}
				if err := json.Unmarshal(handler.Errors, &errorRoutes); err != nil {
					return refused
				}
				for _, nested := range errorRoutes.Routes {
					if err := checkRoute(nested, false); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	for _, server := range config.Apps.HTTP.Servers {
		for _, route := range server.Routes {
			if err := checkRoute(route, true); err != nil {
				return err
			}
		}
		for _, route := range server.Errors.Routes {
			if err := checkRoute(route, false); err != nil {
				return err
			}
		}
	}
	if len(seen) != len(next) {
		return refused
	}
	return nil
}
