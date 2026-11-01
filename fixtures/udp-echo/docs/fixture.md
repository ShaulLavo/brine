# UDP fixture contract

This fixture supplies application evidence for P08-08. It does not change Brine
readiness, deployment or recovery. It has no database, TLS, QUIC, WebTransport,
discovery, authentication, forwarding or arbitrary payload endpoint.

## Server

The image runs as UID and GID 65532. Its filesystem is `scratch`, the empty
filesystem rather than a registry base. The static server needs no writable
path, capabilities, shell, credentials or mounted data.

The release ID is compiled into the binary with `-X main.release=RELEASE`.
Environment cannot replace it. HTTP responses and UDP replies contain that same
ID, so different image releases can be distinguished during update and rollback.

| Setting | Default | Meaning |
| --- | --- | --- |
| `UDP_ECHO_HTTP_ADDR` | `:8080` | HTTP bind inside the container |
| `UDP_ECHO_UDP_ADDR` | `:7777` | UDP bind inside the container |
| `UDP_ECHO_FAIL_HEALTH` | `false` | `true` returns HTTP 503 for the failed-candidate drill |

Bind settings accept IP literals and numeric unprivileged ports. An empty host
binds the container wildcard. Port zero is available for local tests. Brine
still owns the host publication and operator grant, not these fixture settings.

`GET /healthz` returns fixture identity, release ID and `ok`. `GET /stats`
returns that identity plus `received`, `replied`, `rejected`, `oversized` and
`write_errors` counters. Counters reset with the process. No HTTP method mutates
state, and no request accepts a body or query. Other paths return 404.

## Test-only wire format and limits

The request is one nonce datagram containing 1 to 256 ASCII letters, digits,
dots, hyphens or underscores. Generated nonces contain 128 random bits encoded
as 32 lowercase hexadecimal bytes. A valid request receives one canonical JSON
reply with exactly `schema`, `fixture`, `release` and `nonce`, in that order.
Schema is 1 and fixture is `brine-udp-echo`. Invalid packets receive no reply.

| Limit | Maximum |
| --- | --- |
| UDP packet accepted by the reader | 1024 bytes, read into 1025 bytes to detect excess |
| Nonce | 256 token bytes |
| Release ID and client label | 64 token bytes each |
| UDP response | 768 bytes, read into 769 bytes by the client |
| HTTP body read and JSON response | 2048 bytes each |
| Parsed HTTP header accounting | 2048 bytes |
| Go HTTP header parser buffer | 6144 bytes including its fixed 4096-byte allowance |
| Server UDP read and write deadline | 500 ms each |
| HTTP header, body, write and idle deadline | 2 seconds each |
| Server shutdown | 3 seconds, followed by forced connection close |
| Client attempts | 3 |
| Client attempt deadline | 1 ms to 5 seconds, default 1 second |
| Client overall deadline | 1 ms to 15 seconds, default 5 seconds |
| Client JSON result | 2048 bytes including its final newline |

The UDP loop is serial and allocates no per-client sessions or background
workers. HTTP limits apply before either fixture response. Chunked body reads
use the same 2048-byte decoded-body limit and two-second deadline.

## Separate client evidence

The client requires `-endpoint IP:PORT`, `-release EXPECTED` and a `-vantage`
label. IPv6 uses bracketed address syntax. It accepts only a specified unzoned
unicast IP literal and an unprivileged numeric port. It does not resolve DNS,
use ambient proxies or discover destinations. Resolve an approved domain
separately and record that observation if the drill needs it.

The client prints one JSON result with its exact destination, local socket,
caller-supplied network label, nonce, expected and matching release, attempt
count, bounds, start and finish UTC timestamps and outcome. It compares both
the nonce and release. A timeout, refused port, invalid reply or wrong identity
exits nonzero. A canceled context closes its socket promptly. The result never
updates or claims Brine remote readiness. A label describes the operator's
network position; it is not independently authenticated geography.

## Build recipe

Run from the repository root with Go 1.27.3 from `mise.toml`:

```sh
fixtures/udp-echo/build.sh /work/tmp/new-release-a release-a
fixtures/udp-echo/build.sh /work/tmp/new-release-b release-b
```

The script requires a new absolute output directory. It builds CGO-disabled
Linux amd64 and arm64 server and client pairs, strips symbols and build IDs,
removes source paths and VCS state, disables toolchain and dependency downloads,
and writes `SHA256SUMS` and `build.txt`. It leaves successful requested output
for its caller to retain or remove. It removes failed output it created.

Both programs use only the standard library. Root `go.mod` and `go.sum` remain
unchanged. [pins.json](../pins.json) records the exact official Go image index,
its amd64 and arm64 manifests and source revision, resolved through the primary
Docker registry on November 1, 2026. `scratch` has no registry digest.

For an OCI archive with no push or registry write, use the pinned builder:

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  --target udp-echo-server --build-arg RELEASE=release-a \
  --output type=oci,dest=/work/tmp/udp-echo-release-a.oci.tar \
  -f fixtures/udp-echo/Dockerfile .
```

Use `--target udp-echo-client` for the independent client image. The
Dockerfile-specific ignore file limits the context to module metadata and the
fixture. Both image entrypoints have explicit executable mode 0555.
Publication is a separate authorized release step. Until it happens, there is
no deployable immutable fixture image digest. Never invent one or use a mutable
tag in a Brine drill.

## Evidence limits

`integration/udp-delivery/run-local.sh` records its baseline before setup,
installs teardown first and runs unit, real loopback, race and subprocess
checks. Before its first Go command it disables toolchain, proxy and checksum
downloads, including private-module proxy exceptions. Tests, vet and builds use
read-only module mode. It compares repeat cross-builds and distinguishes two
release binaries, retaining their small source/toolchain build receipts.
The subprocess driver verifies native non-root execution, HTTP health, counters,
UDP nonce replies, wrong-release refusal and failed HTTP health. Its cleanup
waits for each child, verifies socket release and removes task binaries.

Cross-compiling arm64 is not physical arm64 execution. Native subprocess tests
are not rootless Podman, Quadlet or Brine deployment evidence. Published index
and platform digests, actual amd64 and arm64 rootless execution, external
public and tailnet packets, update, failed-replacement rollback, interruption,
reboot and removal remain the parent drill's work. That drill must preserve
pre-existing services and data and install its own host teardown before setup.
