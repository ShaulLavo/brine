# P04-05 isolated restore evidence

## Real storage gate

The coordinator ran `TestRealStorageRestore` on 2026-10-10 against real R2 with temporary credentials scoped to the test bucket and run prefix. The worker never read the operator's parent credential file. The gate passed in 8.51 seconds.

The checksum-verified Linux amd64 Litestream 0.5.17 binary ran from a coordinator-provided scratch path. Production restore instead requires the protected enrollment executable at `/opt/brine/litestream/0.5.17/litestream`.

The fixture committed one sentinel row while the real replicator was running. The writer reported WAL mode, a 4,152-byte WAL, one affected row and one source sentinel row. The successful private `sync -wait` barrier reported `txid = 1` and `replica_txid = 1`. The dry-run restore selected exact hex TXID `0000000000000001`; the subsequent isolated restore used that TXID and a full integrity check. One sentinel row was recovered.

All three restores passed integrity, foreign-key and declared invariant checks. The ordinary LTX and SQLite snapshot recovered the committed sentinel and independently observed fixture schema. The third restore verified an affirmative empty SQLite snapshot. Every loss window remained unknown. No zero-loss claim follows from this drill.

The test created one random child prefix under the operator-authorized run prefix. Cleanup listed and deleted only its five objects, then proved that child prefix empty. No live mount, production database, local replica substitute or public listener was used.

P04-05 stays unchecked. Connected command/dispatcher wiring remains serialized after P04-01 through P04-03; the shared fixed schema observer still needs its adapter. This gate used a test-only observer that verifies the fixture's exact marker DDL, pinned Litestream table definitions and normative catalog fingerprint. P04-04's durable remote evidence and connected lifecycle operations are not proved here. Generated systemd units and Debian arm64/Pi replacement/reboot drills remain separate gates.

## Invocation shape

The coordinator supplies an externally minted, destination-scoped credential set on bounded stdin. Do not copy parent credentials into the worktree, put credentials in arguments or write credential values into evidence.

```sh
export PATH=$HOME/.local/share/mise/shims:$PATH
worktree=/work/worktrees/brine/p04-05-restore
cd "$worktree"
export GOTMPDIR="$worktree/.tmp" TMPDIR="$worktree/.tmp" GOCACHE=/work/cache/go-build
go test -c -o .tmp/restore-gate.test ./internal/restore
cd "$worktree/internal/restore"
BRINE_RESTORE_GATE=1 BRINE_LITESTREAM_GATE_BINARY="<checksum-verified-scratch-binary>" "$worktree/.tmp/restore-gate.test"   -test.run '^TestRealStorageRestore$' -test.v -test.timeout 10m
```

Pipe the operator-generated JSON object into the final process's stdin. Its accepted field names are `endpoint`, `region`, `bucket`, `prefix`, `access_key`, `secret_key`, `session_token` and `expires_at`. The session token and supplied expiry are optional; when the external issuer supplies an expiry, preserve it. Admission refuses insufficient remaining lifetime. The whole input is bounded to 32 KiB and rejects unknown fields and trailing input.

Use endpoint form `https://<account>.r2.cloudflarestorage.com`, region `auto`, bucket `brine-test`, prefix `p04-05-gate/<run-id>/`, and path-style addressing. The test appends its own random `drill-<nonce>/` child. These are forms and field names, not stored credentials or an account inventory.

## Receipts from the passing drill

The following receipts are copied from the coordinator's secret-free passing log. There is no account endpoint or credential material in them. TXID evidence is the pinned CLI's exact selected plan boundary followed by successful restoration, not an independent interpretation of the requested-TXID JSON echo. Snapshot position is its immutable object key and independently verified byte hash/size, not an invented TXID.

### LTX restore

```json
{
  "operation_id": "ltx-restore",
  "source": {
    "kind": "litestream_ltx",
    "litestream_ltx": {
      "binding_id": "gate-binding",
      "epoch": "gate-epoch",
      "txid": 1,
      "barrier": {
        "binding_id": "gate-binding",
        "epoch": "gate-epoch",
        "txid": 1,
        "replica_txid": 1,
        "observed_at": "2026-10-10T09:33:10.706132837Z",
        "succeeded": true
      }
    }
  },
  "tool_version": "0.5.17",
  "requested_txid": 1,
  "recovered_txid": 1,
  "observed_at": "2026-10-10T09:33:12.473694694Z",
  "schema": {
    "state": "verified_schema",
    "marker": "fixture-v1",
    "catalog_sha256": "f7a92d3e7a27c89c3afd455307ec78b2417b6e643aefbcf1c7c33c3c59838341"
  },
  "sentinel": {
    "table": "fixture_commits",
    "sequence": 7,
    "marker": "marker-7",
    "committed_at": "2026-10-10T09:33:09Z"
  },
  "upload_barrier": {
    "binding_id": "gate-binding",
    "epoch": "gate-epoch",
    "txid": 1,
    "replica_txid": 1,
    "observed_at": "2026-10-10T09:33:10.706132837Z",
    "succeeded": true
  },
  "loss_window": {
    "state": "unknown",
    "from": "0001-01-01T00:00:00Z",
    "to": "0001-01-01T00:00:00Z",
    "reason": "No independent last-commit coverage proof; asynchronous replication may lose recent writes."
  },
  "integrity_check": "passed",
  "foreign_key_check": "passed",
  "invariant_check": "passed",
  "position_evidence": "pinned_cli_exact_plan_and_successful_restore"
}
```

