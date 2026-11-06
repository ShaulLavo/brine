# Inspect SQLite backup health

1. Read connected status for the selected SQLite database:

   ```sh
   brine restore status APP --database NAME --target TARGET
   brine restore status APP --database NAME --target TARGET --json
   ```

   Omit `--database` only when the app has one SQLite database.

2. Check `remote.state` and `remote_watermark` separately. A reachable listing
   proves access, not recoverability. A known watermark identifies the remote
   Litestream restore boundary, not a verified database or local upload lag.
   An active service alone proves neither.

3. Check credential expiry and `renewal_needed`. Renew credentials through the
   existing reviewed credential workflow when needed. Status never creates a
   credential directory, substitutes another version, or renews credentials.
   Missing, expired, or unreadable credentials skip both remote probes.

4. Check lifecycle evidence and held fences. Ask the operator to refresh stale
   no-expiration evidence. Do not clear a fence or start a service to make the
   status look healthy.

5. Read `last_verified.verified_at` as historical evidence. Status selects an
   existing successful restore-test or initialization verification receipt.
   An uploaded restore-point row alone does not qualify. To check the current
   remote data independently, run the existing isolated restore test:

   ```sh
   brine restore test APP --database NAME --target TARGET
   ```

## Prepare a fenced recovery point

Preparation stops the app. Run it only when an outage is acceptable.

1. Prepare and keep the reported `idempotency_key` and `operation_id`:

   ```sh
   brine restore prepare APP --database NAME --target TARGET
   ```

   It stops the app's writers and the replica, verifies an exact remote point
   in isolation and leaves both units stopped under a held data fence. The
   startup permits refuse while the fence is held, so a reboot does not start
   them. Only committed replicas are supported today.

2. Optionally test the prepared point again, within ten minutes for a later
   reviewed consumer, or at any time as historical evidence:

   ```sh
   brine restore test APP --point PREPARATION_ID --target TARGET
   ```

3. If the task ends `recovery_required`, inspect it; do not start the app by
   hand. Replace it with `brine restore prepare APP --supersedes PREPARATION_ID`
   or release it with `brine restore abandon PREPARATION_ID`. A changed stopped
   source refuses both until it is understood.

4. To give up the preparation, abandon it. Abandon starts nothing. Start the
   app afterwards through the normal start or deploy workflow, or let the next
   boot start it:

   ```sh
   brine restore abandon PREPARATION_ID --target TARGET
   ```

## Unknown values

Local position, position distance, lag, and possible loss remain unknown because
no qualified local-position reader is installed. A TXID difference, if measured
in future, is a replication-position distance, not an application commit count.
Asynchronous replication cannot guarantee zero recent-write loss.

Status has a 60-second host budget. Listing and dry-run probes have 15-second and
30-second child budgets. A failed or timed-out probe returns degraded partial
facts. Caller cancellation cancels the call. Status disposes its private dry-run
workspace and operation-created parent before returning a known watermark.
It never opens the mutation runtime, migrates the control store, writes protected
records, changes permissions, or touches the application database or service.
The read-only control-store connection can update SQLite WAL/SHM bookkeeping.
An old or missing control store refuses rather than creating or upgrading it.
