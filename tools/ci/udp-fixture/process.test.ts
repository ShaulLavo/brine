import { expect, test } from "bun:test";
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const helper = resolve(import.meta.dir, "../../../scripts/publish-udp-fixture.ts");
const valid = "PID UID GID COMMAND\n21017 65532 65532 /udp-echo-server\n";

test("runtime process qualification supplies Docker's required PID column and verifies one non-root server", () => {
  const directory = mkdtempSync(join(process.env.TMPDIR ?? "/tmp", "udp-process-"));
  try {
    writeFileSync(join(directory, "docker"), `#!/bin/sh
case "$*" in
  'top brine-udp-process-test -eo pid,uid,gid,args') printf '%s' "$PROCESS_IDENTITY" ;;
  *) printf "Error response from daemon: Couldn't find PID field in ps output\\n" >&2; exit 1 ;;
esac
`);
    chmodSync(join(directory, "docker"), 0o700);
    const result = inspect(directory, valid);
    expect(result.stderr.toString()).toBe("");
    expect(result.exitCode).toBe(0);
    expect(result.stdout.toString()).toBe(valid.trim());
    for (const output of rejectedListings()) {
      const rejected = inspect(directory, output);
      expect(rejected.exitCode).toBe(1);
      expect(rejected.stderr.toString()).toContain("Running process must");
    }
  } finally {
    rmSync(directory, { recursive: true });
  }
});

function inspect(directory: string, output: string) {
  return Bun.spawnSync([process.execPath, "-e", `import { inspectProcessIdentity } from ${JSON.stringify(helper)}; process.stdout.write(inspectProcessIdentity("brine-udp-process-test"));`], {
    env: { ...process.env, PATH: `${directory}:${process.env.PATH}`, PROCESS_IDENTITY: output },
    timeout: 5000,
  });
}

function rejectedListings(): string[] {
  return [
    "", "PID UID GID COMMAND\n", valid + "21018 65532 65532 /udp-echo-server\n",
    valid.replace("21017", "0"), valid.replace("21017", "unknown"),
    valid.replace("65532 65532", "0 65532"), valid.replace("65532 65532", "65532 0"),
    valid.replace("65532 65532", "nobody nogroup"),
    valid.replace("/udp-echo-server", "/bin/sh"), valid.replace("/udp-echo-server", "/udp-echo-server-other"),
    valid.replace("PID UID GID COMMAND", "UID GID PID COMMAND"),
  ];
}
