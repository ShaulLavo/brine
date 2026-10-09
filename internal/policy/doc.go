/*
Package policy is the pure operator-policy boundary for schema v1. Parse decodes
at most 1 MiB of TOML with exact, case-sensitive field names. Unknown fields,
duplicates, wrong types, invalid values and unsupported schema versions produce
*Refusal diagnostics containing only package-owned text. Category returns 4,
the machine contract's refused-by-policy exit category.

The required fields are schema_version = 1, version (an operator revision token),
and resources.memory_mb and resources.pids_limit (positive 32-bit ceilings).
Omitted app_ports defaults to min = 20000 and max = 20999. An explicit range
requires both endpoints, from 1024 through 65535 inclusive. Normalize retains
this range but does not allocate a host port; observed ports and stable recorded
allocations belong to P01-03 and the host operation engine.

minimum_free_disk_bytes is an optional positive integer in bytes. Omission
resolves to 1073741824 (1 GiB). Explicit zero, negative, noninteger and out-of-range
TOML integers are refused. The canonical policy and normalized Desired retain the
resolved minimum. Desired.CanonicalBytes also resolves a zero Go field to this
default without mutating its input. The planner requires known free disk at or
above the minimum and hashes that decision and observation status, not the raw
measurement. This is a reserve floor, not an estimate of image or release size.

allowed_registries is an array of tables with host and optional
repository_prefixes. Hosts are exact ASCII DNS names with optional ports;
case is folded, but a missing port does not match a rule with an explicit port.
Prefixes match the exact repository or descendants separated by a slash.
An omitted or empty prefix set permits no repositories on that host. There is
no host-wide authorization opt-in in schema v1. Missing or empty registry and
domain sets deny every image and domain, respectively. Registry and repository
aliases are not expanded; docker.io/library/alpine and docker.io/alpine remain
distinct references and must each match an explicitly approved prefix.

allowed_domains contains exact names or *.suffix rules. Case is folded after
rejecting non-ASCII input. Wildcards permit one or more subdomain labels, never
the suffix apex or a partial-label suffix. Unicode and xn-- labels are refused,
matching the spec boundary. This version does not implement IDN conversion.

allowed_secrets is a table mapping app names to sets of logical secret reference
names. Missing entries permit no secrets for that app. Reference case matters.
The package never resolves secret values or immutable Podman secret versions.
Those are host state under D5, not desired config.

persistent_roots is a set of clean, absolute, non-root ASCII paths. Empty means
no approved persistence roots. Policy stores and hashes them for later phases;
the v1 app spec has no persistence paths to authorize. The future persistence
adapter must check containment, symlinks and file ownership on the host.

Normalize revalidates public spec values through the existing strict parser.
Every refusal returns a zero Desired. Missing app resources resolve to the
operator ceilings, so omission never requests unlimited memory or processes.
Health defaults are resolved only by spec.Parse; Normalize preserves all four
health fields and refuses invalid zero or empty values. Domains, environment
and secrets are sorted; output collections never alias input maps or slices. Desired contains
literal environment settings and secret references, never resolved secrets.
CanonicalBytes is deterministic JSON with explicit defaults and no timestamps.
Desired is not a complete plan fingerprint; P01-03 must also bind target platform
and the resolved image/platform identity, as well as the observed target state.
Policy.Hash is sha256 over canonical policy JSON, including operator revision,
resource ceilings, roots and defaults. Allowed sets are sorted and deduplicated;
duplicate registry hosts are refused rather than merged ambiguously.

The host dispatcher must supply policy loaded from an operator-owned path that
the runner cannot modify. Pure Parse cannot attest file ownership or provenance.
App requests must never supply their own policy. This package does no file,
network, subprocess or host access and grants no host mutation authority.
A complete operator-format fixture is in testdata/operator.toml.
*/
package policy
