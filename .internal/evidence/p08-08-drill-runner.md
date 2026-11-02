# P08-08 drill runner source evidence

Status is source-ready only. This change does not claim P08-08 physical acceptance.

## Scope

The new runner lives under `integration/udp-delivery/brine-drill/`. It wraps the
existing `brine` CLI and the merged UDP echo client with typed command arguments.
It writes only a local JSON ledger and evidence paths. It has no shell-command
input, Podman or systemd cleanup path, reboot path, kernel/module path, policy
writer, DNS/firewall client, registry writer or secret reader.

The source-only checkpoint was replayed onto current main
`e366632386907e89169b8076a481e1b46197a0f0`. The accepted design and task plan
remain the authority for the physical drill.

## Manifest and phase contract

The parent supplies a complete JSON manifest with these fields:

- `source` pins one source commit plus absolute Brine and client binary paths,
  SHA-256 values and matching source commits.
- `fixture` pins the published immutable image index, both platform manifests
  and release image digests. Builder pins are not accepted as fixture image
  pins.
- `target` names `pi` or `vps`, the enrolled Brine target, the private config
  directory and one exact controlled-unreachable IP endpoint.
- `apps` contains exactly two approved allocations for Pi and one for VPS. Each
  entry binds app name, exact domain, release, image digest, bind/client
  addresses, host port, container port and exposure.
- `dependency_receipts` contains existing scrubbed receipt files and exact
  SHA-256 values. Missing or changed receipts refuse before any command runs.
- `phase_receipts` contains parent receipts with `schema`, `run_id`, `target`,
  `phase`, `host_proof` and `reboot_restored`. The after-reboot gate requires
  `reboot_restored=true`.
- `owned_resources` lists every fixture app, endpoint, image, grant and other
  exact resource ID with the current run as owner.
- `phases` contains typed steps. Supported actions are `plan`, `apply`,
  `status`, `lifecycle`, `rollback`, `client-probe`, `inspect`, `resume` and
  `teardown-proof`.

`before-reboot` and `after-reboot` are callable runner phases. `cleanup` only
runs explicit Brine removal steps and then accepts a parent teardown receipt.
`inspect` reads only operation IDs already in the ledger. `resume` inspects
first and resolves only a ledger-owned `recovery_required` operation.

The runner never reboots the Pi or VPS. The parent controller owns reservation,
diagnostic setup, interruption, reboot and restoration. An interruption hook can
name only a ledger operation ID. It cannot carry a PID, unit, shell text or
arbitrary host selector.

## Safety and evidence rules

Every apply and resolve key is derived from `run_id` and step ID and is written
before submission. A lost response leaves `unknown` in the ledger. The runner
refuses a fresh-key replay and requires inspection or reports recovery-required.
Known Brine refusal envelopes are recorded as `rejected`, not as successful or
unknown operations.

Command arguments use `exec.CommandContext` without a shell. Output, deadlines
and read-only attempts are bounded. Dry runs validate the full manifest and
print commands without invoking a subprocess or writing the ledger.

Ready status requires all of the following:

- Brine reports the exact current image, domain, UDP allocation, active
  reservation, healthy HTTP probe, local `ready` and remote `unverified`.
- A separate production proof reports the exact `pasta` mapping and the exact
  bind address and port.
- The proof records `ss -H -lunpe` as its source, an affirmative app, process,
  unit and nonzero inode, confirmed inode gating, no foreign overlap and an
  unchanged route. The manifest binds the proof bytes with `proof_sha256` and
  the proof itself carries the current run ID and target name.

Absence status requires Brine status to contain neither the app nor its exact
UDP endpoint, plus a production proof with no mapping, no socket, confirmed
inode gating, no foreign overlap and an unchanged route. A TCP owner, a process
name, a fabricated `UDPSocket.App`, a weakened inode gate or a client result
cannot satisfy this proof.

Client evidence remains separate. The runner checks the app endpoint or the one
controlled-unreachable endpoint, the exact schema and fixture, release, nonce,
echo response, vantage and bounded timestamps. It never turns client success
into Brine remote readiness.

Cleanup requires a non-empty, ordered action sequence: one or more owned
`remove` lifecycle steps followed by exactly one teardown proof. Empty cleanup,
non-removal actions, proof-before-removal and extra proof steps are refused.
The proof reference carries a SHA-256 in the manifest, is part of the ledger's
manifest identity, and is hashed again when consumed. The receipt must also
carry the current run and target, the exact owned resource set in `absent`
state and `foreign_untouched=true`. A stale or cross-run replacement is
refused. The runner does not remove images, grants, firewall rules, routes or
unrelated services itself.

## Verification

The replay was checked with focused normal and race tests for
`./integration/udp-delivery/brine-drill/...`, including empty-cleanup,
ordered-cleanup, stale-proof-byte and cross-run-proof regressions. It also ran
focused `go vet`, builds of the drill command and `./cmd/brine`, the repository
format check, the structure check and `git diff --check`. These checks validate
the source contract only;
they do not claim a physical drill.

No full Go suite, host suite, store suite, Pi, VPS, image pull or push, network
probe, policy change, firewall change, reboot, kernel/module change, package
installation or secret access ran in this lane. The task-owned scratch trees,
build binary and processes were removed and checked absent.

## Missing live pins

The source cannot provide these physical values yet:

- the published fixture image index and platform digests;
- the release image digests selected for the approved apps;
- the installed Brine and independent client binary hashes on each target;
- the approved exact domain and endpoint receipts for the eventual Pi and VPS
  runs;
- production `ss -H -lunpe` ready and absent proofs;
- reboot restoration, interruption, rollback, removal and actual cleanup receipts.

The parent must fill these values from the authorized physical run. The local
fixture builder pins are not substitutes.
