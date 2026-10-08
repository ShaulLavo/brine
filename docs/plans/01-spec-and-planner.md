# Phase 01: App specification and planning

**Depends on:** Phase 00 response and command contracts. **Goal:** reproducible, side-effect-free intent. No Podman/Caddy required to test.

### Tasks

- [ ] **P01-01** Introduce strict, versioned `brine.toml` parser with fixture examples. Reject unknown fields, duplicate keys, bad app names, invalid domain labels, arbitrary paths, unpinned image tags, malformed digests, unsafe ports, invalid health paths, and unsupported options.
- [ ] **P01-02** Normalize desired app configuration and enforce an explicitly supplied target policy: registries, domains, persistent roots, secret references (D5), app port range (D6) and resources. Reject unsafe environment expansion and shell/Quadlet pass-through.
- [ ] **P01-03** Define stable plan types for create/update/no-op/conflict, ordered changes, expected runtime image/platform and durable immutable plan hashes. Exclude wall-clock timestamps from plan fingerprints; include target generation and policy version.
- [x] **P01-07** Define the versioned target snapshot schema (OS, arch, runtime versions, owned units, Caddy files, used ports, allocated app ports, secret IDs, generation). Offline plans read it, and P02-01 inventory must emit exactly this type. Evidence: `internal/target` strict decoding, canonical round trips for five fixtures, independent Caddy-generation/file-set and passt observations, ordering and rejection tests.
- [ ] **P01-04** Add read-only `validate` and `plan --offline` with fixture target snapshots. Offline plans must be conspicuously **not applyable**; never silently treat a fixture as a connected host.
- [ ] **P01-05** Write offline plans as content-addressed plan files marked not applyable. No control database in this phase; it lives on the target and arrives in P03-01 (D1).
- [ ] **P01-06** Run property/fuzz tests over names, paths, image digests and canonical ordering. Add readable human diff and deterministic JSON output from one plan model.

### Exit gate

Same configuration + target snapshot + policy produces the same plan hash and change ordering. An invalid or hostile spec never produces an applyable plan. `validate` and offline `plan` do not contact, alter or provision infrastructure.

**Evidence:** T02, T03, T17, T23; fixture snapshot cases for no-op/create/update/conflict; tests prove no adapters capable of mutation are called.
