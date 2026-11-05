# Brine

Brine deploys containerized apps to a Linux server you own through reviewed plans and an agent-friendly CLI.

Brine is experimental and has no release binaries or tags. Build it from source.

## Quick start

Install Go 1.26 or newer and `ssh`, then run:

```sh
git clone https://github.com/ShaulLavo/brine
cd brine
go build -o brine ./cmd/brine
./brine validate examples/offline/brine.toml --policy examples/offline/policy.toml
```

See the [overview](docs/overview.md), [getting started guide](docs/getting-started.md), [documentation](docs/README.md), and [status and limits](docs/status.md).

[MIT license](LICENSE) · [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md)
