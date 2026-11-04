# P08-08 fixture publication preparation

Status is prepared source, pending independent review and execution. No UDP
fixture image has been built or published by this change. P08-08 remains open.

## Publication contract

The new `udp-fixture.yml` workflow has only `workflow_dispatch`. Its input is a
full reviewed main SHA and an explicit public-access confirmation. The gate
requires this repository, `refs/heads/main`, the same dispatch/input/checkout
SHA, a clean checkout and the same current remote main SHA. The source must
contain fixture merge `369744357524dc81019afc334a2f9376cf8c87b9`.
There is no source-ref interpolation in a shell command.

Before a builder starts, a read-only gate requires the existing
`ghcr.io/shaullavo/brine-udp-fixture` package to be public, linked to this
repository and anonymously readable. Missing package metadata, private or
unknown visibility, missing linkage, denied anonymous access and missing
confirmation stop the run. First-time public-package setup is not automated.
The normal publication path never creates credentials or changes package visibility.

This follow-up adds a separate `bootstrap_confirmed` dispatch input for the
single approved first publication. Bootstrap accepts only an absent package and
absent run-scoped tags and digests. It records an atomic intent before the first
qualified image push and uses the workflow's `GITHUB_TOKEN` through the existing
GHCR login. It then reads back
the owner package metadata and anonymous registry access. The source and
qualification receipts remain unchanged. The workflow does not call a package
visibility or linkage mutation API: if the token cannot create the exact public
package linked to `ShaulLavo/brine`, the run records the observed outcome and
stops rather than leaving a private substitute or retrying an unknown write.
Each unknown platform push and index write inspects its exact run-scoped tag,
immutable digest, manifest, config digest and platform set before deciding. A
retry occurs only after the exact target is absent and the bootstrap package API
also proves absence. Any unknown package API visibility or linkage state stops
without replay. A pre-existing public package remains on the normal
`public_access_confirmed` path.

Native Linux amd64 and arm64 jobs build the reviewed Dockerfile with no build
network and its existing no-download Go settings. They compile
`p08-08-SOURCE_SHA-a` and `p08-08-SOURCE_SHA-b` into distinct server binaries.
Each image must declare UID and GID 65532, have an entrypoint with mode 0555,
and run its process as that UID and GID. Qualification checks HTTP identity,
nonce echo, wrong-release refusal, explicit client bounds and release B's
HTTP 503 mode without changing its UDP identity. Containers use a read-only
filesystem, dropped capabilities and bounded resources. These Docker checks
are not Brine rootless Podman or external-network acceptance.

Only after both native jobs succeed can the publication job load their exact
SHA-256-bound image archives. It does not rebuild. Only this job has package
write permission and uses the existing pinned login action with `GITHUB_TOKEN`.
It rechecks source and public access before writes. Tags include the source,
release, platform, run and attempt and are publication locators only.
Consumers use the returned `image@sha256:...` references, never those tags.

The publisher records write intent before each platform push and index create.
It reconciles every lost or unknown write before deciding whether the exact
operation succeeded or can be retried. Index readback must contain
exactly the pushed amd64 and arm64 manifests. Anonymous registry reads verify
raw index, platform and config bytes, including their exact digests and byte
bounds. Published configs must match the qualified configs and source/release
labels. Both platforms must also pull by index digest through an isolated,
empty Docker credential configuration. Partial receipts survive a later failure.
A failed run is not a publication success, even if it already pushed an image.

## Pins

| Input | Pin |
| --- | --- |
| Bun | `1.4.4` |
| Go | `1.27.3`, from the reviewed fixture builder |
| Go builder | `docker.io/library/golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b` |
| Buildx | `v0.29.1` |
| BuildKit | `docker.io/moby/buildkit:v0.25.1@sha256:79cc6476ab1a3371c9afd8b44e7c55610057c43e18d9b39b68e2b0c2475cc1b6` |
| Runtime | `scratch`, UID and GID `65532:65532` |
| Fixture dependencies | Standard library only |
| Hosted runners | `ubuntu-24.04` and `ubuntu-24.04-arm`; actual Docker and builder versions enter the baseline receipt |

Checkout, Bun setup, Buildx setup, registry login and artifact upload/download
reuse existing Brine workflow action SHAs. No action follows a mutable tag.
The BuildKit index was resolved read-only through the primary Docker registry;
its retained raw bytes hash to the pinned digest. Existing fixture `pins.json`
remains unchanged and still has no published fixture image value.

## Verification

The focused assertions were run before implementation and failed before the
strict bootstrap and reconciliation helpers existed. The final focused suite
has 18 tests and 141 assertions covering dispatch and source refusals, exact
OCI byte custody, platform/config substitution refusal, public-package gates,
pre-existing bootstrap package and tag rejection, platform and index
reconciliation, the already-public normal path, workflow ordering and the real
CLI against synthetic Git. Its fake Docker and Go commands refuse any
invocation. No local container, Go build, registry write or host deployment
occurs in them.

