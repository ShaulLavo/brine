# Native material delivery

Status: Approved implementation. This command configures an allocated SpacetimeDB
runtime without starting it. Native publication and its separate owner/module
proof remain required before the engine or frontend can serve traffic.

First save and apply a managed preparation plan through `brine data prepare` and
`brine apply`. That operation owns the empty allocation and held data fence. Then
send operator-supplied material through private stdin:

```sh
brine data material set game --preparation-plan "$preparation_plan" \
  --target "$target" --idempotency-key "$delivery_key" --json < private-material.json
```

The packet has exactly five fields:

```json
{
  "publisher": "<supplied ES256 JWT>",
  "signing_private": "<supplied PKCS8 P256 private PEM>",
  "signing_public": "<matching SPKI P256 public PEM>",
  "auth_policy": {"mode": "supplied_key_jwt", "require_client_token": true},
  "publisher_expires_at": "<explicit future UTC lease in RFC3339 form>"
}
```

Keep the input file private. The entire packet is limited to 32 KiB. The host
checks the matching key pair, supplied token signature, closed claim shapes and
lease. The operator lease is required even when the supplied signed JWT has no
`exp` claim; when `exp` is present the lease cannot exceed it. Brine preserves the
supplied token and keys. Neither the token's `hex_identity` claim nor desired
module metadata establishes initialized native ownership.

The request selects only the app, exact saved preparation plan and idempotency
key. The host derives binding, epoch, resource limits, ports, pinned image,
expected module, material references and reserved versions from protected state.
Caller-supplied runtime bodies, paths, owner identities and activation flags are
refused. The current policy, original allocation inode, sole disabled
`config.toml`, original fence and absent manager jobs/engine containers must
still match for the first delivery.

A successful first receipt says `configured_unpublished`. Four immutable private
role files exist, the runtime has empty native database/publisher identities and
an empty name list, and the original preparation fence remains held. No native
process, frontend or ingress is started. Backup credentials and the independent
recovery password can then be delivered and the backup policy activated before
reviewed publication.

A later delivery says `stored_unreferenced`. It validates the current material
and protected runtime, writes new immutable versions and leaves the current
runtime unchanged. A separate reviewed activation is required to use them.

Keep the reported idempotency key. Repeating the same request returns the same
metadata, without repeating a private write. Reusing it for other material,
another app or another preparation plan is refused. A failed or interrupted
write retains its reserved versions and any files and returns
`recovery_required`; the command does not overwrite them or resume unknown
writes. A pending initial delivery prevents another key from bypassing it.

Inspect an operation without changing its files or runtime:

```sh
brine data material status "$operation_id" --target "$target" --json
```

Receipts, plans, operations and events contain only IDs, references, hashes,
versions and lease expiry. They contain no private material or private paths.
Retained unknown files belong to the reported operation and require inspected
recovery; automatic pruning would destroy the evidence needed for that decision.
