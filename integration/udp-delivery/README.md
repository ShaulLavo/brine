# UDP delivery fixture checks

Real local HTTP and UDP tests plus a teardown-safe subprocess runner for the disposable UDP fixture.

```sh
TMPDIR=/work/tmp/task-owned GOCACHE=/work/cache/go-build \
  integration/udp-delivery/run-local.sh /work/tmp/new-udp-evidence
```

Run through the shared PC runner for builds and race tests. See [the fixture contract](../../fixtures/udp-echo/docs/fixture.md) for limits and the separate physical acceptance work.
