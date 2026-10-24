# Command reference

Every command, subcommand and flag of `brine` 0.1.72-dev. Sections under [Commands](#commands) are generated from the CLI's own `--help` text. Do not edit them by hand.

Regenerate this page after a command changes:

```sh
go build -o /tmp/brine ./cmd/brine
{ sed '/^<!-- generated below -->$/q' docs/reference/commands.md; scripts/docs-commands.sh /tmp/brine; } > /tmp/commands.md
mv /tmp/commands.md docs/reference/commands.md
```

## Global flags

Every command accepts these flags.

```text
      --json       Machine-readable JSON output
      --jsonl      Machine-readable JSON event stream
      --no-input   Never request interactive input
```

`--json` and `--jsonl` cannot be combined. Envelope fields, error codes and exit codes are in [CONTRACTS](../CONTRACTS.md#machine-modes). Exit codes are 0 success, 1 internal failure, 2 usage or validation, 3 missing dependency, 4 policy refusal, 5 stale plan or lock conflict, 6 manual recovery required, and 130 interrupted.

## Command index

| Group | Commands |
| --- | --- |
| Set up and check | `doctor`, `validate`, `enroll`, `permissions`, `version`, `tui` |
| Plan and deploy | `plan`, `apply`, `deploy`, `status`, `logs` |
| Change a running app | `config set`, `secret set`, `rollback`, `start`, `stop`, `restart`, `remove` |
| Recover | `diagnose`, `reconcile`, `resolve` |
| Data and backups | `data`, `backup`, `restore`, `archive` |
| CDN | `cdn` |

`brine completion bash|zsh|fish|powershell` prints a shell completion script. `brine tui` needs a terminal and refuses `--json`, `--jsonl` and `--no-input`. Today it shows a welcome screen only.

## Commands

<!-- generated below -->

### `brine apply`

```text
Accept a plan for detached execution on the target

Usage:
  brine apply PLAN_ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for apply
      --idempotency-key string   Reuse this key to recover the same operation after a lost response
      --target string            Pinned target name
```

### `brine archive`

```text
Review and retire data archives

Usage:
  brine archive [command]

Available Commands:
  credentials Renew only one retained archive binding and epoch without activating a replica
  maintain    Review retained-data verification or local deletion and inspect interrupted deletion
  native      Archive an exactly prepared SpacetimeDB app and preserve its recovery history
  status      Read archived credential health, retained verification and local deletion state

Flags:
  -h, --help   help for archive

Use "brine archive [command] --help" for more information about a command.
```

### `brine archive credentials`

```text
Renew only one retained archive binding and epoch without activating a replica

Usage:
  brine archive credentials [command]

Available Commands:
  plan        Review archived credential renewal with current original binding/epoch policy
  set         Store bounded private S3 credential JSON on stdin in the archived namespace

Flags:
  -h, --help   help for credentials

Use "brine archive credentials [command] --help" for more information about a command.
```

### `brine archive credentials plan`

```text
Review archived credential renewal with current original binding/epoch policy

Usage:
  brine archive credentials plan ARCHIVE_ID --binding ID --epoch ID --target NAME [flags]

Flags:
      --binding string      Original archived binding ID (required)
      --config-dir string   Private client target directory
      --epoch string        Original archived epoch ID (required)
      --expires-at string   Optional issuer-supplied UTC expiry
  -h, --help                help for plan
      --target string       Pinned target name
```

### `brine archive credentials set`

```text
Store bounded private S3 credential JSON on stdin in the archived namespace

Usage:
  brine archive credentials set ARCHIVE_ID --binding ID --epoch ID --target NAME [flags]

Flags:
      --binding string      Original archived binding ID (required)
      --config-dir string   Private client target directory
      --epoch string        Original archived epoch ID (required)
  -h, --help                help for set
      --plan string         Exact reviewed archived credential plan
      --target string       Pinned target name
```

### `brine archive maintain`

```text
Review retained-data verification or local deletion and inspect interrupted deletion

Usage:
  brine archive maintain [command]

Available Commands:
  apply       Apply one exact reviewed maintenance plan in a detached operation
  plan        Review exact verify_retention, purge_archive or expire_archive intent
  resolve     Inspect and settle only an already-journaled unknown local deletion

Flags:
  -h, --help   help for maintain

Use "brine archive maintain [command] --help" for more information about a command.
```

### `brine archive maintain apply`

```text
Apply one exact reviewed maintenance plan in a detached operation

Usage:
  brine archive maintain apply ARCHIVE_ID --plan ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for apply
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --plan string              Exact reviewed maintenance plan
      --target string            Pinned target name
```

### `brine archive maintain plan`

```text
Review exact verify_retention, purge_archive or expire_archive intent

Usage:
  brine archive maintain plan ARCHIVE_ID --kind KIND --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for plan
      --kind string         verify_retention, purge_archive or expire_archive (required)
      --target string       Pinned target name
```

### `brine archive maintain resolve`

```text
Inspect and settle only an already-journaled unknown local deletion

Usage:
  brine archive maintain resolve OPERATION_ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for resolve
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --target string            Pinned target name
```

### `brine archive native`

```text
Archive an exactly prepared SpacetimeDB app and preserve its recovery history

Usage:
  brine archive native [command]

Available Commands:
  apply       Retire owned runtime and retain the full data archive for thirty days
  inspect     Inspect retained archive effects before recovery
  plan        Review the current exact point, all epochs and terminal recovery trees
  resolve     Reconcile retained archive effects after fresh remote verification

Flags:
  -h, --help   help for native

Use "brine archive native [command] --help" for more information about a command.
```

### `brine archive native apply`

```text
Retire owned runtime and retain the full data archive for thirty days

Usage:
  brine archive native apply APP --plan ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for apply
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --plan string              Reviewed immutable archive plan
      --target string            Pinned target name
```

### `brine archive native inspect`

```text
Inspect retained archive effects before recovery

Usage:
  brine archive native inspect ARCHIVE_ID --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for inspect
      --target string       Pinned target name
```

### `brine archive native plan`

```text
Review the current exact point, all epochs and terminal recovery trees

Usage:
  brine archive native plan APP --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for plan
      --target string       Pinned target name
```

### `brine archive native resolve`

```text
Reconcile retained archive effects after fresh remote verification

Usage:
  brine archive native resolve OPERATION_ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for resolve
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --target string            Pinned target name
```

### `brine archive status`

```text
Read archived credential health, retained verification and local deletion state

Usage:
  brine archive status ARCHIVE_ID --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for status
      --target string       Pinned target name
```

### `brine backup`

```text
Capture native backups and manage private backup credentials

Usage:
  brine backup [command]

Available Commands:
  abandon     Abandon an unused exact native preparation and leave its components stopped
  capture     Capture and verify a SpacetimeDB backup without stopping writers
  credentials Plan and deliver externally issued S3 credentials
  policy      Review and activate native backup cadence and recovery material
  prepare     Verify a fresh exact SpacetimeDB point and retain its data fence
  recovery    Deliver independent recovery passwords for encrypted native backups
  status      Inspect protected native backup health without probing the provider

Flags:
  -h, --help   help for backup

Use "brine backup [command] --help" for more information about a command.
```

### `brine backup abandon`

```text
Abandon an unused exact native preparation and leave its components stopped

Usage:
  brine backup abandon POINT_ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for abandon
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --target string            Pinned target name
```

### `brine backup capture`

```text
Capture and verify a SpacetimeDB backup without stopping writers

Usage:
  brine backup capture APP --database NAME --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
      --database string          Managed database name (required)
  -h, --help                     help for capture
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --target string            Pinned target name
```

### `brine backup credentials`

```text
Plan and deliver externally issued S3 credentials

Usage:
  brine backup credentials [command]

Available Commands:
  plan        Plan a private credential version without reading credential values
  set         Deliver a bounded private JSON credential packet on stdin

Flags:
  -h, --help   help for credentials

Use "brine backup credentials [command] --help" for more information about a command.
```

### `brine backup credentials plan`

```text
Plan a private credential version without reading credential values

Usage:
  brine backup credentials plan APP --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
      --database string     Database name (required for apps with multiple databases)
      --expires-at string   Optional issuer-supplied UTC expiry (RFC3339 with Z)
  -h, --help                help for plan
      --target string       Pinned target name
```

### `brine backup credentials set`

```text
Deliver a bounded private JSON credential packet on stdin

Usage:
  brine backup credentials set APP --plan-id ID --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
      --database string     Database name (required for apps with multiple databases)
  -h, --help                help for set
      --no-wait             Return the detached operation ID without waiting for its receipt
      --plan-id string      Confirmed credential plan identity
      --target string       Pinned target name
```

### `brine backup policy`

```text
Review and activate native backup cadence and recovery material

Usage:
  brine backup policy [command]

Available Commands:
  activate    Authenticate the owned repository and install fixed backup units
  plan        Save a native backup revision from a reviewed deployment plan

Flags:
  -h, --help   help for policy

Use "brine backup policy [command] --help" for more information about a command.
```

### `brine backup policy activate`

```text
Authenticate the owned repository and install fixed backup units

Usage:
  brine backup policy activate PLAN_ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for activate
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --target string            Pinned target name
```

### `brine backup policy plan`

```text
Save a native backup revision from a reviewed deployment plan

Usage:
  brine backup policy plan --deployment-plan ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
      --deployment-plan string   Saved managed deployment plan selecting backup policy and destination
  -h, --help                     help for plan
      --target string            Pinned target name
```

### `brine backup prepare`

```text
Verify a fresh exact SpacetimeDB point and retain its data fence

Usage:
  brine backup prepare APP --database NAME --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
      --database string          Managed database name (required)
  -h, --help                     help for prepare
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --target string            Pinned target name
```

### `brine backup recovery`

```text
Deliver independent recovery passwords for encrypted native backups

Usage:
  brine backup recovery [command]

Available Commands:
  inspect     
  resolve     Resolve inspected backup work through a reviewed operation
  review      
  set         Deliver a private password JSON packet on stdin

Flags:
  -h, --help   help for recovery

Use "brine backup recovery [command] --help" for more information about a command.
```

### `brine backup recovery inspect`

```text
Usage:
  brine backup recovery inspect OPERATION_ID --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for inspect
      --target string       Pinned target name
```

### `brine backup recovery resolve`

```text
Resolve inspected backup work through a reviewed operation

Usage:
  brine backup recovery resolve REVIEW_ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for resolve
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --target string            Pinned target name
```

### `brine backup recovery review`

```text
Usage:
  brine backup recovery review OPERATION_ID --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for review
      --target string       Pinned target name
```

### `brine backup recovery set`

```text
Deliver a private password JSON packet on stdin

Usage:
  brine backup recovery set --deployment-plan ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
      --deployment-plan string   Saved managed deployment plan selecting the recovery reference
  -h, --help                     help for set
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --target string            Pinned target name
```

### `brine backup status`

```text
Inspect protected native backup health without probing the provider

Usage:
  brine backup status APP --database NAME --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
      --database string     Managed database name (required)
  -h, --help                help for status
      --target string       Pinned target name
```

### `brine cdn`

```text
Manage host-journaled immutable-origin CDN attachments

Usage:
  brine cdn [command]

Available Commands:
  apply       Admit one host operation and run its one-shot native effects
  plan        Review a connected CDN enable, switch, direct-origin or retained revision plan
  resume      Inspect issued effects and continue unissued CDN work
  status      Inspect host history; refresh reads providers and the public path

Flags:
  -h, --help   help for cdn

Use "brine cdn [command] --help" for more information about a command.
```

### `brine cdn apply`

```text
Admit one host operation and run its one-shot native effects

Usage:
  brine cdn apply --plan-id ID --idempotency-key KEY --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for apply
      --idempotency-key string   Reuse this key after a lost admission reply
      --plan-id string           Reviewed host CDN plan
      --profiles string          Private operator CDN profile registry
      --target string            Pinned target name
```

### `brine cdn plan`

```text
Review a connected CDN enable, switch, direct-origin or retained revision plan

Usage:
  brine cdn plan APP --binding NAME --profile NAME --sample PATH --target NAME [flags]

Flags:
      --binding string             Protected host binding
      --config-dir string          Private client target directory
  -h, --help                       help for plan
      --mode string                Desired mode: enabled or direct (default "enabled")
      --profile string             Policy-approved private edge profile
      --profiles string            Private operator CDN profile registry
      --recover-operation string   Fully settled failed operation whose reservation this recovery plan replaces
      --revision string            Retained CDN revision to restore through a new plan
      --sample string              Small existing hash-named immutable object path for full-body qualification
      --target string              Pinned target name
```

### `brine cdn resume`

```text
Inspect issued effects and continue unissued CDN work

Usage:
  brine cdn resume --operation ID --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for resume
      --operation string    Host CDN operation
      --profiles string     Private operator CDN profile registry
      --target string       Pinned target name
```

### `brine cdn status`

```text
Inspect host history; refresh reads providers and the public path

Usage:
  brine cdn status --app APP [--operation ID] --target NAME [flags]

Flags:
      --app string          Enrolled app
      --config-dir string   Private client target directory
  -h, --help                help for status
      --operation string    Inspect a particular host CDN operation
      --profiles string     Private operator CDN profile registry
      --refresh             Perform fresh provider reads and full public GETs
      --target string       Pinned target name
```

### `brine config`

```text
Plan app configuration changes

Usage:
  brine config [command]

Available Commands:
  set         Edit environment, resources, domains, health or secret references

Flags:
  -h, --help   help for config

Use "brine config [command] --help" for more information about a command.
```

### `brine config set`

```text
Edit environment, resources, domains, health or secret references

Usage:
  brine config set APP KEY=VALUE... --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for set
      --target string       Pinned target name
      --unset stringArray   Remove an environment key (repeatable)
```

### `brine data`

```text
Prepare persistent data without starting an application

Usage:
  brine data [command]

Available Commands:
  init        Review and initialize never-started empty data with a verified backup
  material    Deliver immutable native publisher, signing and authentication material
  module      Review and publish a SpacetimeDB module against an exact prepared point
  prepare     Plan approved allocation before credentials, schema initialization and deploy
  schema      Write the derived initializer JSON for review

Flags:
  -h, --help   help for data

Use "brine data [command] --help" for more information about a command.
```

### `brine data init`

```text
Review and initialize never-started empty data with a verified backup

Usage:
  brine data init APP [flags]

Flags:
      --artifact string             Local reviewed initializer JSON exported by brine data schema
      --config-dir string           Private client target directory
      --first-release-plan string   Exact retained deployment or preparation plan
  -h, --help                        help for init
      --no-wait                     Return the accepted job ID without waiting
      --plan                        Review SQL and catalog without changing schema
      --plan-id string              Apply this initialization plan; never replay an attempted mutation
      --target string               Pinned target name
```

### `brine data material`

```text
Deliver immutable native publisher, signing and authentication material

Usage:
  brine data material [command]

Available Commands:
  activate    Apply a reviewed material selection while all components remain stopped
  plan        Review stored material activation after a planned stop; restart with the new signing versions
  set         Deliver a private four-role JSON packet on stdin; leave native and frontend services stopped
  status      Inspect retained delivery metadata without changing files or runtime

Flags:
  -h, --help   help for material

Use "brine data material [command] --help" for more information about a command.
```

### `brine data material activate`

```text
Apply a reviewed material selection while all components remain stopped

Usage:
  brine data material activate PLAN_ID --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for activate
      --target string       Pinned target name
```

### `brine data material plan`

```text
Review stored material activation after a planned stop; restart with the new signing versions

Usage:
  brine data material plan APP --delivery OPERATION_ID --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
      --delivery string     Successful stored_unreferenced delivery operation (required)
  -h, --help                help for plan
      --target string       Pinned target name
```

### `brine data material set`

```text
Deliver a private four-role JSON packet on stdin; leave native and frontend services stopped

Usage:
  brine data material set APP --preparation-plan ID --target NAME [flags]

Flags:
      --config-dir string         Private client target directory
  -h, --help                      help for set
      --idempotency-key string    Reuse this key to inspect the same immutable delivery after a lost response
      --preparation-plan string   Saved managed preparation plan (required)
      --target string             Pinned target name
```

### `brine data material status`

```text
Inspect retained delivery metadata without changing files or runtime

Usage:
  brine data material status [OPERATION_ID] --target NAME [flags]

Flags:
      --app string                App for a key-based inspection
      --config-dir string         Private client target directory
  -h, --help                      help for status
      --idempotency-key string    Inspect a lost response using the reported delivery key
      --preparation-plan string   Preparation plan for a key-based inspection
      --target string             Pinned target name
```

### `brine data module`

```text
Review and publish a SpacetimeDB module against an exact prepared point

Usage:
  brine data module [command]

Available Commands:
  apply       Publish and activate the reviewed module without starting the app
  inspect     Inspect the retained publication attempt without changing its state
  plan        Review the owned module bytes, actual native schema and compatible recovery releases
  resolve     Inspect and resolve the original publication without repeating native effects

Flags:
  -h, --help   help for module

Use "brine data module [command] --help" for more information about a command.
```

### `brine data module apply`

```text
Publish and activate the reviewed module without starting the app

Usage:
  brine data module apply APP PLAN_ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for apply
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --target string            Pinned target name
```

### `brine data module inspect`

```text
Inspect the retained publication attempt without changing its state

Usage:
  brine data module inspect PLAN_ID --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for inspect
      --target string       Pinned target name
```

### `brine data module plan`

```text
Review the owned module bytes, actual native schema and compatible recovery releases

Usage:
  brine data module plan APP --point POINT_ID --deployment-plan PLAN_ID --wasm FILE --target NAME [flags]

Flags:
      --config-dir string          Private client target directory
      --deployment-plan string     Immutable target deployment declaration reference
  -h, --help                       help for plan
      --point string               Exact prepared point reference
      --recovery-release strings   Compatible retained release IDs; omit only when this incarnation has no release history
      --target string              Pinned target name
      --wasm string                Local owned Wasm artifact
```

### `brine data module resolve`

```text
Inspect and resolve the original publication without repeating native effects

Usage:
  brine data module resolve APP PLAN_ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for resolve
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --operation string         Original publication apply operation reference
      --target string            Pinned target name
```

### `brine data prepare`

```text
Plan approved allocation before credentials, schema initialization and deploy

Usage:
  brine data prepare <brine.toml> --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for prepare
      --target string       Pinned target name
```

### `brine data schema`

```text
Write the derived initializer JSON for review

Usage:
  brine data schema <brine.toml> --database NAME [flags]

Flags:
      --database string   Declared database whose initializer to export
  -h, --help              help for schema
```

### `brine deploy`

```text
Review and run data preparation, initialization and release in order

Usage:
  brine deploy <brine.toml> --target NAME [flags]

Flags:
      --backup-credentials string   Existing 0600 JSON packet with externally issued backup credentials
      --config-dir string           Private client target directory
  -h, --help                        help for deploy
      --target string               Pinned target name
      --yes                         Approve the combined review and execute in order
```

### `brine diagnose`

```text
Gather a bounded read-only troubleshooting report

Usage:
  brine diagnose [APP] --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for diagnose
      --target string       Pinned target name
```

### `brine doctor`

```text
Check local client dependencies (not remote host readiness)

Usage:
  brine doctor [flags]

Flags:
  -h, --help   help for doctor
```

### `brine enroll`

```text
Enroll through the operator's own admin SSH access

Usage:
  brine enroll <ssh-destination> [flags]

Flags:
      --config-dir string    Private client target directory
      --deploy-key string    Path to the operator-supplied Ed25519 public deploy key
      --finish               Verify an existing enrollment and finish its private client target config
  -h, --help                 help for enroll
      --host-binary string   Matching-architecture Linux brine binary
      --identity string      Private key path (defaults to --deploy-key without .pub)
      --target-name string   Name of the pinned client target
      --undo                 Remove only verified enrollment-owned resources
      --yes                  Still requires terminal stdin and typed target-name confirmation
```

### `brine logs`

```text
Read a bounded, best-effort redacted app log tail

Usage:
  brine logs APP [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for logs
      --since string        Earliest timestamp (RFC3339)
      --tail int            Maximum log entries (1-1000) (default 100)
      --target string       Enrolled target name (required)
```

### `brine permissions`

```text
Manage registry, domain and CDN permissions through operator admin SSH

Usage:
  brine permissions [command]

Available Commands:
  apply       
  plan        
  show        
  status      

Flags:
      --admin string        Operator admin SSH destination; never the restricted deploy credential
      --config-dir string   Private client target directory
  -h, --help                help for permissions
      --target string       Pinned enrolled target

Use "brine permissions [command] --help" for more information about a command.
```

### `brine permissions apply`

```text
Usage:
  brine permissions apply [flags]

Flags:
  -h, --help                     help for apply
      --idempotency-key string   Stable 32-hex permission operation key
      --plan-id string           Reviewed permission plan ID

Global Flags:
      --admin string        Operator admin SSH destination; never the restricted deploy credential
      --config-dir string   Private client target directory
      --target string       Pinned enrolled target
```

### `brine permissions plan`

```text
Usage:
  brine permissions plan [flags]

Flags:
      --allow-cdn-binding stringArray    Allow the exact typed CDN binding from a JSON file (repeatable)
      --allow-domain stringArray         Allow an exact domain (repeatable)
      --allow-registry stringArray       Allow an exact REGISTRY/REPOSITORY prefix (repeatable)
      --app string                       Optional app label for the audit record; does not grant app-specific authority
  -h, --help                             help for plan
      --revoke-cdn-binding stringArray   Revoke the matching exact typed CDN binding from a JSON file (repeatable)
      --revoke-domain stringArray        Revoke an exact domain (repeatable)
      --revoke-registry stringArray      Revoke an exact REGISTRY/REPOSITORY prefix (repeatable)

Global Flags:
      --admin string        Operator admin SSH destination; never the restricted deploy credential
      --config-dir string   Private client target directory
      --target string       Pinned enrolled target
```

### `brine permissions show`

```text
Usage:
  brine permissions show [flags]

Flags:
  -h, --help   help for show

Global Flags:
      --admin string        Operator admin SSH destination; never the restricted deploy credential
      --config-dir string   Private client target directory
      --target string       Pinned enrolled target
```

### `brine permissions status`

```text
Usage:
  brine permissions status [flags]

Flags:
  -h, --help                     help for status
      --idempotency-key string   Stable 32-hex permission operation key

Global Flags:
      --admin string        Operator admin SSH destination; never the restricted deploy credential
      --config-dir string   Private client target directory
      --target string       Pinned enrolled target
```

### `brine plan`

```text
Preview a non-applyable offline plan. Use --target NAME for connected planning on an enrolled host. Image platform is an offline assumption from the snapshot, not a registry verification.

Usage:
  brine plan <brine.toml> --target NAME | --offline --snapshot <snapshot.json> --policy <policy file> [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for plan
      --offline             Require local snapshot planning; never contact infrastructure
      --out string          Existing directory for the private, hash-named offline plan file
      --policy string       Explicit operator policy TOML file
      --snapshot string     Target snapshot JSON file
      --state string        Committed Brine release state JSON file (required for installed apps)
      --target string       Pinned target name
```

### `brine reconcile`

```text
Inspect interrupted operations and safely settle their outcomes

Usage:
  brine reconcile --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
      --dry-run             Preview recovery without writing events or applying effects
  -h, --help                help for reconcile
      --target string       Pinned target name
```

### `brine remove`

```text
Plan to remove the app's owned unit

Usage:
  brine remove APP --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for remove
      --target string       Pinned target name
```

### `brine resolve`

```text
Inspect and resolve a terminal recovery-required operation

Usage:
  brine resolve OPERATION_ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for resolve
      --idempotency-key string   Reuse this key to recover the same operation after a lost response
      --target string            Pinned target name
```

### `brine restart`

```text
Plan to restart the app's owned unit

Usage:
  brine restart APP --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for restart
      --target string       Pinned target name
```

### `brine restore`

```text
Verify remote backups without changing live application data

Usage:
  brine restore [command]

Available Commands:
  native-live Review and restore an exact SpacetimeDB point into a new stopped epoch
  native-test Verify an exact SpacetimeDB backup in private isolation without changing live data
  point       Inspect a retained native point reference
  test        Restore into an isolated private workspace and check declared data invariants

Flags:
  -h, --help   help for restore

Use "brine restore [command] --help" for more information about a command.
```

### `brine restore native-live`

```text
Review and restore an exact SpacetimeDB point into a new stopped epoch

Usage:
  brine restore native-live [command]

Available Commands:
  apply             Apply a reviewed restore and leave its new epoch stopped
  inspect           Read the retained restore stage without retrying effects
  plan              Review selected data, current exact coverage and compatible application code
  resolve           Complete a reviewed known restore effect without repeating extraction or initialization
  review-resolution Review the actual retained layout of an interrupted restore

Flags:
  -h, --help   help for native-live

Use "brine restore native-live [command] --help" for more information about a command.
```

### `brine restore native-live apply`

```text
Apply a reviewed restore and leave its new epoch stopped

Usage:
  brine restore native-live apply APP --plan ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for apply
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --plan string              Reviewed restore plan ID
      --target string            Pinned target name
```

### `brine restore native-live inspect`

```text
Read the retained restore stage without retrying effects

Usage:
  brine restore native-live inspect OPERATION_ID --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for inspect
      --target string       Pinned target name
```

### `brine restore native-live plan`

```text
Review selected data, current exact coverage and compatible application code

Usage:
  brine restore native-live plan APP --database NAME --point ID --prepared-point ID --recovery-release ID --target NAME [flags]

Flags:
      --config-dir string         Private client target directory
      --database string           Managed database name
  -h, --help                      help for plan
      --point string              Exact verified point to restore
      --prepared-point string     Fresh exact point protecting the current source
      --recovery-release string   Compatible release whose installed code is independently checked
      --target string             Pinned target name
```

### `brine restore native-live resolve`

```text
Complete a reviewed known restore effect without repeating extraction or initialization

Usage:
  brine restore native-live resolve OPERATION_ID --review ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for resolve
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --review string            Fresh retained resolution review ID
      --target string            Pinned target name
```

### `brine restore native-live review-resolution`

```text
Review the actual retained layout of an interrupted restore

Usage:
  brine restore native-live review-resolution OPERATION_ID --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for review-resolution
      --target string       Pinned target name
```

### `brine restore native-test`

```text
Verify an exact SpacetimeDB backup in private isolation without changing live data

Usage:
  brine restore native-test POINT_ID --target NAME [flags]

Flags:
      --config-dir string        Private client target directory
  -h, --help                     help for native-test
      --idempotency-key string   Reuse the reported key to recover the same operation after a lost response
      --no-wait                  Return the accepted operation ID without waiting for verification
      --target string            Pinned target name
```

### `brine restore point`

```text
Inspect a retained native point reference

Usage:
  brine restore point POINT_ID --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for point
      --kind string         Point reference kind: verified or prepared (default "verified")
      --target string       Pinned target name
```

### `brine restore test`

```text
Restore into an isolated private workspace and check declared data invariants

Usage:
  brine restore test APP --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
      --database string     Database name (required for apps with multiple databases)
  -h, --help                help for test
      --no-wait             Return the detached operation ID without waiting for its receipt
      --point string        Durable immutable restore-point ID
      --target string       Pinned target name
      --txid string         Exact nonzero hexadecimal Litestream transaction ID
```

### `brine rollback`

```text
Plan a rollback without changing app or data

Usage:
  brine rollback APP --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for rollback
      --release string      Stored release ID (default: previous known-good)
      --target string       Pinned target name
```

### `brine secret`

```text
Store unbound app secret versions

Usage:
  brine secret [command]

Available Commands:
  set         Read a secret value from stdin and store an immutable unbound version

Flags:
  -h, --help   help for secret

Use "brine secret [command] --help" for more information about a command.
```

### `brine secret set`

```text
Read a secret value from stdin and store an immutable unbound version

Usage:
  brine secret set APP NAME --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for set
      --target string       Pinned target name
```

### `brine start`

```text
Plan to start the app's owned unit

Usage:
  brine start APP --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
  -h, --help                help for start
      --target string       Pinned target name
```

### `brine status`

```text
Read app releases and health, or poll an operation

Usage:
  brine status [APP] --target NAME [flags]

Flags:
      --after-cursor uint   Return events after this sequence number
      --config-dir string   Private client target directory
  -h, --help                help for status
      --operation string    Accepted operation ID
      --target string       Pinned target name
```

### `brine stop`

```text
Plan to stop the app's owned unit

Usage:
  brine stop APP --target NAME [flags]

Flags:
      --config-dir string   Private client target directory
      --engine              Include the managed engine in the reviewed stop for material reload
  -h, --help                help for stop
      --target string       Pinned target name
```

### `brine tui`

```text
Open the interactive terminal UI

Usage:
  brine tui [flags]

Flags:
  -h, --help   help for tui
```

### `brine validate`

```text
Validate an app against an explicit policy without contacting a host

Usage:
  brine validate <brine.toml> --policy <policy file> [flags]

Flags:
  -h, --help            help for validate
      --policy string   Explicit operator policy TOML file
```

### `brine version`

```text
Show the CLI version

Usage:
  brine version [flags]

Flags:
  -h, --help   help for version
```

