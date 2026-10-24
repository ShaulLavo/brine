# Getting started

In this tutorial we check an app definition against an operator policy, preview three deployment plans, and read the same plans as JSON. Everything runs on your laptop against sample files in `examples/offline/`. No server is involved, and nothing here deploys anything.

You need Go 1.26 or newer, because `go.mod` declares `go 1.26.0`. You need `ssh` because `brine doctor` checks for it and connected commands run it. The last step needs `jq` to pick fields out of the JSON. Output below comes from a real run of this checkout.

## Build the CLI

From the repository root:

```sh
go build -o brine ./cmd/brine
./brine version
```

You see the development version:

```text
brine 0.1.72-dev
```

## Read the app definition

`examples/offline/brine.toml` describes one app:

```toml
schema_version = 1
name = "hello"
image = "ghcr.io/team/hello@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
container_port = 3000
domains = ["hello.example.com"]

[environment]
APP_ENV = "fixture-setting-value"
```

The image must carry a `@sha256:` digest. Brine refuses an image reference without one. The [configuration reference](reference/configuration.md) lists every key.

## Validate it against a policy

On a real server, the operator owns a policy file that says which registries, domains and secrets an app may use. `examples/offline/policy.toml` is a sample. Validate the app against it:

```sh
./brine validate examples/offline/brine.toml --policy examples/offline/policy.toml
```

```text
Valid app hello
Image: ghcr.io/team/hello@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
Domains: hello.example.com
Container port: 3000
Policy: operator-1
Environment: 1 keys (values hidden), secrets: 0 references
```

Point the app at a domain the policy does not allow:

```sh
sed 's/hello.example.com/hello.evil.org/' examples/offline/brine.toml > /tmp/bad.toml
./brine validate /tmp/bad.toml --policy examples/offline/policy.toml
echo "exit $?"
```

```text
error: The operation was refused by policy.
exit 4
```

Exit code 4 always means a policy refusal. The error text is fixed on purpose, so it never echoes your input back.

## Preview a plan for a fresh host

A plan is the list of changes Brine would make. Offline mode builds one from a snapshot file that describes a host, instead of contacting one:

```sh
./brine plan examples/offline/brine.toml --offline \
  --snapshot examples/offline/fresh-host.json --policy examples/offline/policy.toml
```

```text
OFFLINE PLAN — NOT APPLYABLE
create  hello
  Platform manifest: unknown
+ image: sha256:aaaaaaaaaaaa... (linux/arm64, manifest unknown)
+ domain: hello.example.com
+ host port: 20000
+ container port: 3000
+ resources: 512 MiB, 128 PIDs
+ health: / status 200, startup 30s, timeout 3s
+ env APP_ENV: added (value hidden)
+ allocate host port 20000
~ pull and verify image sha256:aaaaaaaaaaaa...
~ render app service
~ routing generation: 0 -> 1
~ restart app
Plan hash: sha256:097a9b60de672ac2fee106c66a42496f66dcf614fde8bf090bdf4c70e82f86ec
```

Lines that start with `+` are the new desired state, and lines that start with `~` are the steps Brine would run. Environment values stay hidden. The plan hash changes when the app, the policy or the host facts change.

## Preview a plan for an app that already runs

The second snapshot describes a host that already runs `hello`. Add the committed release state with `--state`:

```sh
./brine plan examples/offline/brine.toml --offline \
  --snapshot examples/offline/host-with-app.json --policy examples/offline/policy.toml \
  --state examples/offline/brine-state.json
```

```text
OFFLINE PLAN — NOT APPLYABLE
no-op  hello
  No changes required.
  Platform manifest: unknown
Plan hash: sha256:a3e6dddf45d98cdd5123092e1e0c4635a47c554babb2aa9497d65b244360b95c
```

Nothing differs between the definition and what runs, so the plan is a `no-op`.

## Preview a plan for an unenrolled host

The third snapshot describes a host that is not enrolled:

```sh
./brine plan examples/offline/brine.toml --offline \
  --snapshot examples/offline/unenrolled-host.json --policy examples/offline/policy.toml
echo "exit $?"
```

```text
OFFLINE PLAN — NOT APPLYABLE
conflict  hello
  Platform manifest: unknown
! runtime_unavailable [runner.user]: A required runtime or runner is not ready.
! runtime_unavailable [versions.caddy]: A required runtime or runner is not
ready.
! runtime_unavailable [versions.passt]: A required runtime or runner is not
ready.
! runtime_unavailable [versions.podman]: A required runtime or runner is not
ready.
! unknown_facts [runner.linger]: Required host facts have not been measured.
Plan hash: sha256:a912c9671d2f97939d832469d259e0fd7846be9e1fff353f2eb7750664376483
exit 0
```

The exit code is 0. A conflict is a successful planning result that says "do not apply this". Fix the host or the input, then plan again. A conflict plan carries no executable changes, so never apply it.

## Read the same plans as JSON

An agent reads plans with `--json`. Each command prints exactly one JSON object:

```sh
./brine plan examples/offline/brine.toml --offline \
  --snapshot examples/offline/fresh-host.json --policy examples/offline/policy.toml \
  --json | jq -c '{kind: .data.kind, changes: [.data.changes[].kind]}'
```

```json
{"kind":"create","changes":["allocate_port","pull_image","render_quadlet","stage_caddy_generation","restart_app"]}
```

The conflict plan has `kind` set to `conflict`, and its `conflicts` array lists the codes above:

```sh
./brine plan examples/offline/brine.toml --offline \
  --snapshot examples/offline/unenrolled-host.json --policy examples/offline/policy.toml \
  --json | jq -c '{kind: .data.kind, conflicts: [.data.conflicts[].code]}'
```

```json
{"kind":"conflict","conflicts":["runtime_unavailable","runtime_unavailable","runtime_unavailable","runtime_unavailable","unknown_facts"]}
```

An agent must check `kind` before it applies anything. The envelope fields and exit codes are in [CONTRACTS](CONTRACTS.md#machine-modes).

## Where next

- To run a plan against a real server, follow [Deploy an app](guides/deploy-an-app.md). It covers enrollment, which changes the server.
- To see how the pieces fit, read [Architecture](ARCHITECTURE.md).
- To look up a flag, use the [command reference](reference/commands.md).
