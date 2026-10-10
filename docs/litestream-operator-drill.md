# Protected Litestream operator drill

Status: Approved for the disposable D3 host only.

Enrollment installs Litestream for new hosts. Upgrading an already enrolled host is a Phase 06 operation and is not implemented. Do not replay enrollment or alter its journal to add the tool. This drill is an operator installation, not proof of enrollment or enrollment undo. It changes no app, database, credential, service, SSH setting or firewall. Leave the executable installed for later Phase 04 drills.

## Before installation

Use the authorized operator connection. Keep host identifiers and client configuration out of public evidence. Check whether the exact protected executable already exists. If it does, record its hash, version, owner, mode and link count. Reuse a matching installation. Stop on a symlink, unprotected parent or an unrelated install; do not replace it.

Download the matching D11 release tarball into disposable scratch, using HTTPS. For arm64 the asset is `litestream-0.5.17-linux-arm64.tar.gz`, and SHA-256 is `f8ca4a050095c1efbda2c4365172e61bf9d955ea0d9ac42f448b52e51819baa5`. For amd64 use `litestream-0.5.17-linux-x86_64.tar.gz`, SHA-256 `cfb371176d164437ae869f8351cfde49bd1804ae71c61923f75c9cba9c9c006d`. The release URL starts with `https://github.com/benbjohnson/litestream/releases/download/v0.5.17/`.

Verify the archive hash before extracting or executing anything. Accept only regular files with the release's exact names (`litestream`, `LICENSE`, `README.md`, `etc/litestream.service`, `etc/litestream.yml`). Extract only the executable; do not install the upstream service or configuration. Execute its `version` command in scratch and require `0.5.17`. Record its executable SHA-256 independently of the archive hash.

## Operator effect boundary

Before editing an operator-owned file, make a root-owned timestamped backup preserving its mode and ownership. For an absent executable, record absence instead; no existing file needs backup. Preserve every preexisting parent directory. Refuse symlinked, non-root-owned or group/other-writable parents. Create only missing `/opt/brine`, `/opt/brine/litestream` and `/opt/brine/litestream/0.5.17` directories, root-owned 0755.

Install via a root-owned same-directory staging file. Set root:root ownership and 0755 mode. Verify the staging file's hash and version again. Publish without overwriting an existing executable, then fsync the executable and containing directory. Retain a root-owned operator receipt recording which directories were created, prior absence or backup location, archive hash, executable hash and received time. This receipt is not Brine's enrollment journal and must not be used to authorize enrollment undo.

## Verification

Run these commands through the authorized operator connection:

~~~sh
/opt/brine/litestream/0.5.17/litestream version
sha256sum /opt/brine/litestream/0.5.17/litestream
stat -c '%a %u %g %h' /opt /opt/brine /opt/brine/litestream \
  /opt/brine/litestream/0.5.17 /opt/brine/litestream/0.5.17/litestream
~~~

Require version `0.5.17`, the same executable hash as verified scratch, root ownership, non-writable parents, executable mode 0755 and a single link. The `inventory.LitestreamCollector` additionally reports the executable hash and exact-path version, refusing unknown protection or a changing hash. A running tool is not proof of S3 access or recoverability.

For enrollment acceptance, a fresh disposable host still needs the displayed confirmation list, apply, interrupted-install reconciliation, drift refusal and undo drills. For credentials, later integration must supply committed scope/policy/fence readers and the operation journal, restart only the corresponding replica through P04-02's typed boundary, and verify real externally scoped S3 access. No provider-specific API or credential minting belongs in this drill.