### SQLite snapshot restore

```json
{
  "operation_id": "snapshot-restore",
  "source": {
    "kind": "sqlite_snapshot",
    "sqlite_snapshot": {
      "binding_id": "gate-binding",
      "epoch": "gate-epoch",
      "point_id": "gate-point",
      "object_key": "p04-05-gate/run-bd34a59d/drill-4719a807020e969c/restore-points/gate-point/snapshot.sqlite",
      "sha256": "8ea4bb7dddaf43cee16dcb2cefc5b37fe77365268c4c421e482a04a79816e1c4",
      "size": 20480
    }
  },
  "tool_version": "brine-s3-snapshot-v1",
  "observed_at": "2026-10-10T09:33:13.450196965Z",
  "schema": {
    "state": "verified_schema",
    "marker": "fixture-v1",
    "catalog_sha256": "f7a92d3e7a27c89c3afd455307ec78b2417b6e643aefbcf1c7c33c3c59838341"
  },
  "sentinel": {
    "table": "fixture_commits",
    "sequence": 7,
    "marker": "marker-7",
    "committed_at": "2026-10-10T09:33:09Z"
  },
  "loss_window": {
    "state": "unknown",
    "from": "0001-01-01T00:00:00Z",
    "to": "0001-01-01T00:00:00Z",
    "reason": "No independent last-commit coverage proof; asynchronous replication may lose recent writes."
  },
  "integrity_check": "passed",
  "foreign_key_check": "passed",
  "invariant_check": "passed"
}
```

### Empty SQLite snapshot restore

```json
{
  "operation_id": "empty-restore",
  "source": {
    "kind": "sqlite_snapshot",
    "sqlite_snapshot": {
      "binding_id": "gate-binding",
      "epoch": "gate-epoch",
      "point_id": "empty-point",
      "object_key": "p04-05-gate/run-bd34a59d/drill-4719a807020e969c/restore-points/empty-point/snapshot.sqlite",
      "sha256": "e4a594a04b362fb0effc8d813971e25f6bec6102a1cd3fc12f37477e14d2e542",
      "size": 4096
    }
  },
  "tool_version": "brine-s3-snapshot-v1",
  "observed_at": "2026-10-10T09:33:14.549479589Z",
  "schema": {
    "state": "verified_empty",
    "marker": "brine-empty-v1",
    "catalog_sha256": "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"
  },
  "loss_window": {
    "state": "unknown",
    "from": "0001-01-01T00:00:00Z",
    "to": "0001-01-01T00:00:00Z",
    "reason": "No independent last-commit coverage proof; asynchronous replication may lose recent writes."
  },
  "integrity_check": "passed",
  "foreign_key_check": "passed",
  "invariant_check": "passed"
}
```

## Cleanup from the passing drill

```text
deleted_object="p04-05-gate/run-bd34a59d/drill-4719a807020e969c/ltx/0000/0000000000000001-0000000000000001.ltx"
deleted_object="p04-05-gate/run-bd34a59d/drill-4719a807020e969c/ltx/0001/0000000000000001-0000000000000001.ltx"
deleted_object="p04-05-gate/run-bd34a59d/drill-4719a807020e969c/ltx/0009/0000000000000001-0000000000000001.ltx"
deleted_object="p04-05-gate/run-bd34a59d/drill-4719a807020e969c/restore-points/empty-point/snapshot.sqlite"
deleted_object="p04-05-gate/run-bd34a59d/drill-4719a807020e969c/restore-points/gate-point/snapshot.sqlite"
```

The final listing was empty. Failed earlier drill attempts also cleaned only their own run objects.

## Regressions caught by real storage

- The release binary prints `0.5.17`, without a `v` prefix. The exact version-output fixture first failed the strict version check, then both production and drill checks were corrected.
- Loading a config resets the pinned CLI logging destination. Explicit `logging.stderr: true` keeps `restore -json` stdout parseable. A failing-first config test covers it.
- Multiple parameterized SQLite statements in one `Exec` reused the first positional arguments for the later statement. The fixture stored its schema marker instead of its commit timestamp. A local fixture verification reproduced the mismatch; separate marker and sentinel statements fixed it. This was a drill bug, not a failed production upload barrier.
