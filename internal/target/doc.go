// Package target defines schema version 1 for offline snapshot files and live
// inventory responses. It performs no I/O or host operations.
//
// Identity, OS, and architecture are required. Identity binds a stable opaque
// target ID to an OpenSSH SHA256 host-key fingerprint, without network addresses.
// OS ID and version use os-release values; architecture uses Go/OCI names.
// Inventory that cannot determine those identity or platform facts must fail,
// rather than fabricate a compatible host. Unsupported platforms retain their
// actual OS and architecture strings. Decode and Encode preserve them; Validate
// returns an UnsupportedError unless the host is Debian 13 on amd64 or arm64.
//
// Every observation field is required. Its status is known, unknown, unsupported,
// or absent. Known carries a non-null value, including false, zero, or an empty
// array. Unknown means not measured; unsupported means the collector cannot
// measure the fact. Neither carries a value. Absent means a measured optional
// resource does not exist, and is allowed only for software versions, runner
// user, current release, image, allocated port, and the Caddy config set. This lets
// fresh hosts differ from incomplete inventories without inventing zero values.
// Empty app, unit, secret, port, and Caddy file sets use known with [], not null.
//
// Generation is an unsigned 64-bit counter; known zero is a fresh host. Its
// monotonicity across snapshots belongs to the host state store. FreeDiskBytes
// measures the filesystem backing the runner's state and app data, or the
// intended backing filesystem before enrollment. Versions include systemd, Podman,
// passt, Caddy, and Litestream, retaining exact observed
// strings. Validate does not check dependency version compatibility or readiness.
// Planners must refuse unsafe unknown observations and check required capabilities.
//
// Apps include only Brine-owned resources. Quadlet names identify source files,
// not generated systemd units. Quadlet and Caddy hashes are sha256:<64 lowercase
// hex digits> of exact file bytes. Images use the same digest syntax and bind a
// linux/amd64 or linux/arm64 platform. Secret records contain only names and IDs.
// A secret Name is the actual Podman name, not the app-spec logical reference.
// Plans bind immutable brine-<app>-<ref>-v<n> names and their IDs. Generic or legacy
// names remain observable for drift diagnosis; the planner must refuse to resolve
// an applyable plan to an unversioned name.
// Allocated ports survive stopped apps, so they need not appear in UsedPorts.
// UsedPorts is the deduplicated union of bound TCP and UDP host port numbers
// across all users and addresses, including non-Brine processes and IPv6. It
// over-reserves ports bound on other addresses. Conflict classification and image
// platform matching belong to the planner, not snapshot validation.
//
// CaddyConfig is the observed config set selected by /etc/caddy/brine/current.
// Its generation is the number in that symlink's gen-<n> destination, independent
// of the control-state Generation counter. Its Files enumerate every .caddy file
// in that directory by basename and exact file-byte hash, including extra or stale
// files with no recorded app. No per-app Caddy hash duplicates this source of truth.
// Absent means no current config set exists; unknown means inventory could not
// inspect it. Unsupported can describe a layout the collector cannot interpret.
// This filesystem observation does not prove Caddy loaded the selected config
// after a reload timeout; runtime reconciliation must inspect what it serves.
//
// Snapshot assignment shares observation pointers and slice backing arrays.
// Known retains its input, including slice storage. Consumers own this mutable
// data and must not treat value assignment as a frozen planning copy. Encode
// sorts a deep copy without modifying the caller. Decode creates independent data.
//
// Encode defines Brine's canonical JSON, not RFC 8785. It preserves struct field
// order, sorts apps, units, secrets, and Caddy files by name, and ports numerically,
// uses encoding/json string escaping, and adds one trailing newline. Observation
// values are absent from unobserved fields. Callers hashing a snapshot use Encode,
// not json.Marshal. Duplicate set members, duplicate object keys, unknown or
// differently cased fields, missing fields, nulls, and trailing JSON are rejected.
package target
