import { expect, test } from "bun:test";
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const script = resolve(import.meta.dir, "../../../scripts/publish-udp-fixture.ts");
const source = "1".repeat(40);

test("real CLI gates use a synthetic Git boundary and never reach Docker or Go", () => {
  const directory = mkdtempSync(join(process.env.TMPDIR ?? "/tmp", "udp-publish-cli-"));
  try {
    const log = join(directory, "calls");
    writeFileSync(log, "");
    writeFileSync(join(directory, "git"), `#!/bin/sh
printf '%s\\n' "$*" >> "$CALL_LOG"
case "$1" in
  ls-remote) printf '%s\\trefs/heads/main\\n' "$MOCK_MAIN" ;;
  rev-parse) printf '%s\\n' "$MOCK_HEAD" ;;
  status) printf '%s' "$MOCK_DIRTY" ;;
  merge-base) exit 0 ;;
  *) exit 81 ;;
esac
`);
    chmodSync(join(directory, "git"), 0o700);
    refuseHostTools(directory);
    const env = { ...process.env, GITHUB_TOKEN: "", PATH: `${directory}:${process.env.PATH}`, CALL_LOG: log, MOCK_HEAD: source, MOCK_MAIN: source, MOCK_DIRTY: "", GITHUB_EVENT_NAME: "workflow_dispatch", GITHUB_REF: "refs/heads/main", GITHUB_REPOSITORY: "ShaulLavo/brine", GITHUB_SHA: source, INPUT_SOURCE_SHA: source, INPUT_PUBLIC_ACCESS_CONFIRMED: "true", INPUT_BOOTSTRAP_CONFIRMED: "false", GITHUB_RUN_ID: "123", GITHUB_RUN_ATTEMPT: "1" };
    const valid = Bun.spawnSync([process.execPath, script, "gate"], { env, timeout: 5000 });
    expect(valid.exitCode).toBe(0);
    expect(JSON.parse(valid.stdout.toString()).source).toBe(source);
    expect(readFileSync(log, "utf8").trim().split("\n")).toEqual([
      "ls-remote --exit-code origin refs/heads/main", "rev-parse HEAD", "status --porcelain --untracked-files=all",
      `merge-base --is-ancestor 369744357524dc81019afc334a2f9376cf8c87b9 ${source}`,
    ]);
    checkRejectedCLI(env, directory);
    expect(readFileSync(log, "utf8")).not.toMatch(/^(docker|go) /m);
    const bootstrap = Bun.spawnSync([process.execPath, script, "bootstrap-preflight"], { env: { ...env, INPUT_PUBLIC_ACCESS_CONFIRMED: "false", INPUT_BOOTSTRAP_CONFIRMED: "true" }, timeout: 5000 });
    expect(bootstrap.exitCode).toBe(1);
    expect(bootstrap.stderr.toString()).toContain("GITHUB_TOKEN");
  } finally {
    rmSync(directory, { recursive: true });
  }
});

function checkRejectedCLI(env: Record<string, string | undefined>, directory: string): void {
  for (const changed of [{ GITHUB_EVENT_NAME: "push" }, { INPUT_BOOTSTRAP_CONFIRMED: "true" }, { INPUT_SOURCE_SHA: "main" }, { INPUT_PUBLIC_ACCESS_CONFIRMED: "false" }, { INPUT_PUBLIC_ACCESS_CONFIRMED: "false", INPUT_BOOTSTRAP_CONFIRMED: "false" }, { MOCK_HEAD: "2".repeat(40) }, { MOCK_MAIN: "2".repeat(40) }, { MOCK_DIRTY: "?? new.ts" }]) {
    const result = Bun.spawnSync([process.execPath, script, "gate"], { env: { ...env, ...changed }, timeout: 5000 });
    expect(result.exitCode).toBe(1);
  }
  const qualify = Bun.spawnSync([process.execPath, script, "qualify", "s390x", join(directory, "must-not-exist")], { env, timeout: 5000 });
  expect(qualify.exitCode).toBe(1);
  expect(qualify.stderr.toString()).toContain("Expected amd64 or arm64");
}

function refuseHostTools(directory: string): void {
  for (const name of ["docker", "go"]) {
    writeFileSync(join(directory, name), `#!/bin/sh\nprintf '${name} %s\\n' "$*" >> "$CALL_LOG"\nexit 99\n`);
    chmodSync(join(directory, name), 0o700);
  }
}
