# Brine UDP drill runner

This runner is the source-only controller for the P08-08 physical drill. It
wraps the existing `brine` CLI and the merged UDP echo client. It does not
reboot hosts, install modules, edit policy, touch firewalls or remove images.

The parent controller supplies a complete JSON manifest. The manifest pins the
Brine and client binaries, their matching source commit and SHA-256, the
published fixture image index and platform digests, release image digests,
approved app/domain/endpoints, target kind, exact owned resources, dependency
receipts, and phase receipts. A Pi manifest must contain two apps. A VPS
manifest must contain one app.

Build and validate the source with:

```sh
go run ./integration/udp-delivery/brine-drill/cmd \
  --manifest /absolute/run/manifest.json \
  --ledger /absolute/run/ledger.json \
  --phase before-reboot \
  --dry-run
```

The runner exposes these phases:

- `before-reboot` validates the parent's pre-reboot receipt, then runs the
  manifest's plan, apply, status, lifecycle, rollback and client steps.
- `after-reboot` requires the parent's restoration receipt, then runs status or
  exact operation inspection steps.
- `cleanup` runs only Brine-owned removal and validates a parent teardown proof.
- `inspect` reads only ledger-owned operation IDs.
- `resume` inspects first, then resolves only a ledger-owned
  `recovery_required` operation with a persisted deterministic key.

Each mutation gets a key derived from the run ID and step ID. The key is saved
before `brine apply` or `brine resolve` starts. A lost response marks the
ledger entry `unknown`; a later phase must inspect it. The runner never creates
a fresh key for that step.

Ready proofs require Brine's full production status and a separate receipt
from the actual `ss -H -lunpe` ownership observer. The receipt must include the
exact app, inode, unit, bind address, port, `pasta` mapping, HTTP health and
unexplained-socket checks. A TCP owner, a process name, a fabricated app field
or an independent client result cannot satisfy it. Absence proofs require the
same production observer to confirm no mapping and no socket. Client results
stay separate and report their exact endpoint, release, nonce, vantage and
bounded attempt window.

The parent owns Pi reservation, diagnostic setup, reboot, restoration, policy
and image teardown, and any privileged interruption. Interruption and recovery
steps may name only operation IDs already in the local ledger. No runner step
accepts a shell command, unit name, PID, firewall rule or arbitrary cleanup
selector.
