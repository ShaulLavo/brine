# Managed ingress logging prerequisite

SpacetimeDB browser subscriptions carry a temporary credential in the query
string. Caddy logs the full request URI on proxy failures even when access logs
are disabled. The operator must protect the global default logger before a
managed route can be admitted.

For pinned Caddy 2.10.2, use this encoder in the existing global default logger:

~~~caddyfile
{
	log default {
		format filter {
			wrap json
			fields {
				request>uri delete
			}
		}
	}
}
~~~

Keep existing global options and output settings. If the default logger already
exists, update its encoder within that block. Do not add a second global block
or replace unrelated operator configuration. Back up the protected root file
before any operator change, validate the complete configuration, reload through
the existing service workflow, and inspect the actual active configuration.

Every enabled access output for a managed host needs the same encoder. For
example, add the encoder to its existing site log block:

~~~caddyfile
log {
	format filter {
		wrap json
		fields {
			request>uri delete
		}
	}
}
~~~

The default encoder does not protect a separate custom log. Each output that
receives managed error, access or reverse-proxy logs must delete the URI too.
This includes reverse-proxy debug output. Do not enable credential logging or
handler tracing. A log whose output is explicitly `discard` also qualifies.

Deleting only the `token` query parameter does not qualify. Caddy's query filter
returns a URI unchanged when URL parsing fails. A malformed authority such as
`//hostname:bad/...` can therefore expose a token on a denied request. Deleting
the complete `request>uri` field covers literal, encoded and malformed request
targets without relying on query parsing.

`caddy.VerifyManagedLogging` checks complete adapted JSON without changing any
file or service. It requires an explicit protected default logger, checks every
matching error/access/proxy output, and considers host casing, wildcard logger
maps and default fallbacks. Access skip rules do not establish protection.
Dynamic route logger names, unknown filters and ambiguous logging input refuse
with a value-free error. The helper grants no route or deployment authority.

D4 reserves the root Caddyfile for the operator after enrollment. Routine Brine
operations must refuse unsafe logging rather than editing that file. Enrollment
or an explicitly authorized operator change may establish this prerequisite;
this document adds no enrollment mutation. Managed deployment remains gated by
the separate ownership, startup, publication and physical qualification checks.

The optional local regression command is:

~~~sh
BRINE_TEST_CADDY=/absolute/path/to/caddy-2.10.2 \
  go test ./internal/caddy -run '^TestManagedLogging' -count=1
~~~

The probe uses loopback Caddy, unavailable private HTTP upstreams and non-secret
sentinels. It compares admission with actual process/access logs for full-field
deletion, site-only deletion, token-only query filtering, explicit discard and
unprotected shadow outputs. It reaps all test processes. This is local Caddy
evidence, not native authentication, WebSocket or physical-host evidence.
