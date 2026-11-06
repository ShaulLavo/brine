# UDP interruption fixture timing

The apply interruption tests now copy one closed, initialized schema into each
new private database. The subprocess still opens that database through production
`store.Open`, is killed at its original durable boundary, and the parent reopens
the same journal. Nothing seeds a recovery database.

## Coverage and history

The audit selected this setup in Brine finding B1 at source snapshot
`c339b9731f88f73dfaddc91c6a7d3fee31c6550e`. The existing pattern is
[`7d336d83`](https://github.com/ShaulLavo/brine/commit/7d336d83af666dbeecb358e0bdb2229ccca8ef8c),
which introduced pristine schema copies for store tests. The UDP regression came
from [`978907d5`](https://github.com/ShaulLavo/brine/commit/978907d5e0a03314f9512dfdd3571341c12475b6).
The earlier interruption-recovery work remains in
[`ac54ff43`](https://github.com/ShaulLavo/brine/commit/ac54ff4391c4848a1d5cb06b2af45b9a65aa77d9).

At the audit pin, `internal/apply/udp_subprocess_test.go:214` contains the 30
create, move and remove cases across intent, applied and completed boundaries.
Its four failed-removal cases begin at line 424. Both original test bodies and
all failure identities are preserved. Only setup at lines 235 and 431 changes.
The 45-second child deadline and minute-long pause remain unchanged. Fresh
migration, descendant containment and real socket tests are unchanged.

The new controls compare the complete pristine schema with fresh production
initialization, check schema version, integrity and mode 0600, prove two fixtures
cannot read one another's operations, refuse overwriting a journal, and verify
seed scratch teardown. Immutable strings hold the shared seed. Each database,
connection, plan, operation, child and checkpoint remains private. Production
`store.Open` continues to apply WAL, FULL synchronization, foreign keys and the
5-second busy timeout.

## Measurement

Run both source versions with Go 1.27.3 on the same Linux amd64 host and temp
filesystem. Admission uses the suite class of the heavy runner. The focused
command is identical on both versions:

```sh
go test -race -v -count=1 -p 1 -timeout 420s \
  -run '^(TestUDPRealSubprocessInterruptionBoundaries|TestUDPRemovalFailedBoundariesResolveFromRealSQLiteState)$' \
  ./internal/apply
```

The final pair ran sequentially within one runner job, `audit-action-9-paired`,
on the local Intel Core i7-14700K Linux amd64 host, with temporary source and
database state on `/work`. Go was 1.27.3, package parallelism was 1, the race
detector was enabled, and each side used one uncached test iteration.

| Focused package execution | Before | After | Saved |
| --- | ---: | ---: | ---: |
| Both original UDP tests | 267.718 s | 137.877 s | 129.841 s, 48.50% |

This saves **2.164 minutes per focused run**. Both runs passed the same 34 unique
named cases. A byte comparison also reconstructs the original test file exactly
after reversing only the two setup edits. These are package execution times from
a non-quiet host, not a hosted CI or daily saving. The new fixture-control tests
are outside that selection. Their separate final race probe took 13.513 seconds.

`./scripts/check.sh fast` and `./scripts/check.sh lint` pass with no new findings.
The final fixture controls pass under `-race`. The full apply race suite passed
in 65.117 seconds before the final root-confined file-opening adjustment. After
that adjustment, the complete normal apply suite passed in 134.410 seconds on
`/work`, all 34 selected race cases passed in the pair above, and `go vet ./...`
passed. `./scripts/check.sh test-normal` passed its all-package normal tests,
Linux and Darwin vet, module tidiness, documentation checks, CLI build and isolated
protocol experiment. That run compiled apply before the file-opening adjustment;
the final apply and vet runs above verify that last test-helper change. There is
no production code, timer or repeat-count change.

## Cleanup

The measurement copies source into a private temporary snapshot under the
worktree's ignored `.tmp` directory. Teardown is installed before copying and
removes source, compiled binaries and SQLite state on exit. Test cleanup kills
and waits for each child before deleting its private directory. The heavy runner
also checks and drains descendants. No deployment, service, container or route is
created. Shared Go and analyzer caches retain their existing owners and consumers.
