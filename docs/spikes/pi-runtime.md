# Pi runtime spike

Recorded 2026-10-08 against the decisions merged in `1fc96bf`. This is real-hardware evidence, not Brine implementation. No task checkboxes changed.

The runtime assumptions held for the tested configuration. Boot recovery remains unverified because an active interactive session prevented a reboot. D1 also needs an explicit restriction on the parent directories of `authorized_keys`. Protecting only the file does not protect the SSH boundary.

## Results

| Question | Answer | Evidence limit |
| --- | --- | --- |
| 1. Debian package revisions | Works | Debian 13.6, arm64, distribution packages listed below |
| 2. Rootless Quadlet and linger | Works with caveat | Starts and returns after runner user-manager restart; no full reboot |
| 3. Environment secret | Works | Container sees the value; ordinary container inspect and generated unit do not contain it |
| 4. Caddy generation and reload-only rule | Works | Valid generation serves; invalid and duplicate candidates fail; other verbs and another unit are denied |
| 5. App access to admin API | Works | Connection refused through container loopback and both generated host aliases |
| 6. Forced SSH command | Works with caveat | Stdin reaches dispatcher; arbitrary command is not executed; PTY and forwarding fail; runner-owned home permits key replacement |
| 7. Transient operation after SSH loss | Works with caveat | Runner-owned transient unit survives forced SSH transport termination and finishes; launch uses operator SSH with privilege drop |

## Evidence conventions

The target alias, account inventory, addresses, keys, and secret values are omitted. `$TARGET` represents the authorized target. `$LOOPBACK` represents the numeric IPv4 loopback address used in the executed commands. `$MESH` represents the existing Mesh executable. These substitutions preserve the command structure without publishing target details.

Commands that use `runner` ran inside an operator SSH session, from `/tmp`. The temporary runner had UID 1001. Its user manager was already running through linger.

```sh
runner() {
 sudo -n -u brine-spike env \
  XDG_RUNTIME_DIR=/run/user/1001 \
  DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus "$@"
}
```

Enrollment used `useradd --create-home --shell /bin/bash brine-spike` and `loginctl enable-linger brine-spike` through `sudo -n`. `sudo -n true` succeeded before mutation. No existing runner account or Caddy installation existed at baseline. The HTTP and fixture ports were free.

## 1. Exact package revisions

Installation ran only on the authorized target:

```sh
sudo -n apt-get update -qq
sudo -n apt-get install -y podman passt caddy
dpkg-query -W -f='${Package}\t${Version}\n' \
 podman passt caddy systemd crun conmon
```

Trimmed output:

```text
caddy   2.6.2-12+deb13u1
conmon  2.1.12-4
crun    1.21-1
passt   0.0~git20250503.587980c-2+deb13u1
podman  5.4.2+ds1-2+b2
systemd 257.13-1~deb13u1
```

`/etc/os-release` reported Debian GNU/Linux 13, trixie, `DEBIAN_VERSION_FULL=13.6`. `uname -m` returned `aarch64`.

```sh
runner podman info --format json | python3 -c '
import json,sys
x=json.load(sys.stdin)["host"]
print(x["security"]["rootless"], x["cgroupVersion"],
      x["networkBackend"], x["rootlessNetworkCmd"])
'
```

The observed fields were `true`, `v2`, `netavark`, and `pasta`. D2 matches the installed runtime.

## 2. Rootless Quadlet starts and returns after manager restart

The fixture was Docker Official Image `busybox:1.37.0`, approximately 4.35 MB unpacked on arm64. Initial tag resolution preceded the digest-pinned unit. No mutable tag appeared in the unit.

```sh
runner podman pull docker.io/library/busybox:1.37.0
runner podman image inspect docker.io/library/busybox:1.37.0 \
 --format '{{.Digest}} {{.RepoDigests}} {{.Architecture}} {{.Size}}'
runner podman manifest inspect \
 docker.io/library/busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e
```

Recorded digests:

- Multi-platform index `sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e`.
- Selected arm64 manifest `sha256:d82c2ab94640ded77cf76514ce6a84870761105058a4a9e51b05a8a79be97a6c`.
- Selected image configuration `sha256:ad4f11f7c85f87c0a6d6c0fe492ad53f9cdcff6e8c88b881360c70eff701c67b`.

Manifest inspection confirmed both `amd64` and `arm64` entries. The unit pinned the index digest, which selected the arm64 image on this target.

