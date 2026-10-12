# Fixed recovery-pairs evidence

The fixed fixture writes a sequence, marker, commit timestamp and scheduled flag
to both recovery tables in one transaction. Brine supports only the three
qualified artifact/schema pairs in the shared engine predicate. A declaration,
descriptor name or caller flag does not qualify a different module.

`ObserveConfirmedPairs` hashes the actual Wasm bytes, derives native program
identity and qualifies the registered canonical schema before any network read.
It reads actual database/owner/name/module/schema metadata before and after a
fixed set of private SQL queries. Each SQL request explicitly sets
`confirmed=true`. The exact native response schema, zero write statistics,
paired rows, contiguous sequence, fixed marker and positive timestamps must
match. Bodies and transport are disposed before the function returns.

The caller must already hold the owned live engine handle and publication
exclusion. A loopback address alone proves neither ownership nor disk mode.
The owned pinned runtime excludes `--in-memory`; native SQL otherwise has a
memory-mode branch without durability. The qualified modules have no client
connection/disconnection hooks. The HTTP SQL endpoint does run those lifecycle
paths, so unsupported artifact bytes refuse before metadata or SQL requests.

The resulting `ConfirmedPairsRecord` is a durable lower bound. It has no native
offset or finality claim. A scheduler or writer can advance after the read. Its
strict canonical codec retains scope and actual row tuples; decoding establishes
syntax only. The host must authenticate protected journal bytes, digest and
operation identity before using a retained record.

After the original stops, the existing passive reader opens only a private copy
with frozen durability and no ModuleHost, hooks or scheduler. Its fixed
projection reports independent recovery tables, marker, publisher and sequence
rows, optional note rows, layouts and actual table kinds. The Go domain checks
the qualified actual program/schema pair, paired finite rows, publisher id 1
matching the observed native owner, sequence id 1 matching the last pair and
the complete actual user-table set matching the registered schema.

`CheckConfirmedLowerBound` requires the final passive state to retain every
confirmed tuple. `CheckRecoveredPairsEqual` compares final and restored finite
facts and native offset. Complete `SamePersistedState` equality remains a
separate required check. Neither starting a restored server nor restarting a
fenced source supplies this evidence.

`CheckRecoveryPairPreservation` compares the base four columns through staged
publication, qualifies every appended rating as 7, verifies an additive note
table is empty and compares unchanged commitments for every other declared user
table. Its opaque proof derives data requirements and scope digests from those
facts. Guest-code and callable/lifecycle/scheduler reviews remain independent.

First publication uses `CheckRecoveryPairsInitial` with only the actual passive
candidate. It requires empty recovery, writer, schedule and note tables, the
fixed marker and actual initialized publisher/sequence controls. It returns only
`verify_new_table_data`. It does not manufacture an empty native source or call
preservation against one.

The row limit is the fixture's 10,000-row bound. Response, canonical record and
native output budgets remain bounded; a legitimate large result can exceed
those byte limits and refuse. Returned bytes, names, rows and requirements do
not expose mutable backing storage. No API accepts SQL, profile selection,
expected caller rows or a proof digest that grants authority.

The local source observer has been exercised against actual native 2.11.0
instances for all three supported fixtures, empty and with ordinary/scheduled
rows. Unit and race tests cover the retained codec, finite comparisons,
preservation and initial-state refusals. Actual passive projection qualification
and production host/journal composition remain separate gates until their
observed acceptance evidence ships.
