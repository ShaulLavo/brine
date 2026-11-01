# UDP echo fixture

A non-root HTTP health and bounded UDP nonce echo fixture for disposable delivery drills.

Run local checks through the machine's shared runner:

```sh
TMPDIR=/work/tmp/task-owned GOCACHE=/work/cache/go-build \
  integration/udp-delivery/run-local.sh /work/tmp/new-udp-evidence
```

Build a distinct release and use the separate client:

```sh
fixtures/udp-echo/build.sh /work/tmp/new-udp-release release-a
/work/tmp/new-udp-release/amd64/udp-echo-client \
  -endpoint 127.0.0.1:7777 -release release-a -vantage local-test
```

See [the fixture contract and image recipe](docs/fixture.md) for startup, bounds, image pins and evidence limits.
