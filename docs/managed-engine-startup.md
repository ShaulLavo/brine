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
