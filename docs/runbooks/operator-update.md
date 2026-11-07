# Update an enrolled host

Use this procedure through the operator's existing admin SSH access. The deploy
key is restricted and cannot perform an update. Keep the finished private target
configuration and identity-key file from enrollment.

1. Build or obtain the reviewed Linux candidate for the host's architecture.
2. Run the update and inspect its artifact hashes, schema change and quiescence.

   ~~~sh
   brine enroll operator@host --target-name staging --update --host-binary /absolute/brine-linux
   ~~~

3. Type the displayed target name. Store quiescence avoids app restarts unless
   the plan reports a drain blocker. To accept full-runner downtime explicitly,
   add `--quiesce=runner`. A pre-gate installation always needs this downtime.
4. After an interruption, rerun the same command with the same candidate.
   The host resumes its retained manifest. Do not replace admission locks or
   clear its state by hand. Unknown activation waits for native inspection,
   not another restart.
5. To roll back an unfinished update, supply the same candidate and replace
   `--update` with `--update-abort`. This restores the captured raw control DB
   and WAL, or original absence. A terminal forward commit cannot be aborted.
   An approved unrecorded binary replacement has no supported abort unless its
   recorded rollback bytes are available. Continue that transaction forward.

A different installed binary hash normally refuses. If you intentionally made
a manual replacement, review the observed hash and use
`--replace-unrecorded-binary` with that exact hash. Other artifact drift cannot
be overridden. A manual binary-only replacement with a gated candidate fails
closed until this update path installs admission.

After commit, Brine checks ping, rejected shell commands and inventory through
the existing deploy key. `update_committed_verification_failed` means the update
committed but that connection check failed. Inspect it without automatic rollback.
Never use `--finish` as an update verification command on a used host.

The result names retained recovery staging. Successful updates keep only their current committed staging. An abort retains
the latest committed point and its own recovery staging until the next completed
update supersedes it. Raw abort quarantine is retained separately in the
runner state directory for inspection. Remove it only after the operator has
accepted recovery and no pending transaction references it.

After enrollment undo verifies that its resources are absent, it retires the
recorded committed and aborted staging before removing the enrollment journal.
Retirement checks the saved manifest and file identities. Unknown entries or
changed files refuse and leave the journal available for inspection. This does
not remove raw abort quarantine from the runner state directory.

These commands are not permission to deploy an unreviewed binary or change a
real host. Physical PAM, reboot, observer-listener and systemd qualification
belong to the separately authorized host drill.