Local checks use the shared PC runner with owned scratch and teardown:

```sh
bun test tools/ci/udp-fixture
/work/cache/actionlint/1.7.12/actionlint \
  -shellcheck=/home/shaul/.local/share/mise/installs/shellcheck/0.11.0/shellcheck-v0.11.0/shellcheck \
  .github/workflows/udp-fixture.yml
bun /work/projects/platform/node_modules/typescript/bin/tsc \
  --noEmit --ignoreConfig --strict --target esnext --module esnext \
  --moduleResolution bundler --allowImportingTsExtensions --skipLibCheck \
  --typeRoots /work/projects/platform/node_modules/@types,/work/projects/platform/node_modules/.bun/bun-types@1.4.2/node_modules \
  --types bun-types scripts/publish-udp-fixture.ts tools/ci/udp-fixture/*.ts
git diff --check
```

Type checking reuses cached TypeScript 7.0.2 and cached Bun 1.4.2 declarations;
execution uses the pinned Bun 1.4.4. No package installation is required.
The private report includes a rerunnable TypeScript AST nesting check. No code
path exceeds three levels. `go vet ./...` and `go build ./cmd/brine` passed.
`go test ./...` was also attempted, but the existing `internal/host` engine
source-verification tests and one `internal/store` publication-path test failed
before this slice touched any Go or runtime input. No Go file changed here; the
parent owns normal PR CI and independent review.

## Native process-list qualification correction

November 2 runs [41368510216](https://github.com/ShaulLavo/brine/actions/runs/41368510216)
and [41405713283](https://github.com/ShaulLavo/brine/actions/runs/41405713283)
on `e366632386907e89169b8076a481e1b46197a0f0` passed Buildx setup.
Both native architectures then failed in the qualification step with
`docker top failed or timed out` and `Couldn't find PID field in ps output`.
This is a deterministic command error, not evidence of a transient builder failure.

The custom `docker top NAME -eo uid,gid,args` listing omitted PID. Docker's
[Moby process-list parser](https://github.com/moby/moby/blob/v28.0.4/daemon/top_unix.go)
requires the `PID` header to filter host processes to the container's PIDs.
The corrected listing requests `pid,uid,gid,args`. Qualification still requires
one live process with numeric UID and GID 65532 and now also verifies a positive
numeric PID, the expected column order and the exact `/udp-echo-server` command.
Image-config identity, entrypoint bytes and mode, archive custody, tool pins,
no-download settings and publication permissions are unchanged.

The new focused regression exercises the real process-inspection helper through
a synthetic Docker command boundary. Before the fix it failed with the same
PID-field error. After the fix it passes and rejects missing or extra processes,
root or named identities, malformed PIDs, changed commands and swapped headers.
`bun test tools/ci/udp-fixture` passes 20 tests and 175 assertions. These synthetic
checks alone do not establish native container or publication success.

A fresh amd64 Docker 29.7.0 probe imported offline Go 1.27.3 fixture binaries
into disposable scratch images. Starting the server with the production network,
security and resource options did not complete within its 120-second deadline,
so it never reached `docker top`. Teardown verified absence of the owned
containers, images and scratch. The failure's cause is unknown; this probe is
not native qualification evidence. No permission, network policy or tool pin
was changed to make it run. Both native workflow qualifications, publication
and physical Brine acceptance remain separate execution gates.

## Remaining execution gates

After review and merge, a separately authorized operator can dispatch this
workflow on main with that exact reviewed main SHA. This source-preparation
change does not authorize that dispatch. If public-package prerequisites are
not established, stop and report the exact failed gate. Do not publish a private
substitute, change visibility, add a token or select another registry.

There is no physical container, image-index, anonymous-pull, rootless runtime,
public/tailnet packet, lifecycle, rollback, interruption, reboot or removal
proof from this lane. The later Brine drill must use successful publication
receipts and preserve its separate host baselines and teardown. Do not replace
missing proof with builder digests, cross-compilation or HTTP-only evidence.

Qualification receipts and exact archives expire after seven days. Publication
receipts expire after thirty days. Containers, extracted binaries and local
images created by the workflow have explicit cleanup. Pre-existing images are
preserved, the pinned Buildx action owns its builder cleanup, and published
registry artifacts are retained rather than automatically deleted. An unknown
write is reconciled from the saved intent and actual registry state.

## Post-drill retention decision

`post_drill_keep_delete_decision: pending` is the recorded status until the
owner reviews the completed UDP drill. Set it to `keep` when the immutable
fixture remains needed by a follow-up drill, or to `delete` only after all
consumers and retained receipts are gone. This source-only change performs no
registry deletion.
