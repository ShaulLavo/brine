# Recover an interrupted native restore

Use these commands on the enrolled target. Keep the operation ID and idempotency
key from the original request. The restricted CLI accepts record IDs; it does not
accept data paths, SQL, shell commands or proof bodies.

## Inspect the original attempt

Run `brine status --target TARGET --operation OPERATION_ID --json` to read its
immutable task outcome. Then run:

```sh
brine restore native-live inspect OPERATION_ID --target TARGET --json
```

The restore retains its original preparation fence, authenticated download and
original data tree when an effect is uncertain. A failed request does not authorize
another extraction, repository initialization or tree overwrite. Keep those
artifacts and the original signing and recovery versions.

## Review a retained target

If the original attempt reached a protected target, request a fresh review.
This includes `transition_intent` before the tree transition was recorded:

```sh
brine restore native-live review-resolution OPERATION_ID --target TARGET --json
```

The host checks the current tree locations, owned units, exact secret files,
compatible installed code, loaded route absence, pending jobs and process absence.
The returned canonical review has a content-derived ID and an expiry. Use that ID
with a new idempotency key:

```sh
brine restore native-live resolve OPERATION_ID --review REVIEW_ID \
  --target TARGET --idempotency-key KEY --json
```

Before a transition exists, the review requires the original tree to remain at
its live location, the authenticated target to remain staged and the original
epoch and preparation fence to remain current. Resolution reuses that retained
target and incoming repository reservation. It creates the repository only when
the initialization journal proves no creation was admitted. An admitted but
uncertain initialization permits authenticated config readback only. A completed
initialization is reused. Extraction is never repeated. The fresh resolver review
does not reopen the expired stopped-source permit.

Resolution retains the original recovery-required task outcome. Its new task can
adopt known completed moves after directory sync and finish the new epoch. A source
still present under a retained uncertain move is not blindly moved again. The
restored engine and application remain stopped after completion. Replan the
ordinary managed deployment before starting them.

## Handle a refused review

If the host cannot prove the layout or process state, keep the fence held. The
current resolver refuses uncertain extraction, an unverified repository,
missing target preparation, foreign or replaced directories, running processes,
changed secrets and changed code. It does not clear the fence by request syntax.

Use the operator's administrative access to inspect the original journal, owner
marker, tree identities and known process state. Preserve the control store and
both original and partial trees before intervention. Never delete the partial
stage, overwrite an original, rename a foreign directory, change ownership,
replace a signing key or retry an uncertain mutation to make a check pass. Escalate
an unexplained state with the original operation ID and safe error code. Keep
credential values, endpoint inventories and signing material out of reports.

The restore code intent retains the original and reviewed recovery release IDs.
The host installs the reviewed frontend through the existing Quadlet replacement
and retains its predecessor. Engine-only recovery retains and removes the prior
frontend. Both remain stopped and ingress stays closed during this work. An
unexplained artifact refuses recovery. Application rollback does not rewind data
or select an older epoch. Physical historical-code recovery remains a required
acceptance drill.

## Inspect an interrupted archive

Run `brine archive native inspect ARCHIVE_ID --target TARGET --json` and read the
original task outcome. Native retirement preserves the original fence owner and
journals each whole transition-tree move before renaming. Preserve current data,
prior original trees, downloaded bundles and all recovery versions.

Run `brine archive native resolve OPERATION_ID --target TARGET --json` for an
interrupted cleanup with an existing archive manifest. The resolve task keeps the
original failed task outcome, verifies its exact remote source again and adopts
only known unit and tree movements. Uncertain preparation without a manifest
still requires manual inspection with the original fence held.

Retained archives have their own read authority for credential renewal, replay
and retention verification. It binds the original manifest, consumer, fence and
encrypted bundle. It does not reopen an expired preparation permit. Unpublished
archives use the recorded empty source and publisher identity without creating a
database or inventing data replay evidence.

The original thirty-day deadline starts after all retirement moves complete.
Early purge requires the separate default-false `allow_archive_purge` grant and a
fresh protected retention proof. Its deletion intent preserves that proof hash;
recovery rechecks the current grant, policy and retention before further effects.
Local expiry remains bound to the same deadline. Remote epochs and keys remain
retained. Physical archive and post-archive recovery drills are still required
acceptance evidence.
