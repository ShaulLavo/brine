// Package caddy renders policy-validated sites and publishes owned generations.
// It has no CLI, Caddy admin client, subprocess runner, or enrollment behavior.
// Production callers supply /etc/caddy/brine as the root and the unchanged main
// Caddyfile bytes. The manager never opens or writes that main file. Enrollment
// must create an empty gen-0 directory and current symlink before first use.
// The root needs exactly one standalone top-level physical line:
// import /etc/caddy/brine/current/*.caddy
// The import has one literal space between unquoted tokens, no continuation,
// comment or trailing argument. Leading indentation and CRLF are preserved.
// A Caddy 2.6.2 source-span tokenizer replaces only the argument token. All other
// references to the owned root refuse. Path-like snippet names refuse too.
// Other actual imports (including absolute imports) refuse because an external
// file could declare a path-named snippet that shadows current on real reload,
// yet leaves the generation-path candidate unaffected. We never inspect those
// unowned files. Ordinary response values such as respond "import" still work.
// Caddy environment substitutions refuse because expansion precedes lexing;
// newer-version heredocs and ambiguous quoted standalone braces also refuse.
// The root and its ancestors must not be symlinks.
//
// The caller holds the host mutation lock, checks the main file has not changed,
// and durably journals intent before Apply. Expected State comes from committed
// control state. Its Sites manifest contains NewSite values reconstructed from
// committed desired state; each rendered site must equal its selected file.
// Nonempty hash-only state cannot authorize Apply. Observe leaves Sites unset
// and hashes selected disk files only, not Caddy's loaded
// configuration. A successful result's Next state becomes committed evidence
// only after the operation engine has checked service health and committed it.
// After Validate succeeds, Validator.Adapt returns Caddy's JSON for the same
// candidate. The manager independently checks every expected host's complete
// route and loopback upstream against the pinned renderer/adapter shape, permits
// unrelated operator routes, and refuses missing, duplicate, conditional or
// misdirected sites and old hosts that should be gone (including nested/error
// routes and negated matcher sets). Only HTTP route matcher hosts are examined;
// logging fields and other handler payloads do not authorize or block routing.
// Adaptation errors, timeouts, malformed JSON, and output above 16 MiB
// refuse before switching current. This is candidate evidence, not observation
// of the running service. Different Caddy versions or global directive ordering
// that changes the expected route shape require a reviewed contract change.
// Validator and Reloader must honor their ten-second contexts and bound output.
// Only the real systemd adapter may implement the reload-only command boundary.
//
// Crash recovery depends on the last recorded stage and observed service state.
// Before current-renamed, the old generation stays selected; incomplete staged
// files and an unused candidate may remain. Generation numbers are reserved by
// directory creation, so retries skip orphan numbers instead of overwriting
// them. Each file and generation directory is synced before publication.
// A temporary symlink is renamed over current, then the owned root is synced.
// Between current-renamed and current-synced, a power loss may restore either
// pointer. Both complete generations remain available. After current-synced
// and before a confirmed reload, disk selects the new generation but Caddy may
// still serve the old one. Do not infer service state from the symlink.
//
// A known reload failure restores the old pointer and reloads again with a
// separate bounded context. Failure during rollback leaves both generations
// and reports recovery_required. A timeout or cancellation during either reload
// returns UnknownOutcomeError and unknown_outcome. No further filesystem write,
// rollback, cleanup, or pruning occurs, and that manager rejects further Apply.
// On initial reload timeout current remains new; on rollback reload timeout it
// remains old. Inspect what Caddy actually serves before any repair or retry.
// A fresh manager can inspect disk, but is not proof of completed reconciliation.
// Service observation and durable reconciliation belong to later apply phases.
//
// After confirmed success, pruning retains the new and immediately previous
// generation. A pruning error still returns applied, so callers must not retry
// publication as though reload failed. A crash while pruning only affects older
// inactive generations. Validation and reload failures retain staged evidence.
// Unused temporary symlinks from a crash may remain for later reconciliation.
// Unexpected generation entries refuse pruning instead of recursive deletion.
// The host lock and operator-controlled root protect against competing writes;
// os.Root additionally confines filesystem access even for malformed symlinks.
package caddy
