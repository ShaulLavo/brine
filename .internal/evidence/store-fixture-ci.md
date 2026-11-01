# Store fixture initialization

The store race CI job reached its existing 40-minute package timeout. The last
running test had spent only two seconds opening its fixture. This did not show a
hung UDP transition or a data race.

## Bounded measurement

A diagnostic overlay changed only the ordinary store fixture helper. The source
under review stayed unchanged. Both cases used independent real SQLite files,
WAL, FULL synchronization, private state directories and production `Open`.

| Local race measurement | Repeated migrations | Closed pristine seed |
| --- | ---: | ---: |
| Median fixture `Open`, three samples | 3302.094 ms | 80.195 ms |
| Seven existing tests, including archive state cases | 43.121 s | 10.465 s |

The race initialization probe sampled 13.90 CPU seconds over 14.01 wall seconds.
The profile attributes 65.32% of CPU samples to `Store.migrate`, with race and
pointer-check instrumentation prominent. These measurements establish repeated
schema and instrumentation cost. They do not establish fsync dominance or
predict the hosted runner's final duration.

## Fixture contract

`openTest` copies bytes from one closed pristine database built by the current
binary through all production migrations. Each fixture opens its own copy through
production `Open`. No live SQLite connection, app rows or writable fixture state
is shared. The seed's temporary directory is removed before initialization
returns, and each ordinary fixture keeps its existing test cleanup.

`TestPristineTestControlDBInitialized` compares the seed's complete SQLite schema
with a fresh production initialization. It queries the copied bytes directly,
without letting `Open` repair missing initialization. An uninitialized seed
fails that test. Other focused tests check one immutable seed, row and connection
isolation, FULL/WAL settings, file permissions and initialization teardown.

Explicit migration fixtures and production migration code are unchanged. The
bounded feature-overlay control ran all 27 existing migration, schema and
pristine-schema tests plus the UDP tests, 46 top-level tests in total. The same
migration selection runs against the actual test-only change.

## Reproduce

Run through the machine's shared runner with an owned durable temporary directory.
No full-suite rerun is needed to reproduce this bounded comparison.

```sh
go test ./internal/store -run 'PristineTestControlDB|OpenTestPristineFixtureIsolation' -count=1
go test -race ./internal/store -run 'PristineTestControlDB|OpenTestPristineFixtureIsolation|Migrat|Schema|Pristine' -count=1 -timeout=5m
go vet ./internal/store
bash scripts/test-ci.sh
bash scripts/check.sh lint
bash scripts/check.sh structure
git diff --check
```

No production behavior, dependency, timeout, skip or quality baseline changes.
Hosted CI must still validate the feature independently.
