/*
Package spec is the pure schema v1 brine.toml boundary. Parse performs no file,
network, subprocess, or host operations. All refusals return a zero App and an
*Error with a stable code, safe field path, and parser-owned message. Decoder
errors are deliberately not wrapped because their context can contain secrets.
Unknown key names are replaced with [unknown]; dynamic map key errors point to
environment or secrets rather than echoing input.
Syntax, duplicate keys, and malformed table shapes use spec.invalid_toml at $.

The required fields are schema_version, name, image, container_port, and a
nonempty domains array. Names are lowercase ASCII DNS labels of 1 to 63 bytes,
with letters or digits at each end and hyphens permitted inside. Domains have
at least two DNS labels and at most 253 bytes. Domain case is folded to lowercase;
IP addresses, trailing dots, wildcards, Unicode, and xn-- IDN labels are refused.
Duplicate domains after case folding are refused.

Images must use an explicit ASCII DNS registry and a lowercase OCI repository,
with @sha256: followed by exactly 64 hexadecimal digits. Hexadecimal case is
folded to lowercase. A registry port and a tag are allowed only alongside the
required digest. The repository path is limited to 255 bytes, excluding the
registry and tag. Implicit registries, IPv6/IP registries, URLs, and credentials
are unsupported. Registry ports are 1 to 65535; container ports are 1024 to
65535. Host ports are not part of the app definition (architecture decision D6).

Absent health settings default to path /, expected status 200, startup deadline
30 seconds, and timeout 3 seconds. Paths must be absolute, clean ASCII HTTP paths
without whitespace, controls, backslashes, percent escapes, query, or fragment.
Statuses are 100 to 599, startup deadlines 1 to 3600 seconds, and timeouts 1 to
300 seconds, not exceeding the startup deadline. If resources is present, both
memory_mb and pids_limit are required positive 32-bit integers. Target policy
can impose tighter limits later; parsing is not authorization.

Environment and secrets use case-sensitive POSIX-style variable names matching
[A-Za-z_][A-Za-z0-9_]* and cannot overlap. Environment values are literal strings;
NUL and ${ expansion are refused. Literal dollar signs and multiline values
remain literal data; runtime adapters must safely encode them without shell or
systemd expansion. Target policy must bound environment sizes before execution.
The parser input limit is not an execution-size guarantee. Secret references match
[A-Za-z0-9][A-Za-z0-9_.-]{0,252}. The parser cannot distinguish a plausible secret
name from a plaintext value with the same spelling. Target policy and reference
resolution must establish that a named Podman secret actually exists (D5).

App and its branded fields are the validated result for downstream domain code,
not another decoding surface. Callers must use Parse for untrusted input rather
than constructing App directly. No CLI commands are wired by this package.
*/
package spec
