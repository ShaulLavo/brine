"""Private replay experiment, not a production startup authority constructor."""
import json
import os
import pathlib
import shutil
import socket
import subprocess
import sys


def guarded_cases(fixture, source, expected):
    from probe import tree_seal
    original = tree_seal(source)
    results = {}
    cases = [("intact", False, True), ("missing-with-bootstrap", True, False), ("missing-withheld", True, True)]
    for label, missing, withhold in cases:
        tree = fixture.root / ("startup-" + label)
        shutil.copytree(source, tree)
        if missing:
            shutil.rmtree(tree / "replicas")
        if withhold:
            (tree / "program-bytes").rename(fixture.root / (label + "-withheld-programs"))
        results[label] = sandbox(fixture, tree, expected)
        if tree_seal(source) != original:
            raise RuntimeError("private verification changed original source")
    if not results["intact"]["matches"] or not results["missing-with-bootstrap"]["matches"] or results["missing-withheld"]["matches"]:
        raise RuntimeError("initialized history guard did not distinguish bootstrap")
    return {"cases": results, "original_unchanged": True, "original_process_stopped": True}


def sandbox(fixture, tree, expected):
    script = pathlib.Path(__file__).resolve().parent
    args = ["bwrap", "--unshare-all", "--new-session", "--die-with-parent", "--clearenv", "--setenv", "PATH", "/usr/bin:/bin",
            "--setenv", "TMPDIR", "/tmp", "--setenv", "XDG_CONFIG_HOME", "/tmp/config", "--setenv", "TOKIO_WORKER_THREADS", "2",
            "--ro-bind", "/usr", "/usr", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib", "/lib64",
            "--symlink", "usr/bin", "/bin", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
            "--ro-bind", str(fixture.args.runtime.resolve()), "/engine", "--ro-bind", str(fixture.args.helper.resolve()), "/helper",
            "--bind", str(tree), "/data", "--ro-bind", str(fixture.keys), "/keys",
            "--ro-bind", str(script / "probe.py"), "/probe.py", "--ro-bind", str(script / "pins.json"), "/pins.json",
            "--ro-bind", str(script / "startup_probe.py"), "/inspect.py", "/usr/bin/python3", "/inspect.py", "--inspect"]
    result = subprocess.run(args, input=json.dumps({"publisher_credential": fixture.publisher["token"], "expected": expected}).encode(),
                            capture_output=True, timeout=30)
    if result.returncode:
        raise RuntimeError("private sandbox failed or exceeded budget")
    return json.loads(result.stdout)


def inspect():
    from probe import free_port, stop, wait_health
    if pathlib.Path("/work").exists() or set(name for _, name in socket.if_nameindex()) != {"lo"}:
        raise RuntimeError("source or external network accessible")
    private = json.load(sys.stdin)
    expected = private["expected"]
    port = free_port()
    log = pathlib.Path("/data/verification-private.log").open("w")
    process = subprocess.Popen(["/engine", "start", "--listen-addr", f"127.0.0.1:{port}", "--data-dir", "/data",
                                "--jwt-pub-key-path", "/keys/public.pem", "--jwt-priv-key-path", "/keys/private.pem",
                                "--page_pool_max_size", "67108864", "--non-interactive"], stdout=log, stderr=log)
    observed = None
    try:
        wait_health(process, port)
        packet = {"host": "127.0.0.1", "port": port, "database_identity": expected["database_identity"],
                  "publisher_identity": expected["publisher_identity"], "names": expected["names"],
                  "publisher_credential": private["publisher_credential"]}
        result = subprocess.run(["/helper", "--mode", "observe"], input=json.dumps(packet).encode(), capture_output=True, timeout=20)
        if result.returncode == 0:
            observed = json.loads(result.stdout)
    finally:
        stopped = stop(process)
        log.close()
    print(json.dumps({"matches": observed == expected, "private_process_gone": process.poll() is not None,
                      "network_only_loopback": True, "original_absent_from_mounts": True, "shutdown": stopped}))


if __name__ == "__main__":
    inspect()
