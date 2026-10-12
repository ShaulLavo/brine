package caddy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

var ErrManagedLoggingUnsafe = errors.New("caddy: managed request logging is not protected")

type managedLog struct {
	Writer  json.RawMessage `json:"writer"`
	Encoder json.RawMessage `json:"encoder"`
	Include []string        `json:"include"`
	Exclude []string        `json:"exclude"`
}

func (log *managedLog) UnmarshalJSON(raw []byte) error {
	fields, err := managedLoggingObject(raw, "writer", "encoder", "include", "exclude", "level", "sampling", "with_caller", "with_stacktrace")
	if err != nil {
		return err
	}
	type plain managedLog
	var value plain
	if json.Unmarshal(raw, &value) != nil || !managedLogNamespaces(fields, "include") || !managedLogNamespaces(fields, "exclude") {
		return ErrManagedLoggingUnsafe
	}
	*log = managedLog(value)
	return nil
}

type managedServerLogs struct {
	DefaultLoggerName    string              `json:"default_logger_name"`
	LoggerNames          map[string][]string `json:"logger_names"`
	SkipHosts            []string            `json:"skip_hosts"`
	SkipUnmappedHosts    bool                `json:"skip_unmapped_hosts"`
	ShouldLogCredentials bool                `json:"should_log_credentials"`
	Trace                bool                `json:"trace"`
}

func (logs *managedServerLogs) UnmarshalJSON(raw []byte) error {
	_, err := managedLoggingObject(raw, "default_logger_name", "logger_names", "skip_hosts", "skip_unmapped_hosts", "should_log_credentials", "trace")
	if err != nil {
		return err
	}
	type plain managedServerLogs
	var value plain
	if json.Unmarshal(raw, &value) != nil {
		return ErrManagedLoggingUnsafe
	}
	for _, names := range value.LoggerNames {
		if len(names) == 0 || len(names) > 128 {
			return ErrManagedLoggingUnsafe
		}
	}
	*logs = managedServerLogs(value)
	return nil
}

func managedLoggingObject(raw []byte, allowed ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, ErrManagedLoggingUnsafe
	}
	for name, value := range fields {
		if !slices.Contains(allowed, name) || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, ErrManagedLoggingUnsafe
		}
	}
	return fields, nil
}

func managedLogNamespaces(fields map[string]json.RawMessage, field string) bool {
	raw, ok := fields[field]
	if !ok {
		return true
	}
	var names []string
	return json.Unmarshal(raw, &names) == nil && !slices.ContainsFunc(names, func(name string) bool { return name == "" || strings.ContainsAny(name, "{}\r\n") })
}

type managedLogConfig struct {
	Logging struct {
		Logs map[string]managedLog `json:"logs"`
	} `json:"logging"`
	Apps struct {
		HTTP struct {
			Servers map[string]struct {
				Routes []json.RawMessage  `json:"routes"`
				Logs   *managedServerLogs `json:"logs"`
			} `json:"servers"`
		} `json:"http"`
	} `json:"apps"`
}

// VerifyManagedLogging reads actual adapted configuration without granting any
// permission to alter protected operator logging or activate a route.
func VerifyManagedLogging(adapted []byte, domains []spec.Domain) error {
	if len(domains) == 0 || len(domains) > 128 || validManagedLogJSON(adapted) != nil {
		return ErrManagedLoggingUnsafe
	}
	var config managedLogConfig
	if json.Unmarshal(adapted, &config) != nil || len(config.Apps.HTTP.Servers) == 0 || len(config.Apps.HTTP.Servers) > 4096 || len(config.Logging.Logs) > 4096 {
		return ErrManagedLoggingUnsafe
	}
	defaultLog, ok := config.Logging.Logs["default"]
	if !ok || !safeManagedLog(defaultLog) {
		return ErrManagedLoggingUnsafe
	}
	seen := map[spec.Domain]bool{}
	for _, server := range config.Apps.HTTP.Servers {
		matched, err := managedRouteDomains(server.Routes, domains)
		if err != nil || len(matched) > 0 && managedLogOverrides(server.Routes, domains) {
			return ErrManagedLoggingUnsafe
		}
		if err := verifyManagedServerLogging(config.Logging.Logs, server.Logs, matched); err != nil {
			return err
		}
		for _, domain := range matched {
			seen[domain] = true
		}
	}
	for _, domain := range domains {
		if domain == "" || !seen[domain] {
			return ErrManagedLoggingUnsafe
		}
	}
	return nil
}