`/home/brine-spike/.config/containers/systemd/brine-fixture.container` contained the following. `$LOOPBACK` below replaces the literal loopback address, not a Quadlet variable used on the host.

```ini
[Unit]
Description=Disposable Brine runtime fixture
[Container]
Image=docker.io/library/busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e
ContainerName=brine-fixture
PublishPort=$LOOPBACK:20001:8080
Volume=/home/brine-spike/www:/www:ro
Secret=brine-spike-v1,type=env,target=X
Exec=httpd -f -p 8080 -h /www
[Install]
WantedBy=default.target
```

The runner-owned `www/index.html` contained `brine fixture v1`.

```sh
runner systemctl --user daemon-reload
runner systemctl --user start brine-fixture.service
runner systemctl --user is-active brine-fixture.service
runner podman inspect brine-fixture \
 --format '{{.ImageDigest}} {{.HostConfig.NetworkMode}}'
curl -fsS "http://$LOOPBACK:20001/"
```

Trimmed output:

```text
active
sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e pasta
brine fixture v1
```

A restart of only this fixture user's manager exercised `[Install] WantedBy=default.target` without explicitly starting the fixture again:

```sh
sudo -n systemctl restart user@1001.service
runner systemctl --user is-active brine-fixture.service
curl -fsS "http://$LOOPBACK:20001/"
loginctl show-user brine-spike -p Linger -p Sessions
```

The immediate observation was `activating`, and the first HTTP request failed. A later observation returned `active` and `brine fixture v1`. Linger was `yes`. Session inspection showed only the runner's user-manager session, with no TTY or interactive runner login.

This is manager-restart evidence, not reboot evidence. Startup is asynchronous, so Phase 02 must poll health with a deadline rather than issue one immediate request.

### Reboot was skipped

Before any reboot, the following read-only checks ran:

```sh
loginctl list-sessions --no-legend
loginctl show-session 2 -p Active -p State -p IdleHint -p Type -p Remote -p TTY
"$MESH" ls --daemon --privacy
```

Sanitized output:

```text
TTY=tty1
Remote=no
Type=wayland
Active=yes
State=active
IdleHint=no
no live sessions on known hosts
18 ended sessions hidden
```

An existing non-runner interactive session was active. The authorization required skipping the reboot in that case. No session was terminated or changed. Zero reboots were performed. The target's final `uptime -s` still preceded this spike by many hours.

## 3. Environment secret is visible only where expected

A fixture-only value was piped to `runner podman secret create brine-spike-v1 -`. The value never appeared in a unit or process argument used to create the secret. The disposable in-container equality assertion used the known fixture value; that assertion is redacted here.

```sh
runner podman exec brine-fixture sh -c '<redacted equality assertion for X>'
runner podman inspect brine-fixture | python3 -c '<redacted value-presence check>'
runner systemctl --user cat brine-fixture.service | \
 python3 -c '<redacted value-presence check>'
```

Actual assertion output:

```text
SECRET_VISIBLE=true
inspect_contains_secret_value= False
inspect_contains_env_target= False
unit_contains_secret_value= False
```

The generated unit named the secret and the target variable only. D5 works for ordinary container inspect. This does not hide a secret from the runner, a privileged operator, or deliberate secret-inspection commands. It does not test redaction of arbitrary application logs.

## 4. Whole-root Caddy validation and reload-only authorization

The Debian package started and enabled Caddy during installation. Its package-default site was retained. The only enrollment addition to the main Caddyfile was:

```caddyfile
import /etc/caddy/brine/current/*.caddy
```

The fixture created runner-owned generation directories beneath `/etc/caddy/brine`. `current` initially linked to `gen-1`. Both valid generations used a loopback-bound HTTP listener on port 8081. No public hostname, ACME request, or certificate was used.

`gen-1/app.caddy` contained:

```caddyfile
http://:8081 {
 bind $LOOPBACK
 reverse_proxy $LOOPBACK:20001
}
```

`gen-2/app.caddy` added `header X-Brine-Spike generation-2`. `gen-invalid/app.caddy` used `this_directive_does_not_exist`. `gen-duplicate` contained two copies of the valid site block in separate files.

For each generation, the runner built a temporary copy of the complete main Caddyfile. Only the Brine import destination changed:

