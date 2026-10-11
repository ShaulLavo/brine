# SpacetimeDB online backup pilot

This is pre-integration evidence from the authorized disposable arm64 fixture before Phase 09 integration. It does not complete a Phase 09 task or admit a production engine.

The official v2.11.0 standalone binary kept writing during four native snapshot/log captures. Restic 0.19.1 encrypted each archive, uploaded it to scoped R2 storage and downloaded it into a fresh directory. Four isolated same-version engines replayed those remote copies with the expected database, module, schema, owner identity and signing keys. Source data mounts were absent. The owner token stayed outside the archives.

| Capture | Rows observed before capture | Rows recovered | Observed omitted tail |
| --- | ---: | ---: | ---: |
| Completed snapshot/log files 1 | 375 | 342 | 33 |
| Completed snapshot/log files 2 | 516 | 486 | 30 |
| Completed snapshot/log files 3 | 754 | 728 | 26 |
| Plus bounded active-head copy | 1,311 | 1,311 | 0 |

The captures loaded nonempty native snapshots and replayed their log suffixes. The full active-head run was one successful copy at the observed byte length. It did not deliberately cut a partial record or prove that every acknowledgement is durable. The SQL reads used the pinned default confirmed-read behavior, without an explicit confirmed-read request. A hash check rejected a deliberately truncated required log before startup. That is archive-integrity evidence, not a native truncated-tail replay test.

Control-db and program bytes remained unchanged through every capture. Module publication, database metadata changes, signing-key rotation, Sled compaction, concurrent log rotation, snapshot completeness and durable-offset boundaries need separate bounded proof. Completed-segment-only archival gives no fixed wall-clock loss bound under sparse writes.

Replay and verification took 0.42 to 0.77 seconds in this small fixture. The writer made about five reducer calls per second. Engine RSS was roughly 93 to 101 MB with 64 MiB page-pool and JS-heap bounds. These numbers do not estimate production capacity. No OCI image, amd64 runtime or Rust/Wasm fixture execution is claimed.

Routine backup design uses this online native snapshot/log route with existing encrypted backup tooling. Exact preparation for reviewed publish, restore and retirement still needs a durable fence, independently proven quiescence and remotely verified coverage of the exact source.

The complete scrubbed local record, scripts, member hashes and replay logs are retained at `/work/reports/brine-wave4/spacetime-proof/result.md`. Credentials, recovery passwords, owner tokens, signing keys and host inventory are excluded from this repository.
