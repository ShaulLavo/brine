#!/usr/bin/env python3
"""Manual fixture-only native gate. No host service or public spec admission."""
import argparse
import hashlib
import json
import os
import pathlib
import platform
import shutil
import signal
import socket
import stat
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

PINS = json.loads(pathlib.Path(__file__).with_name("pins.json").read_text())
MAX_BYTES = 16 << 20
MAX_FILES = 1024
DB = "brine-runtime-fixture"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def request(port, path, token=None, data=None, method="GET"):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(f"http://127.0.0.1:{port}/v1/{path}", data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=5) as response:
            raw = response.read((1 << 20) + 1)
            if len(raw) > 1 << 20:
                raise RuntimeError("fixture response budget")
            return response.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as error:
        error.read((1 << 20) + 1)
        return error.code, None


def wait_health(process, port):
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("fixture exited before readiness")
        try:
            if owns_listener(process.pid, port) and request(port, "health")[0] == 200:
                return
        except (urllib.error.URLError, TimeoutError):
            pass
        time.sleep(.05)
    raise RuntimeError("fixture readiness budget")


def owns_listener(pid, port):
    process = pathlib.Path("/proc") / str(pid)
    sockets = set()
    for descriptor in (process / "fd").iterdir():
        try:
            target = descriptor.readlink().as_posix()
        except OSError:
            continue
        if target.startswith("socket:["):
            sockets.add(target[8:-1])
    for line in (process / "net/tcp").read_text().splitlines()[1:]:
        fields = line.split()
        if fields[1] == f"0100007F:{port:04X}" and fields[3] == "0A" and fields[9] in sockets:
            return True
    return False


def stop(process):
    if process.poll() is not None:
        return {"exit": process.returncode, "forced": False, "clean_candidate": False}
    process.send_signal(signal.SIGINT)
    forced = False
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        forced = True
        process.kill()
        process.wait(timeout=5)
    return {"exit": process.returncode, "forced": forced, "clean_candidate": process.returncode == 0 and not forced}


def tree_seal(root):
    rows = []
    total = 0
    for path in sorted(root.rglob("*")):
        info = path.lstat()
        if path.is_symlink() or info.st_uid != os.getuid() or not (path.is_dir() or path.is_file()):
            raise RuntimeError("fixture tree ownership or type")
        if not path.is_file():
            continue
        total += info.st_size
        rows.append([str(path.relative_to(root)), info.st_size, digest(path)])
        if total > MAX_BYTES or len(rows) > MAX_FILES:
            raise RuntimeError("fixture tree budget")
    return hashlib.sha256(json.dumps(rows, separators=(",", ":")).encode()).hexdigest()


