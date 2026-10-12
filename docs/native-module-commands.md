# Reviewed native module publication

Native module changes use the same restricted personal identity as other Brine
operations. They require an exact prepared SpacetimeDB point, a retained target
deployment declaration, delivered native roles and admitted encrypted backup
material. Deployment does not create or publish the module.

## Review and apply

Upload the bounded local Wasm bytes and select compatible retained recovery
release references. The host extracts the actual native schema in containment,
verifies the original encrypted restore, checks current roles and policy, and
stores the review acknowledgement with the authenticated requester.

```sh
brine data module plan game --point POINT_ID --deployment-plan PLAN_ID \
  --wasm ./module.wasm --recovery-release RELEASE_ID --target test --json
brine data module apply game REVIEWED_PLAN_ID --target test --json
```

Omit `--recovery-release` only for a genuinely unpublished allocation whose
current incarnation has no committed release history. Review expires after at
most fifteen minutes and cannot be rebound to different bytes, roles or data.
The deployment plan supplies the target declaration and is not an execution
grant for starting a stale deployment.

Apply returns a detached operation reference and records intent before native
publication. Private publication runs on a separately owned working copy; the
authenticated download and original live tree remain intact. Passive validation
checks the actual program, schema and supported data requirements. The host
stops and disposes its private helpers, initializes one fresh owned backup
repository, journals original preservation and target promotion, and commits a
successor epoch. Completion releases only the original fence and leaves engine,
frontend and ingress intent stopped. Create a fresh deployment plan to start the
application against the new epoch.

`--no-wait` returns after acceptance. Keep the reported idempotency key and
operation ID to recover the same request after a lost response.

## Inspect and resolve

```sh
brine data module inspect REVIEWED_PLAN_ID --target test --json
brine data module resolve game REVIEWED_PLAN_ID --operation APPLY_OPERATION_ID \
  --target test --json
```

Inspection reads protected progress without publishing, starting, stopping or
initializing anything. Resolution has a separate retained operation and keeps
the original outcome unchanged. It never repeats native publication, a private
stop or repository initialization.

Before live transition admission, the resolver verifies the original full tree,
actual component and route absence, and any private working state. A changed
working state is inspected with the fixed passive reader under the protected
publisher identity. The full owned inventory must have one selected replica;
missing, ambiguous, corrupt or unrecognized native state refuses resolution.
An explained source-preserved outcome retains the original data and records
stopped intent before releasing its fence. It does not claim that an unknown
private publication never happened.

An already admitted transition resumes only through its original consumer,
repository, target and outcome records plus fresh physical layout and absence.
Unknown moves are adopted through observed layout and fsync, never repeated.
A recorded completed transition or source-preserved result can supply a lost
terminal receipt without releasing its fence again.

The host API accepts reference selectors and owned Wasm bytes only. It accepts
no source path, URL, SQL, schema, native token, requester or independent approval
or proof hash. The reader pin and its request producer change together; current
enrollment installs the independently verified native reader 0.0.5 artifacts.