func verifyManagedServerLogging(logs map[string]managedLog, server *managedServerLogs, domains []spec.Domain) error {
	if len(domains) == 0 {
		return nil
	}
	if server != nil && (server.ShouldLogCredentials || server.Trace) {
		return ErrManagedLoggingUnsafe
	}
	if !protectedManagedNamespace(logs, "http.handlers.reverse_proxy") {
		return ErrManagedLoggingUnsafe
	}
	for _, domain := range domains {
		if err := verifyManagedDomainLogging(logs, server, string(domain)); err != nil {
			return err
		}
	}
	return nil
}

func verifyManagedDomainLogging(logs map[string]managedLog, server *managedServerLogs, domain string) error {
	names := []string{""}
	if server != nil {
		names = managedLoggerNames(server, domain)
	}
	if len(names) == 0 || len(names) > 128 {
		return ErrManagedLoggingUnsafe
	}
	for _, name := range names {
		if strings.ContainsAny(name, "{}\r\n") {
			return ErrManagedLoggingUnsafe
		}
		if !protectedManagedNamespace(logs, loggerNamespace("http.log.error", name)) {
			return ErrManagedLoggingUnsafe
		}
		if server != nil && !protectedManagedNamespace(logs, loggerNamespace("http.log.access", name)) {
			return ErrManagedLoggingUnsafe
		}
	}
	return nil
}

func managedLoggerNames(server *managedServerLogs, host string) []string {
	names := []string{server.DefaultLoggerName}
	for key, mapped := range server.LoggerNames {
		if managedLoggerHost(key, host) {
			names = append(names, mapped...)
		}
	}
	return names
}

func managedLoggerHost(key, host string) bool {
	if strings.EqualFold(key, host) {
		return true
	}
	labels := strings.Split(host, ".")
	for n := range labels {
		if labels[n] == "" {
			continue
		}
		labels[n] = "*"
		if strings.EqualFold(key, strings.Join(labels, ".")) {
			return true
		}
	}
	return false
}

func loggerNamespace(base, name string) string {
	if name == "" {
		return base
	}
	return base + "." + name
}

func protectedManagedNamespace(logs map[string]managedLog, namespace string) bool {
	found := false
	for _, log := range logs {
		if !managedLoggerReceives(log, namespace) {
			continue
		}
		if !safeManagedLog(log) {
			return false
		}
		found = true
	}
	return found
}

func managedLoggerReceives(log managedLog, namespace string) bool {
	if slices.Contains(log.Exclude, "*") {
		return false
	}
	accept, reject := 0, 0
	for _, prefix := range log.Include {
		if strings.HasPrefix(namespace+".", prefix+".") {
			accept = max(accept, len(prefix))
		}
	}
	if len(log.Include) > 0 && accept == 0 {
		return false
	}
	for _, prefix := range log.Exclude {
		if strings.HasPrefix(namespace+".", prefix+".") {
			reject = max(reject, len(prefix))
		}
	}
	return accept > reject || len(log.Include) == 0 && reject == 0
}

func safeManagedLog(log managedLog) bool {
	var writer map[string]json.RawMessage
	if len(log.Writer) > 0 && json.Unmarshal(log.Writer, &writer) == nil && len(writer) == 1 && string(writer["output"]) == `"discard"` {
		return true
	}
	encoder, err := strictjson.Object(log.Encoder, "format", "wrap", "fields")
	if err != nil || string(encoder["format"]) != `"filter"` {
		return false
	}
	wrap, err := strictjson.Object(encoder["wrap"], "format")
	if err != nil || string(wrap["format"]) != `"json"` {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoder["fields"], &fields) != nil || len(fields) == 0 {
		return false
	}
	for _, raw := range fields {
		filter, err := strictjson.Object(raw, "filter")
		if err != nil || string(filter["filter"]) != `"delete"` {
			return false
		}
	}
	return fields["request>uri"] != nil
}

