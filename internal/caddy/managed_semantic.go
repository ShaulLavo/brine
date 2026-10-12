package caddy

import (
	"fmt"
	"regexp"
	"strings"
)

// Group labels are adapter-generated identifiers. Their equality and placement
// carry the exclusive-handler behavior; their particular spelling does not.
func canonicalRouteGroups(route map[string]any) {
	groups := map[string]string{}
	canonicalGroupValue(route, groups)
}

func canonicalGroupValue(value any, groups map[string]string) {
	switch node := value.(type) {
	case map[string]any:
		if name, ok := node["group"].(string); ok {
			canonical, present := groups[name]
			if !present {
				canonical = fmt.Sprintf("group%d", len(groups)+1)
				groups[name] = canonical
			}
			node["group"] = canonical
		}
		// Route children are arrays. Visit known children in execution order.
		for _, key := range []string{"handle", "routes"} {
			canonicalGroupValue(node[key], groups)
		}
	case []any:
		for _, child := range node {
			canonicalGroupValue(child, groups)
		}
	}
}

func expectedManagedRoute(site Site) map[string]any {
	route := expectedPlainRoute(site)
	handlers := route["handle"].([]any)[0].(map[string]any)["routes"].([]any)[0].(map[string]any)["handle"].([]any)
	managed := site.managed
	prefix := managed.ingress.prefix
	subscribe := prefix + "v1/database/" + managed.ingress.database + "/subscribe"
	token := prefix + "v1/identity/websocket-token"
	query := "{http.request.orig_uri.query}.matches('^([^%;]|%[0-9a-fA-F]{2})*$')"
	subscribeExpression := fmt.Sprintf("{http.request.orig_uri}.matches('^%s([?].*)?$') && %s && {http.request.header.Upgrade}.lowerAscii() == 'websocket' && {http.request.uri.query.token} != '' && {http.request.orig_uri.query}.split('&').filter(pair, pair.matches('^(t|%%74)(o|%%6[fF])(k|%%6[bB])(e|%%65)(n|%%6[eE])(=|$)')).size() == 1", regexp.QuoteMeta(subscribe), query)
	tokenExpression := fmt.Sprintf("{http.request.orig_uri}.matches('^%s([?].*)?$') && %s && {http.request.header.Authorization}.matches('^Bearer .+$')", regexp.QuoteMeta(token), query)
	proxy := []any{map[string]any{"handler": "rewrite", "strip_path_prefix": strings.TrimSuffix(prefix, "/")}, expectedProxy(uint16(managed.enginePort))}
	routes := []any{
		expectedManagedHandle(map[string]any{"path": []any{subscribe}, "method": []any{"GET"}, "expression": subscribeExpression}, proxy),
		expectedManagedHandle(map[string]any{"path": []any{token}, "method": []any{"POST"}, "expression": tokenExpression}, proxy),
		expectedManagedHandle(map[string]any{"expression": map[string]any{"expr": "{http.request.orig_uri.path}.matches('^/_brine/db(/|$)')", "name": "originalManaged"}}, []any{expectedResponse(403)}),
		expectedManagedHandle(map[string]any{"path": []any{"/_brine/db", "/_brine/db/*"}}, []any{expectedResponse(403)}),
	}
	fallback := expectedResponse(404)
	if managed.appPort != 0 {
		fallback = expectedProxy(uint16(managed.appPort))
	}
	routes = append(routes, expectedManagedHandle(nil, []any{fallback}))
	handlers[2] = map[string]any{"handler": "subroute", "routes": routes}
	return route
}

func expectedProxy(port uint16) map[string]any {
	return map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": fmt.Sprintf("127.0.0.1:%d", port)}}}
}

func expectedResponse(status int) map[string]any {
	return map[string]any{"handler": "static_response", "status_code": float64(status)}
}

func expectedManagedHandle(matcher map[string]any, handlers []any) map[string]any {
	route := map[string]any{"group": "group1", "handle": []any{map[string]any{"handler": "subroute", "routes": []any{map[string]any{"handle": handlers}}}}}
	if matcher != nil {
		route["match"] = []any{matcher}
	}
	return route
}
