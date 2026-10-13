# Managed engine startup

The managed SpacetimeDB service uses one foreground supervisor. These primitives do not admit deployment, first publication, application rollback, or live data restore. P09-04 remains incomplete until the lifecycle and physical gates pass.

## Owned records

Schema 15 adds immutable engine tags beside existing database records. Historical SQLite canonical bytes remain unchanged. `SpacetimeDBBinding` identifies an owned native tree; `ArchiveBinding` identifies its reserved destination and initial epoch. Neither record contains SQLite filenames, catalog hashes, or TXIDs.

`Store.RegisterEngineModule` records the native current-program wire hash, the Wasm SHA-256, and an authoritative canonical schema. Schema markers map immutably to schema content. Several logic revisions may share the same schema marker. `CommitEngineRuntime` requires an owned durable fence and the registered module. `ReadEnginePermit` reads allocation, active incarnation, destination, epoch, fences, runtime, and secret versions in one transaction. `AllowsVerification` authorizes stopped-source inspection only.

## Protected preparation

`Store.AcquireEnginePreparation(ctx, binding, fence, operation)` returns an opaque capability after checking the owned held fence and acquiring the short binding guard. `host.NewEngineSecretMaterial` copies four private byte values. The DTO refuses JSON serialization and redacts formatted output. `host.WriteEngineMaterial` accepts the preparation capability and an authenticated `EngineRuntime`; it verifies every content hash before writing immutable versions.

Files are private, create-only, atomically published, and stored at:

```text
STATE/engine-secrets/BINDING/publisher/vVERSION.secret
STATE/engine-secrets/BINDING/signing-private/vVERSION.secret
STATE/engine-secrets/BINDING/signing-public/vVERSION.secret
STATE/engine-secrets/BINDING/auth-policy/vVERSION.secret
```

The auth policy is exactly `supplied_key_jwt` with `require_client_token=true`. The native data directory must contain `[module-http] enabled=false` in `config.toml`. The qualified public ingress requires client tokens separately. No external OIDC mode or unqualified native auth flag is inferred.

Clear private material and close the preparation capability before `CommitEngineRuntime`, which acquires the same guard. Preparation writes no current runtime record and releases no durable fence. The lifecycle caller owns authorization, intent journaling, resource allocation, and service installation.

## Service and locks

`host.EngineServiceBytes` is the executable service authority. It emits fixed `Type=notify`, `NotifyAccess=main`, and `ExecStart=/usr/local/bin/brine host engine-exec BINDING`. `EngineRuntime.UnitSHA256` is the SHA-256 of those exact bytes. Frontend containers retain their Quadlets. Engine stdout, stderr, and Podman logs are disabled; this service does not provide retained engine logs.

`KillMode=mixed` sends the initial `SIGINT` only to the supervisor. The supervisor owns the typed native stop; systemd can kill the remaining service cgroup at its stop deadline. A nonzero foreground exit remains a failure during a requested stop, and independent native and container absence remains required. Stage cleanup checks the retained directory against its current parent entry before deletion. A replaced stage refuses cleanup and retains both directories and their handles for reconciliation.

For a known binding, a failed foreground attempt records the last refusal at `STATE/engine-refusals/BINDING.json`. The private atomic receipt contains only version 1, a closed stage code, and UTC time. It is at most 4096 bytes, mode 0600, and retained across a later successful start. The best-effort writer uses a one-second context; a diagnostic failure preserves the original refusal. Unknown bindings create no receipt. No raw errors, native logs, paths, tokens, or keys enter the receipt. Unknown disposal or stop writes its code before retaining locks for inspection. These diagnostics grant no startup or cleanup authority and change no production deadline.

Fixed `ExecStopPost=/usr/local/bin/brine host engine-stop BINDING` also runs after a killed or failed-before-ready supervisor. Cleanup requires the current manager control PID, invocation, service cgroup and `stop-post` state, protected unit/runtime bytes and actual native identity. An optional current lifecycle job is checked twice. Cleanup uses the same launch and lifetime locks and exact native binder, then typed stop and pidfd/container absence checks. Already absent owned native state succeeds. A fenced writer or expired startup material can still be stopped; cleanup never grants startup authority or reads publisher/signing values. Unknown effects retain the locks until independently absent or the manager kills the control process. The service stop deadline includes a bounded cleanup margin.

Reviewed preparation creates `STATE/engine-locks/BINDING.launch` and `.lifetime`. Brine never unlinks or replaces these fixed files. Acquisitions and retained capabilities check owner, mode, link count, and current path against the held descriptor. Raw operator replacement or control-state edits remain outside the runtime-agent authority model.

Startup takes the short launch guard, then the lifetime lock. It never takes the global host mutation lock. Fence and engine configuration writers take the short guard before their transaction and release it before stopping writers. Startup releases the short guard only after the actual native handoff. The supervisor retains the lifetime lock until runtime absence. This ordering allows a fence claim to stop an already-running engine without waiting for its lifetime lock first.

## Stopped-source proof

The supervisor authenticates its exact systemd main PID, start ticks, invocation, cgroup, loaded unit, and numeric running start job. Foreign children, descendant cgroups, competing jobs, containers, and orphan source mounts refuse verification.

