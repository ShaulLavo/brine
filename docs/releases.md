# Release binaries

Brine is experimental. The release pipeline is source-only until a reviewed
GitHub dry run succeeds and the owner approves the platform matrix and first
public tag. Adding this pipeline does not publish a release or authorize an
installation. T24 and full release authenticity remain unverified until the dry
run and first real release have evidence.

## Proposed artifact matrix

| Artifact | Use |
| --- | --- |
| `brine-linux-amd64` | Linux amd64 client and enrolled host binary |
| `brine-linux-arm64` | Linux arm64 client and enrolled host binary |
| `brine-darwin-amd64` | macOS Intel client only |
| `brine-darwin-arm64` | macOS Apple Silicon client only |

Cross-compilation is not runtime support evidence. macOS binaries do not provide
the Linux host runtime. They have no Apple signing or notarization. Confirming
this as the public supported matrix remains an owner decision.

Every build uses the exact Go version in `mise.toml`, `CGO_ENABLED=0`,
`-trimpath`, stripped debug sections, read-only modules and required VCS stamping. The helper verifies the
executable format, architecture, source commit, clean VCS flag and commit time in
each binary. It checks the native Linux binary's reported CLI version. A bounded
`receipt.json` records source commit and tree, version, candidate tag, Go version,
and each artifact's SHA-256 and byte size. `SHA256SUMS` covers the four binaries
and the receipt. No archive timestamps or build-directory paths enter the receipt.

## Run a local candidate build

Use a clean Linux amd64 checkout, including no ignored files. Install the pinned
Go and Bun versions from `mise.toml`. Set cache and temporary paths outside the
checkout. On the development PC these belong under `/work`.

```sh
export GOTOOLCHAIN=local
export GOCACHE=/work/cache/go-build
export GOMODCACHE=/work/cache/go/pkg/mod
export TMPDIR=/work/tmp
export BRINE_RELEASE_TAG=v0.1.74-dev
bash scripts/build-release.sh /work/tmp/brine-release-first
bash scripts/build-release.sh /work/tmp/brine-release-second
bun scripts/release.ts compare /work/tmp/brine-release-first /work/tmp/brine-release-second
```

Both output directories must be absent and their parent must exist. Use a
candidate tag equal to `v` followed by the checked-out `cli.Version`. Local
builds do not create or require a Git tag. Compilation errors, source movement and
interruption remove the helper's partial output. The helper does not delete an
existing directory. Remove successful build directories when their consumer is
done. Shared Go caches may remain.

## Run the reviewed workflow

After the source PR merges with review and green CI, dispatch `release.yml` on
`main` with a candidate `tag` matching `cli.Version`. This is build-only. A manual
dispatch cannot enter publication, request an OIDC token or publish attestations.
The workflow requires the exact source commit on `main` and its latest main-push
CI run to have completed successfully. A pending, failed or unknown CI observation
refuses the run.

`build` and `rebuild` use separate fresh GitHub runners, no restored Go build
cache, the same pinned toolchain and the same commit. `compare` checks every
file and receipt against the source, then requires identical `SHA256SUMS`.
Artifacts are retained for three days. A local comparison does not prove that
these independent GitHub builds agree.

Only an owner-approved stable version tag push can enter `publish`. The tag must
identify the workflow's exact source commit on green `main`, and `cli.Version`
must equal the tag without `v`. A feature PR must not bump the version. All
external actions are pinned by commit. Only publication receives contents,
attestation and OIDC write permissions. Before attesting and again before
release creation, it rechecks source, tag, CI, artifact bytes and release absence.
An existing release or an unknown absence check refuses publication. Release
creation is attempted once without asset overwrite, release edits or retries.
If creation times out or partially uploads assets, inspect that release before
any further action. Do not delete or overwrite it to make a rerun pass.

## Verify a downloaded release

Download the desired binary, `receipt.json` and `SHA256SUMS` from the same release.
Verify the attestations before trusting the checksums. For example, for Linux
amd64:

```sh
gh attestation verify brine-linux-amd64 --repo ShaulLavo/brine
gh attestation verify receipt.json --repo ShaulLavo/brine
gh attestation verify SHA256SUMS --repo ShaulLavo/brine
sha256sum --ignore-missing -c SHA256SUMS
```

Check that the verified provenance names `.github/workflows/release.yml`, the
intended version tag and its exact source commit. Check that `receipt.json`
records that same commit, version and binary hash. To check the complete release,
download all four binaries and run `sha256sum -c SHA256SUMS`. Attestations use
GitHub OIDC, not a held signing key. A matching checksum alone is not proof of
who built the file. Verification does not install or execute a binary.

## Upgrade and downgrade limits

A verified artifact is not permission to replace an enrolled host binary or
restart a service. Source releases do not install schema changes. Current
protected enrollment records bind installed artifact hashes, so copying a new
binary over an installation is not a supported update procedure.

The planned operator update and control-store recovery commands are separate
work and are not advertised as available here. Opening a control store with a
new binary can migrate its schema. An older binary refuses a newer schema with
`schema_unsupported`. Reverting the binary alone is not a database downgrade.
The intended recovery requires a verified pre-update control snapshot and its
matching older binary, losing control history created after that snapshot. That
recovery remains unshipped until its admission, capture and restore-validation
gates pass. Application rollback never rolls back application data or the
Brine control store. No production-readiness or recovery claim follows from a
release build.