```sh
sed "s@import /etc/caddy/brine/current/\\*.caddy@import /etc/caddy/brine/$gen/*.caddy@" \
 /etc/caddy/Caddyfile > /etc/caddy/brine/candidate.caddy
caddy validate --adapter caddyfile --config /etc/caddy/brine/candidate.caddy
```

The commands above ran as the runner. Validation returned:

```text
gen-1: Valid configuration
gen-2: Valid configuration
gen-invalid: unrecognized directive: this_directive_does_not_exist
gen-duplicate: ambiguous site definition: http://:8081
```

Neither rejected directory became `current`. No reload was attempted for either rejected candidate.

The installed temporary polkit rule was:

```javascript
polkit.addRule(function(action, subject) {
 if (subject.user == "brine-spike" &&
     action.id == "org.freedesktop.systemd1.manage-units" &&
     action.lookup("unit") == "caddy.service" &&
     action.lookup("verb") == "reload") {
   return polkit.Result.YES;
 }
});
```

Activation ran as the runner:

```sh
runner systemctl --no-ask-password reload caddy.service
curl -fsS "http://$LOOPBACK:8081/"
runner ln -s gen-2 /etc/caddy/brine/current.next
runner mv -Tf /etc/caddy/brine/current.next /etc/caddy/brine/current
runner systemctl --no-ask-password reload caddy.service
curl -fsS -D - "http://$LOOPBACK:8081/"
```

Both reloads succeeded. The second response included:

```text
HTTP/1.1 200 OK
Server: Caddy
X-Brine-Spike: generation-2

brine fixture v1
```

Negative tests ran through the same runner and with `--no-ask-password`:

```sh
runner systemctl --no-ask-password restart caddy.service
runner systemctl --no-ask-password stop caddy.service
runner systemctl --no-ask-password reload-or-restart caddy.service
runner systemctl --no-ask-password try-reload-or-restart caddy.service
runner systemctl --no-ask-password reload brine-spike-denied.service
```

Every request failed with `Interactive authentication required.` The other unit was a spike-owned system fixture with `ExecStart=/bin/true`, `ExecReload=/bin/true`, and `RemainAfterExit=yes`. No unrelated unit was used as a mutation target.

D4's generation sequence and polkit scope work on these revisions. This spike did not inject a reload failure after successful validation, race a generation change, or test timeout reconciliation. Those remain P02-05 acceptance work.

## 5. Default pasta refuses app access to Caddy admin

First, a host-side HTTP probe verified that the admin API was listening and returned status 200. Each container probe then used BusyBox `wget` with a three-second timeout:

```sh
curl -fsS -o /dev/null -w 'host_admin_http=%{http_code}\n' \
 "http://$LOOPBACK:2019/config/"
runner podman exec brine-fixture sh -c \
 'wget -T 3 -O - http://<loopback>:2019/config/; echo loopback_probe_exit=$?'
runner podman exec brine-fixture sh -c \
 'wget -T 3 -O /dev/null http://host.containers.internal:2019/config/; echo host_alias_probe_exit=$?'
runner podman exec brine-fixture sh -c \
 'wget -T 3 -O /dev/null http://host.docker.internal:2019/config/; echo docker_alias_probe_exit=$?'
```

`<loopback>` redacts the same numeric address as `$LOOPBACK`. The two reserved runtime aliases are generated container names, not target inventory.

Trimmed output:

```text
host_admin_http=200
wget: can't connect to remote host: Connection refused
loopback_probe_exit=1
wget: can't connect to remote host: Connection refused
host_alias_probe_exit=1
wget: can't connect to remote host: Connection refused
docker_alias_probe_exit=1
```

The actual pasta process included `--no-map-gw` and `--map-guest-addr`; no loopback-enabling option or host network mode was supplied. D4's expected denial holds for this exact default configuration. Testing the generated host aliases prevents a false conclusion from testing only the container's own loopback.

## 6. Forced SSH command delivers stdin but needs protected ancestors

A fresh local Ed25519 key was generated under `/work/tmp/brine-spike-pi-runtime`. The only authorized entry used:

```text
restrict,command="/usr/local/bin/brine-spike-serve" <throwaway public key>
```

The operator-owned script did not dispatch `SSH_ORIGINAL_COMMAND`. It recorded the original command and copied stdin to a runner-owned log and stdout:

```sh
#!/bin/sh
umask 077
printf "original=%s\n" "$SSH_ORIGINAL_COMMAND" >> /home/brine-spike/forced-command.log
cat | tee -a /home/brine-spike/forced-command.log
```

The shell-escape probe used the restricted key:

```sh
printf 'stdin-probe\n' | ssh -T -o BatchMode=yes -o IdentitiesOnly=yes \
 -i /work/tmp/brine-spike-pi-runtime/key -l brine-spike "$TARGET" \
 'sh -c "touch /home/brine-spike/SHELL_ESCAPE"'
sudo -n cat /home/brine-spike/forced-command.log
test ! -e /home/brine-spike/SHELL_ESCAPE
```

Trimmed output:

```text
stdin-probe
original=sh -c "touch /home/brine-spike/SHELL_ESCAPE"
stdin-probe
SHELL_ESCAPE=absent
```

A forced command does not reject the SSH connection merely because a shell command was requested. It substitutes the dispatcher. The requested shell command did not execute.

```sh
ssh -T -o BatchMode=yes -o IdentitiesOnly=yes \
 -i /work/tmp/brine-spike-pi-runtime/key -l brine-spike \
 -W localhost:2019 "$TARGET" </dev/null
printf 'pty-probe\n' | ssh -tt -o BatchMode=yes -o IdentitiesOnly=yes \
 -i /work/tmp/brine-spike-pi-runtime/key -l brine-spike "$TARGET" sh
```

Trimmed output:

```text
channel 0: open failed: administratively prohibited: open failed
stdio forwarding failed
PTY allocation request failed on channel 0
```

### File ownership alone does not preserve the forced-command boundary

The runner owned its mode-0700 home. The operator owned the mode-0755 `.ssh` directory and mode-0644 `authorized_keys` file. The runner could still replace the whole directory through its writable home:

```sh
runner mv /home/brine-spike/.ssh /home/brine-spike/.ssh.saved
runner mkdir -m 0700 /home/brine-spike/.ssh
runner cp /home/brine-spike/.ssh.saved/authorized_keys \
 /home/brine-spike/.ssh/authorized_keys
runner sh -c 'test -w /home/brine-spike/.ssh/authorized_keys && echo RUNNER_CAN_REPLACE_ROOT_OWNED_KEYS=true'
```

The assertion returned `RUNNER_CAN_REPLACE_ROOT_OWNED_KEYS=true`. This probe copied the same restricted key; it never installed an unrestricted key. The original operator-owned directory was restored before cleanup.

D1's file-ownership wording is insufficient if enrollment puts `authorized_keys` beneath a runner-writable home. This is not an escape demonstrated through the forced command itself. It is a durability failure of the SSH restriction after a runner compromise.

## 7. A transient user operation survives SSH transport loss

An operator SSH session dropped privileges to the runner and launched the following command:

```sh
cd /tmp
sudo -n -u brine-spike env \
 XDG_RUNTIME_DIR=/run/user/1001 \
 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus \
 systemd-run --user --unit=brine-op-test /bin/sh -c \
 'sleep 15; printf operation-finished > /home/brine-spike/op-finished'
echo SSH_READY
exec sleep 120
```

A local Python subprocess waited for `SSH_READY`, terminated its own SSH client process, and waited for that process to exit. A separate SSH session then inspected the user unit. This deliberately dropped a live transport rather than merely letting the original command exit normally.

Trimmed output immediately after transport termination:

```text
Running as unit: brine-op-test.service
SSH_READY
SSH_transport_terminated=255
active
```

A subsequent inspection returned:

```text
operation_marker=operation-finished
Result=success
ExecMainStatus=0
LoadState=not-found
ActiveState=inactive
```

The operation finished and systemd garbage-collected the unit, as D1 anticipates. The retained marker proved completion independently of the unit's continued existence.

The restricted key was not temporarily unrestricted to run this probe. The tested operation ran as the runner but was launched through operator SSH, not through a production `brine host serve` dispatcher. Phase 03 still needs the same disconnect test through the implemented dispatcher and durable operation journal.

## Changes needed before Phase 02 implementation

Do not change DECISIONS.md silently. Add a superseding decision for the SSH path restriction. Suggested replacement wording for D1's ownership sentence:

> The binary, policy, and authorized key file are operator-owned. The runner cannot write them or replace them through any writable parent directory. Enrollment uses an operator-owned runner home with separate runner-writable state and workload directories, or an operator-managed authorized-key location outside the runner's writable tree. Enrollment tests attempted replacement of both the key file and its parent directories.