A bounded rooted census seals member paths, types, modes, owner, device, inode, mount, link count, size, and content. Kernel `openat2` rejects symlinks, magic links, and mount crossings at member-open boundaries. Native source aliases are allowed only when the entire census finds exactly every link to each regular inode, with matching metadata and bytes. An unexplained outside alias refuses. The private copy expands each alias into an independent one-link file. Before and after source seals detect membership and content drift.

Private replay mounts only the copy, immutable signing keys, and the fixed Brine helper. It has a private PID and network namespace, a read-only image, UID and GID 1000, umask 0077, no capabilities, no new privileges, bounded resources, and no systemd notification socket. The helper independently checks its process, network, mounts, and disabled module HTTP configuration. Publisher material enters through bounded private stdin.

The initialized copy withholds only its external `program-bytes`. The hook-free observer checks current database, publisher, names, program, and canonical schema. The helper stops the native child and checks that no other process remains in its PID namespace before returning matching metadata. The outer supervisor independently inspects private container disposal, removes the stage, and rechecks the original source, permit, secret hashes, service, and current job.

This ordinary boot proof establishes current metadata from retained native history. It does not establish full row equality, exact mutation coverage, or acknowledgement durability. Those require the separately qualified passive reader and recovery operations.

## Native handoff and failure

The fixed live command uses the committed image, original data mount, two read-only key mounts, loopback listener, resources, umask, and SIGINT stop signal. Inspection requires exact mounts, executable, argv, image ID, user, security settings, resource limits, native PID, start ticks, pidfd, and the container-ID-specific rootless libpod cgroup. A Podman wrapper PID or successful command start is insufficient.

The supervisor releases the launch guard and sends `READY=1` from its service main process after this handoff. Stop uses `podman stop --time N ID` and its inspected `StopSignal=SIGINT`; the reference Podman has no `stop --signal` option. Container absence and the retained native pidfd must agree before lifetime authority is released. Unknown private disposal, launch, or stop retains the supervisor and its locks while it inspects. A manager-forced supervisor kill can release flock descriptors; the next start still requires actual source and binding absence.

Numeric job, notification, and foreground behavior were observed with a disposable discovery helper. Production supervisor qualification remains a separate physical gate. Mock tests and the discovery helper do not satisfy that gate.

## Protected runner storage

Enrollment installs the root-owned canonical `/etc/brine/runner-storage.conf`
with the overlay driver and `/usr/bin/fuse-overlayfs` mount program. The required
Debian `fuse-overlayfs` package uses the reviewed 1.14 package family. Its
qualified binary reports `fuse-overlayfs: version 1.13-dev`. Package installation
and file ownership follow the existing enrollment journal.

Every Brine runner subprocess selects this fixed profile through
`CONTAINERS_STORAGE_CONF`. Generated Podman services set it in `[Service]`,
not the application's environment. Foreground engine launches use the same
profile. Startup and host inventory refuse missing or changed canonical bytes,
unprotected parents or helper, unavailable `/dev/fuse`, an unqualified helper
version, or a Podman store without dynamic UID mapping. Metadata-only operation
status remains available without opening the runtime.

The runner keeps UID and GID 1000 inside the native container. FUSE maps image
ownership dynamically. Native overlay without ID-mapped mounts instead copies
and changes ownership across the image tree before launching a child. On the
authorized test host, that work exceeded a 300-second diagnostic cap for the
2.54 GB image. A fresh isolated FUSE store borrowed the same image read-only,
verified its index, platform and image ID, and created its container in 0.26
seconds within the unchanged 60-second deadline. That test did not start native
code or prove a successful deployment.

Selecting FUSE marks the store with `.has-mount-program`. Later FUSE layers
can use different whiteout metadata from native overlay. Removing the profile
is not a safe rollback after those layers exist. Existing enrolled hosts need a
separately authorized operator baseline with no runtime consumers, recorded
original state and verified image, layer and source preservation. Replaying an
old enrollment transaction does not install this dependency.

## Namespace-local mount identity

The runner can read a rootless FUSE container's process identity while traversal
through `/proc/PID/root` returns permission denied. A disposable probe confirmed
that both the runner and a root observer saw this refusal, while a fixed
namespace-local metadata program could inspect the exact data and key inodes.
The visible host `MergedDir` was an unmounted underlying directory, so it cannot
stand in for the container's root.

Binding preserves the owned container ID, labels, pinned image, exact mount
list and security policy, process command, executable, PIDfd, start ticks and
cgroup checks. It then executes only `/brine host engine-mount-profile` as
`1000:1000` in that exact container. The helper accepts no arguments or input,
reads only fixed stat and kernel namespace metadata, and starts no native child.
No credential or file content appears in its bounded strict frame.

The frame binds PID 1's start ticks and user and mount namespace identities.
Fixed helper, data, key and optional artifact metadata must match host file
descriptors held through the query. The runner reads the target UID and GID maps
and kernel overflow IDs itself. It translates each protected host owner to the
namespace view, including the overflow identity only when the map leaves that
host owner unmapped. Device, inode, type, mode and mapped owners match exactly.
Regular file size and link count also match. Directory size may change during
ordinary online writes and grants no additional authority.

The runner rechecks descriptors, current protected helper identity, namespaces,
UID and GID maps, process lifetime, command and executable, and current container
metadata before returning a bound handle. Publication and descriptor helpers
already mount the fixed Brine binary read-only. The live engine now includes
that same exact helper mount. Same-namespace native-child guards stay strict;
the passive reader has no host root-traversal guard to replace. Source seals,
material and permit rechecks still precede credential input or native effects.
