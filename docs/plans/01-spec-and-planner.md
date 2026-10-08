# Phase 01: App specification and planning

**Depends on:** Phase 00 response and command contracts. **Goal:** reproducible, side-effect-free intent. No Podman/Caddy required to test.

### Tasks

- [ ] **P01-01** Introduce strict, versioned `brine.toml` parser with fixture examples. Reject unknown fields, duplicate keys, bad app names, invalid domain labels, arbitrary paths, unpinned image tags, malformed digests, unsafe ports, invalid health paths, and unsupported options.
- [ ] **P01-02** Normalize desired app configuration and enforce an explicitly supplied target policy: registries, domains, persistent roots, secrets and resources. Reject unsafe environment expansion and shell/Quadlet pass-through.
- [ ] **P01-03** Define stable plan types for create/update/no-op/conflict, ordered changes, expected runtime image/platform and durable immutable plan hashes. Exclude wall-clock timestamps from plan fingerprints; include target generation and policy version.
- [x] **P01-07** Define the versioned target snapshot schema (OS, arch, runtime versions, owned units, Caddy files, used ports, allocated app ports, secret IDs, generation). Offline plans read it, and P02-01 inventory must emit exactly this type. Evidence: `internal/target` strict decoding, canonical round trips for four fixtures, ordering and rejection tests.
- [ ] **P01-04** Add read-only `validate` and `plan --offline` with fixture target snapshots. Offline plans must be conspicuously **not applyable**; never silently treat a fixture as a connected host.
- [ ] **P01-05** Introduce controlled SQLite storage for plans, target IDs and observed generations (with schema migrations and lock tests). Do not store plaintext secrets. Separate control-plane state from app databases.
- [ ] **P01-06** Run property/fuzz tests over names, paths, image digests and canonical ordering. Add readable human diff and deterministic JSON output from one plan model.

### Exit gate

Same configuration + target snapshot + policy produces the same plan hash and change ordering. An invalid or hostile spec never produces an applyable plan. `validate` and offline `plan` do not contact, alter or provision infrastructure.

**Evidence:** T02, T03, T17, T23; fixture snapshot cases for no-op/create/update/conflict; tests prove no adapters capable of mutation are called.