class Fixture:
    def __init__(self, args):
        self.args = args
        self.root = pathlib.Path(tempfile.mkdtemp(prefix="brine-spacetime-runtime-", dir=args.scratch))
        self.root.chmod(0o700)
        self.keys = self.root / "keys"
        self.keys.mkdir(mode=0o700)
        self.processes = []
        self.logs = []
        self.publisher = None
        self.user = None
        self.expected = None
        self.environment = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "LANG": "C.UTF-8", "TOKIO_WORKER_THREADS": "2",
                            "TMPDIR": str(self.root), "XDG_CONFIG_HOME": str(self.root / "config"),
                            "XDG_DATA_HOME": str(self.root / "data-home"), "BUN_INSTALL_CACHE_DIR": str(self.root / "bun-cache")}

    def start(self, state, label):
        port = free_port()
        log = (self.root / (label + "-private.log")).open("w")
        self.logs.append(log)
        process = subprocess.Popen([str(self.args.runtime), "start", "--listen-addr", f"127.0.0.1:{port}",
                                    "--data-dir", str(state), "--jwt-pub-key-path", str(self.keys / "public.pem"),
                                    "--jwt-priv-key-path", str(self.keys / "private.pem"),
                                    "--page_pool_max_size", "67108864", "--non-interactive"],
                                   stdout=log, stderr=log, env=self.environment)
        self.processes.append(process)
        wait_health(process, port)
        return process, port

    def bootstrap(self):
        subprocess.run(["openssl", "genpkey", "-algorithm", "EC", "-pkeyopt", "ec_paramgen_curve:prime256v1",
                        "-out", str(self.keys / "private.pem")], check=True, capture_output=True)
        subprocess.run(["openssl", "pkey", "-in", str(self.keys / "private.pem"), "-pubout", "-out", str(self.keys / "public.pem")],
                       check=True, capture_output=True)
        (self.keys / "private.pem").chmod(0o600)
        source = self.root / "source"
        source.mkdir(mode=0o700)
        (source / "config.toml").write_text('[commitlog]\nmax-segment-size = 32768\nwrite-buffer-size = 4096\n[module-http]\nenabled = false\n[logs]\nlevel = "info"\n')
        process, port = self.start(source, "source-bootstrap")
        _, self.publisher = request(port, "identity", data=b"", method="POST")
        status, _ = request(port, f"database/{DB}?host_type=Wasm", self.publisher["token"], self.args.module.read_bytes(), "PUT")
        if status != 200:
            raise RuntimeError("fixture publication refused")
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            status, info = request(port, f"database/{DB}", self.publisher["token"])
            if status == 200:
                break
            time.sleep(.05)
        self.expected = {"database_identity": info["database_identity"]["__identity__"][2:],
                         "publisher_identity": info["owner_identity"]["__identity__"][2:], "names": [DB]}
        self.observation(port)
        _, self.user = request(port, "identity", data=b"", method="POST")
        identity = {"__identity__": "0x" + self.user["identity"].removeprefix("0x")}
        status, _ = request(port, f"database/{DB}/call/authorize_writer", self.publisher["token"], json.dumps([identity]).encode(), "POST")
        if status != 200:
            raise RuntimeError("fixture writer authorization refused")
        token_file = self.root / "user-token"
        token_file.write_text(self.user["token"])
        token_file.chmod(0o600)
        self.client(port, "write", 1)
        stopping = stop(process)
        if process.poll() is None:
            raise RuntimeError("source remains running")
        checkpoint = self.root / "metadata-checkpoint"
        checkpoint.mkdir(mode=0o700)
        for name in ["control-db", "program-bytes"]:
            shutil.copytree(source / name, checkpoint / name)
        for name in ["config.toml", "metadata.toml"]:
            shutil.copy2(source / name, checkpoint / name)
        return source, stopping

    def observation(self, port):
        payload = dict(self.expected, host="127.0.0.1", port=port, publisher_credential=self.publisher["token"])
        result = subprocess.run([str(self.args.helper), "--mode", "observe"], input=json.dumps(payload).encode(),
                                capture_output=True, timeout=20)
        if result.returncode:
            raise RuntimeError("shared metadata helper refused")
        return json.loads(result.stdout)

    def client(self, port, action, sequence=None):
        command = [str(self.args.bun), str(self.args.client), f"http://127.0.0.1:{port}", DB, action]
        if sequence is not None:
            command.append(str(sequence))
        result = subprocess.run(command, env=dict(self.environment, BRINE_USER_TOKEN_FILE=str(self.root / "user-token")),
                                capture_output=True, timeout=15)
        if result.returncode:
            raise RuntimeError("fixed confirmed fixture client refused")
        evidence = json.loads(result.stdout)
        if evidence.get("kind") != "confirmed_subscription" or evidence.get("confirmedReads") is not True:
            raise RuntimeError("unconfirmed evidence")
        return evidence

    def capture(self, source, label, include_head):
        replicas = list((source / "replicas").glob("*/clog"))
        if len(replicas) != 1:
            raise RuntimeError("expected one discovered replica")
        replica = replicas[0].parent
        logs = sorted(replicas[0].glob("*.stdb.log"))
        head = int(logs[-1].name.split(".")[0])
        snapshots = sorted((replica / "snapshots").glob("*.snapshot_dir/*.snapshot_bsatn"))
        eligible = [path for path in snapshots if int(path.name.split(".")[0]) < head]
        if not eligible:
            raise RuntimeError("no finalized snapshot before active head")
        snapshot = eligible[-1]
        offset = int(snapshot.name.split(".")[0])
        containing = [path for path in logs[:-1] if int(path.name.split(".")[0]) <= offset][-1]
        selected = [path for path in logs if path >= containing and (include_head or path != logs[-1])]
        dest = self.root / label
        shutil.copytree(self.root / "metadata-checkpoint", dest)
        shutil.copytree(snapshot.parent, dest / "replicas" / replica.name / "snapshots" / snapshot.parent.name)
        target = dest / "replicas" / replica.name / "clog"
        target.mkdir(parents=True)
        members = {}
        for path in selected:
            descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
            with os.fdopen(descriptor, "rb") as stream:
                before = os.fstat(stream.fileno())
                if not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid() or before.st_size > MAX_BYTES:
                    raise RuntimeError("source file ownership or budget")
                raw = stream.read(before.st_size)
                stream.seek(0)
                repeated = stream.read(before.st_size)
                after = os.fstat(stream.fileno())
            if len(raw) != before.st_size or raw != repeated or before.st_ino != after.st_ino or before.st_dev != after.st_dev or after.st_size < before.st_size:
                raise RuntimeError("source log changed during bounded copy")
            (target / path.name).write_bytes(raw)
            members[path.name] = {"bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()}
        tree_seal(dest)
        return dest, {"replica": replica.name, "snapshot_offset": offset, "head_start_offset": head, "includes_head": include_head, "members": members}

    def replay(self, tree, label):
        process, port = self.start(tree, label)
        observation = self.observation(port)
        evidence = self.client(port, "read")
        stopped = stop(process)
        return {"observation": observation, "evidence": evidence, "stop": stopped}

    def dispose(self):
        for process in self.processes:
            if process.poll() is None:
                stop(process)
        for log in self.logs:
            log.close()


def validate_inputs(args):
    machine = platform.machine()
    architecture = {"x86_64": "linux/amd64", "aarch64": "linux/arm64"}.get(machine)
    if architecture is None or digest(args.runtime) != PINS["platforms"][architecture]["standalone_sha256"]:
        raise RuntimeError("pinned native runtime mismatch")
    if digest(args.module) != args.module_sha256 or digest(args.helper) != args.helper_sha256:
        raise RuntimeError("explicit artifact hash mismatch")
    version = subprocess.run([str(args.bun), "--version"], capture_output=True, text=True, check=True, timeout=5).stdout.strip()
    if version != PINS["bun"]:
        raise RuntimeError("pinned fixture client runtime mismatch")
    repository = pathlib.Path(os.environ.get("BRINE_REPOSITORY_ROOT", pathlib.Path(__file__).resolve().parents[2])).resolve()
    if args.scratch.resolve().is_relative_to(repository):
        raise RuntimeError("private fixture scratch cannot be in the repository")


def run(args):
    validate_inputs(args)
    fixture = Fixture(args)
    result = {"runtime_version": PINS["runtime_version"], "runtime_sha256": digest(args.runtime),
              "module_sha256": digest(args.module), "helper_sha256": digest(args.helper)}
    try:
        source, result["bootstrap_stop"] = fixture.bootstrap()
        process, port = fixture.start(source, "source-online")
        for sequence in range(2, 181):
            status, _ = request(port, f"database/{DB}/call/append", fixture.user["token"], json.dumps([sequence]).encode(), "POST")
            if status != 200:
                raise RuntimeError("bounded fixture write refused")
        expected = fixture.client(port, "verify", 180)
        current = fixture.observation(port)
        completed, closed_receipt = fixture.capture(source, "closed", False)
        full, full_receipt = fixture.capture(source, "head", True)
        result["source_stop"] = stop(process)
        result["source_rows"] = len(expected["rows"])
        result["captures"] = {"closed": closed_receipt, "head": full_receipt}
        result["closed"] = fixture.replay(completed, "closed-replay")
        result["head"] = fixture.replay(full, "head-replay")
        if result["head"]["evidence"] != expected or result["head"]["observation"] != current:
            raise RuntimeError("captured point overstated coverage or metadata")
        result["exact_point_candidate"] = False
        result["unknown_stop_never_exact"] = True
        result["guarded_replay"] = guarded_replay(fixture, source, current)
        result["passed"] = True
    finally:
        fixture.dispose()
        (fixture.root / "result.json").write_text(json.dumps(result, indent=2))
    print(json.dumps(result))


def guarded_replay(fixture, source, expected):
    from startup_probe import guarded_cases
    return guarded_cases(fixture, source, expected)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", type=pathlib.Path, required=True)
    parser.add_argument("--module", type=pathlib.Path, required=True)
    parser.add_argument("--module-sha256", required=True)
    parser.add_argument("--helper", type=pathlib.Path, required=True)
    parser.add_argument("--helper-sha256", required=True)
    parser.add_argument("--client", type=pathlib.Path, required=True)
    parser.add_argument("--bun", type=pathlib.Path, required=True)
    parser.add_argument("--scratch", type=pathlib.Path, required=True)
    args = parser.parse_args()
    try:
        run(args)
    except Exception:
        raise SystemExit("native runtime probe refused; inspect the selected private scratch directory") from None


if __name__ == "__main__":
    main()