Required phase-plan additions:

- P02-02 and P06-01 must test writable ancestors, not only the owner and mode of `authorized_keys`. The operator-owned-home option avoids changing SSH configuration for other users.
- P02-02 must ensure the runner command's working directory is accessible and provide the user bus environment for non-login operator sessions. Initial `sudo -u` Podman invocation failed because it inherited an inaccessible operator home. Moving to `/tmp` resolved it without changing other users' permissions.
- P02-03 and P02-06 must retain the full reboot drill. A lingering manager restart is useful evidence but does not satisfy T06 or the phase exit gate. An idle, explicitly authorized window is still required.
- P02-04 must bind and verify both the multi-platform index and the selected platform manifest. `podman inspect .ImageDigest` reported the index digest in this probe.
- P02-05 must probe both container loopback and generated host aliases. It must also cover failed reload and unknown-outcome reconciliation, which this spike did not exercise.
- P03's operation test must disconnect through the real dispatcher, record intent before unit launch, and verify completion after unit garbage collection.
- Enrollment must describe package post-install service activation. Installing Caddy started and enabled it immediately; installing netavark also enabled package-managed units and activated its DHCP proxy socket.

D2, D4, D5, and D6 need no replacement based on the observed behavior. The reboot part of D1 remains an assumption, not a disproved decision.

## Cleanup and exact remaining state

All fixture containers, the BusyBox image, the Podman secret, Quadlet source and generated unit, Caddy generation directories, polkit rule, denied-reload fixture unit, forced-command script and key, runner home, subordinate-ID entries, and linger entry were removed. The local private and public throwaway key files were deleted.

Cleanup stopped the fixture unit before removing its source, reloaded the user manager, removed the secret and image, and confirmed empty container, image, and secret listings. It disabled linger and terminated only the fixture user's manager before `userdel --remove brine-spike`.

The first `userdel` found the manager still shutting down and refused. A read-only inspection then showed `user@1001.service` as `inactive/dead` with no runner processes. The subsequent removal succeeded. No process belonging to another account was terminated.

Caddy's main file was restored to its package-default content, not rewritten as a new configuration. Its MD5 matched the package conffile record, `8cbf072a3e390217a88c242a7f18ee76`. Reloading that clean file removed fixture routes from the package-created autosave before Caddy was stopped and disabled.

Final checks included:

```sh
getent passwd brine-spike
systemctl is-active caddy.service
systemctl is-enabled caddy.service
md5sum /etc/caddy/Caddyfile
sudo -n ss -ltnp
systemctl is-active netavark-dhcp-proxy.service netavark-dhcp-proxy.socket
systemctl is-enabled netavark-dhcp-proxy.service \
 netavark-dhcp-proxy.socket netavark-firewalld-reload.service
```

The runner no longer existed. All six explicitly checked spike paths were absent. There were no listeners on fixture ports 20001 or 8081, or Caddy admin port 2019. Caddy was `inactive` and `disabled`. Its autosave contained no Brine name or fixture port. No runner entry remained in `/etc/subuid` or `/etc/subgid`.

The following installed packages remain, as authorized. Systemd was already installed and was not upgraded:

```text
aardvark-dns buildah caddy catatonit conmon containernetworking-plugins
containers-storage criu crun fuse-overlayfs golang-github-containers-common
golang-github-containers-image libcompel1 libcriu2 libnet1 libnss3-tools
libprotobuf-c1 libprotobuf32t64 libslirp0 libsubid5 libyajl2 netavark passt
podman python3-protobuf python3-pycriu slirp4netns uidmap
```

Package-owned directories, package defaults, Caddy's clean autosave, apt logs, and system journal records remain. Netavark's package-installed DHCP proxy socket remains `active`; its service is `inactive`. The package enables both units and `netavark-firewalld-reload.service`. The latter was linked to an absent `firewalld.service`, not a newly installed firewall. These package defaults were not changed during cleanup. Caddy's package account remains. No fixture-owned runtime resource remains.

## Verification limits

This spike covers the requested runtime probes on one arm64 machine. It is not proof of an amd64 deploy, production authorization, successful R2 restore, reboot recovery, or complete T05/T11/T17 coverage. No VPS, tailnet, firewall, existing interactive session, or unrelated service was modified.
