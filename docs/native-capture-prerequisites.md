# Native capture prerequisites

Phase 09 remains Approved and incomplete. This page records the original
protected storage, enrollment and passive-reader prerequisite slice. Its local
checks do not complete physical acceptance. Subsequent implementation connects
native capture, preparation, restore tests, publication, live restore and
retirement through the request dispatcher. Use the
[operator runbook](runbooks/spacetimedb.md) and
[backup command contract](native-backup-commands.md) for the current flow.

## Historical prerequisite behavior

`brine restore point POINT_ID --target NAME --kind verified` reads a retained
historical point reference. `--kind prepared` reads a completed exact preparation
reference. The target opens its protected control store read-only. It does not
open the mutation runtime, collect live inventory, start an engine, contact
backup storage or create evidence. Missing, corrupt and incomplete records
refuse. Every returned view reports `admission: unavailable`.

Historical verified points and fresh exact preparation are separate records.
Online persisted history is a lower bound, with later loss unknown. It never
satisfies the exact one-consumer claim used by publication or live restore.
Prepared points retain the original preparation operation and fence owner.
Consumption is a separate immutable row, committed atomically with the effect
claim. An identical consumer is idempotent; a different consumer conflicts.
Generic fence release refuses a consumed preparation even before its transition
exists. Point expiry does not release the fence.

Migration17 adds immutable task inputs, preparation intents, a hash-linked
stop-intent/stopped/source/remote/verified/completed chain, prepared points,
historical verified points and retained bundle records, and the one-consumer
ledger. Existing SQLite records and canonical bytes remain unchanged. Only
operation-owned preparation can create a native intent. The real launch and
lifetime locks must belong to the protected store root and binding. Decoded
records, a matching pathname or a lock from another state root supply no
capability.

Enrollment now plans and installs official restic 0.19.1 for the target
architecture. It authenticates both the compressed release and executable,
stages the binary, checks its exact version and records root-owned files and
dedicated directories for undo. Undo preserves shared `/opt/brine` and refuses
ownership, link, content or unowned-directory drift. No repository or engine is
created or restarted by this enrollment step.

The encrypted transport binds captures to the actual authenticated repository
config ID, separate from the destination hash, snapshot ID and archive manifest.
It reads only the fixed `cat config` command. Initialization retains the actual
ID and authenticates it by readback. An unknown initialization can be inspected
under its retained creation grant; it is never repeated automatically. Existing
repository operations refuse a missing or mismatched ID. Manifest identity and
canonical transport bytes must agree with the stored native source scope.

## Authority and process boundaries

A known clean initialized stop requires an owned native PID/start-ticks/pidfd,
an issued managed stop, the same stopped container with zero exit/non-OOM facts,
and independent manager, process, job and source absence. A nil stop result or
an initially absent writer is not clean-stop evidence. An affirmative
unpublished allocation is a separate arm, with no missing-history fallback.

The durable fence and stop intent precede the manager stop. Preparation releases
its short launch guard while the manager's stop-post handler acquires launch and
lifetime locks. The fence blocks intervening starts. After the actual stop,
preparation reacquires the real locks and rereads the same operation, fence,
runtime and allocation. It never revives a released capability or rewrites the
owner.

The private checkpoint builder binds C's actual stopped source copy and original
root/member seal, an owned prior metadata observation, current runtime and
allocation, and protected material. A structural checkpoint decoder cannot
construct that capability. The recorded immutable signing-key version is still
a recovery dependency to verify; reading a local key is not a restore test.

The passive reader sees only independently copied selected history. It receives
no original source, control configuration, signing material, publisher token,
backup credentials, home or socket. Its namespace, network, capabilities,
read-only root, resource limits, exact image and mount purpose are fixed. It
must stop and be independently disposed before its output can be used. Unknown
reader presence preserves the private view. Cleanup refuses a replacement
directory and preserves both the original and replacement for reconciliation.

Actual Podman qualification found that inspect.Args includes argv0 for the
entrypoint-only image. The launcher now requires the exact singleton form. The
fixed profile also disables automatic writable `/run` and `/var/tmp` mounts and
passwd/group shims, retaining explicit private `/tmp`. The published reader now includes a narrow read-only empty-file mask for the
generated `/run/.containerenv` file. Actual qualification with this pin is
pending. The first two arm64 probes refused containment, preserved their stopped
containers and views, and left every selected input hash unchanged. Those probes
are refusal evidence, not a successful reader or backup drill.

## Verification and required consumers

The protected storage/enrollment/root-guard tree passed `go test ./...`,
`go vet ./...`, the CLI build, focused race tests, formatting and diff checks.
New-code lint passed. Independent review reproduced the replacement-directory
cleanup defect and verified its repair. It also checked the enrollment and
repository-identity source boundaries. Actual official restic 0.19.1 ran in a
private disposable local repository; initialization and encrypted config
readback returned the same ID. That probe was a local process/codec check, not a
protected installation or S3 acceptance test.

The original slice used temporary exact-symbol dependency allowances for the
following consumers. This list records the required composition; it is not a
current allowance inventory or authority grant:

- P05 online capture will consume the rooted metadata census and bounded prefix
  hash/check/copy functions, preserving membership, inode, alias and prefix
  rewrite refusals.
- P05 stopped capture and P06 source description will consume the owned native
  observation, checkpoint, stop and passive view/reader/disposal components.
- P06 publication and P07 live restore will consume the private same-transaction
  prepared-point claim, derived from their actual canonical plans and recorded
  operations. They retain the original fence owner.
- P05 repository admission and remote restore will consume authenticated config
  identity and unknown-initialization inspection under protected scope,
  credential, recovery, ownership and lease checks.

Remove each allowance when its real consumer lands, and delete unused APIs.
No unused caller may conceal missing integration.

At this prerequisite stage, P05 still required the actual source/remote witness and complete persisted-state
comparison, fixed confirmed-row lower-bound/final equality and invariants,
controlled checkpoint and key-recovery retention, repository admission,
credential/recovery delivery and activation, capture/drill handlers, schedules,
CLI health and reconciliation, and the actual authorized Pi/S3 operation drill.
Current source schema reload must derive an opaque schema from authenticated module
bytes through the fixed descriptor purpose. A stored schema hash does not
construct one. Unknown stop, upload, initialization or publication effects remain
fenced or pending until inspection establishes their state.
