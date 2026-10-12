package caddy

import (
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/spec"
)

// VerifyProspectiveManagedLogging proves only a conservative prerequisite in
// actual current configuration. It does not fabricate routes or grant apply.
// Every server and route tree is considered because a future candidate may put
// the managed host in any existing server context.
func VerifyProspectiveManagedLogging(adapted []byte, domains []spec.Domain) error {
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
	for _, domain := range domains {
		if domain == "" {
			return ErrManagedLoggingUnsafe
		}
	}
	for _, server := range config.Apps.HTTP.Servers {
		if err := verifyManagedServerLogging(config.Logging.Logs, server.Logs, domains); err != nil {
			return err
		}
		if prospectiveManagedOverrides(server) {
			return ErrManagedLoggingUnsafe
		}
	}
	return nil
}

func prospectiveManagedOverrides(server managedLogServer) bool {
	for _, route := range server.Routes {
		if managedLogRawOverrides(route) {
			return true
		}
	}
	if server.Errors != nil {
		for _, route := range server.Errors.Routes {
			if managedLogRawOverrides(route) {
				return true
			}
		}
	}
	for _, raw := range server.NamedRoutes {
		var routes []json.RawMessage
		if json.Unmarshal(raw, &routes) != nil {
			return true
		}
		for _, route := range routes {
			if managedLogRawOverrides(route) {
				return true
			}
		}
	}
	return false
}
