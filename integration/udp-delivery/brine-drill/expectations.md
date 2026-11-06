# Drill release expectations

Manifest schema 2 separates the app's stable identity from each release.
`apps[].incarnation` pins the exact app incarnation alongside its name, domain,
and endpoint. App-wide `expected_release` and `image_digest` fields are rejected.
There is no schema 1 fallback.

An `apply` step can declare an `expectation` containing `release`, the fixture's
UDP echo label, and `image_digest`. The pair must match `fixture.release_digests`.
An apply without an expectation can run but cannot supply release evidence.
An expected `failed` or `rolled_back` apply cannot supply release evidence either.
The runner preserves those terminal outcomes rather than treating them as success.

`status` steps selecting `ready` or `absent`, all `client-probe` steps, and
`rollback` steps require `release_step`. This names an earlier successful apply
with a pinned expectation, or an earlier successful rollback, for the same app.
Missing, forward, cyclic, foreign-app, failed, or unpinned references refuse.
Phase declaration order is before-reboot, after-reboot, cleanup, inspect, resume.
Execution still requires the referenced operation to have succeeded in the ledger.

This step fragment represents an update and preservation check. The omitted
manifest fields and proof files remain required. Digests below are illustrative.

```json
[
	{"id":"deploy-v1","action":"apply","app":"echo","plan_id":"PLAN_V1",
	 "expectation":{"release":"v1","image_digest":"sha256:V1_DIGEST"}},
	{"id":"update-v2","action":"apply","app":"echo","plan_id":"PLAN_V2",
	 "expectation":{"release":"v2","image_digest":"sha256:V2_DIGEST"}},
	{"id":"failed-v3","action":"apply","app":"echo","plan_id":"PLAN_V3",
	 "expected_outcome":"failed"},
	{"id":"preserved-v2","action":"status","app":"echo","expected_outcome":"ready",
	 "release_step":"update-v2","proof":"/absolute/v2-proof.json","proof_sha256":"HASH"},
	{"id":"rollback-v1","action":"rollback","app":"echo","release_step":"deploy-v1"},
	{"id":"restored-v1","action":"client-probe","app":"echo","release_step":"rollback-v1",
	 "expected_outcome":"ok","vantage":"separate-client","nonce":"rollback-nonce"}
]
```

The bound ledger supplies the exact Brine release ID from the referenced apply's
operation ID. Its app, action, deterministic key, plan, command hash, and successful
state must match. Operation polling also checks the returned operation ID.
A rollback selects that historical ID with the existing `rollback --release`
command. Its own apply creates a new operation ID for the restored image.
Later observations reference the rollback step and require that new ID, not the
historical ID. A completed rollback replay submits no plan or mutation.

Ready and absence proof receipts include `incarnation` and `release_id` in
addition to the pinned fixture release label, image, app, run, target, and endpoint.
Ready status must match the ledger release ID and the manifest incarnation and
image. The existing HTTP, independent UDP owner, inode, mapping, and absence
predicates remain required. A client result proves only its separate bounded
nonce exchange, with the selected fixture label and exact endpoint and vantage.

Dry-run output derives probe labels from manifest references without a ledger.
Unexecuted rollback IDs appear as `<operation:STEP_ID>` in the displayed command.
This is a display placeholder, never a submitted release ID.

`expectations_test.go` exercises one immutable fixture manifest through v1, v2,
a failed replacement preserving v2, and rollback to v1. These are local scripted
observations, not physical acceptance or permission to perform host operations.