func managedRouteDomains(routes []json.RawMessage, domains []spec.Domain) ([]spec.Domain, error) {
	var matched []spec.Domain
	for _, raw := range routes {
		var route adaptedRoute
		if json.Unmarshal(raw, &route) != nil {
			return nil, ErrManagedLoggingUnsafe
		}
		matched = append(matched, managedRouteMatches(route.Match, domains)...)
	}
	return matched, nil
}

func managedRouteMatches(matches []map[string]json.RawMessage, domains []spec.Domain) []spec.Domain {
	if len(matches) == 0 {
		return slices.Clone(domains)
	}
	var matched []spec.Domain
	for _, match := range matches {
		var hosts []string
		if len(match["host"]) == 0 || json.Unmarshal(match["host"], &hosts) != nil {
			return slices.Clone(domains)
		}
		matched = append(matched, matchingManagedDomains(hosts, domains)...)
	}
	return matched
}

func matchingManagedDomains(hosts []string, domains []spec.Domain) []spec.Domain {
	return slices.DeleteFunc(slices.Clone(domains), func(domain spec.Domain) bool {
		return !slices.ContainsFunc(hosts, func(host string) bool { return managedRouteHost(host, string(domain)) })
	})
}

func managedRouteHost(pattern, host string) bool {
	if strings.ContainsAny(pattern, "{}") || strings.EqualFold(pattern, host) {
		return true
	}
	parts, labels := strings.Split(pattern, "."), strings.Split(host, ".")
	if len(parts) != len(labels) {
		return false
	}
	for n, part := range parts {
		if part != "*" && !strings.EqualFold(part, labels[n]) {
			return false
		}
	}
	return true
}

func managedLogOverrides(routes []json.RawMessage, domains []spec.Domain) bool {
	for _, route := range routes {
		var matchedRoute adaptedRoute
		if json.Unmarshal(route, &matchedRoute) != nil {
			return true
		}
		if len(managedRouteMatches(matchedRoute.Match, domains)) == 0 {
			continue
		}
		var value any
		if json.Unmarshal(route, &value) != nil || managedLogOverrideValue(value) {
			return true
		}
	}
	return false
}

func managedLogOverrideValue(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		return managedLogOverrideMap(value)
	case []any:
		return slices.ContainsFunc(value, managedLogOverrideValue)
	default:
		return false
	}
}

func managedLogOverrideMap(value map[string]any) bool {
	if value["handler"] == "log_append" {
		return true
	}
	if _, ok := value["access_logger_names"]; ok {
		return true
	}
	if _, ok := value["log_name"]; ok {
		return true
	}
	for _, item := range value {
		if managedLogOverrideValue(item) {
			return true
		}
	}
	return false
}

func validManagedLogJSON(raw []byte) error {
	if len(raw) == 0 || len(raw) > maxSetBytes || !utf8.Valid(raw) || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return ErrManagedLoggingUnsafe
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	nodes := 0
	if managedLogJSONValue(decoder, 0, &nodes) != nil {
		return ErrManagedLoggingUnsafe
	}
	_, err := decoder.Token()
	if err != io.EOF {
		return ErrManagedLoggingUnsafe
	}
	return nil
}

func managedLogJSONValue(decoder *json.Decoder, depth int, nodes *int) error {
	*nodes++
	if depth > 64 || *nodes > 100_000 {
		return ErrManagedLoggingUnsafe
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == json.Delim('{') {
		return managedLogJSONObject(decoder, depth, nodes)
	}
	if token != json.Delim('[') {
		return nil
	}
	for decoder.More() {
		if managedLogJSONValue(decoder, depth+1, nodes) != nil {
			return ErrManagedLoggingUnsafe
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim(']') {
		return ErrManagedLoggingUnsafe
	}
	return nil
}

func managedLogJSONObject(decoder *json.Decoder, depth int, nodes *int) error {
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return ErrManagedLoggingUnsafe
		}
		seen[name] = true
		if managedLogJSONValue(decoder, depth+1, nodes) != nil {
			return ErrManagedLoggingUnsafe
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return ErrManagedLoggingUnsafe
	}
	return nil
}
