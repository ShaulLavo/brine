import { afterEach, expect, test } from "bun:test";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { artifacts, cleanupFixtures, releaseCommand, releaseFixture } from "./fixture.ts";

afterEach(cleanupFixtures);

function calls(root: string) {
  const path = join(root, "calls");
  return existsSync(path) ? readFileSync(path, "utf8").trim().split("\n") : [];
}

for (const mutation of ["tracked", "untracked", "ignored"]) {
  test(`verification and comparison refuse ${mutation} source changes`, async () => {
    const fixture = releaseFixture();
    const first = await artifacts(fixture);
    const second = await artifacts(fixture, "second");
    if (mutation === "tracked") writeFileSync(join(fixture.checkout, "mise.toml"), "changed");
    if (mutation === "untracked") writeFileSync(join(fixture.checkout, "untracked"), "changed");
    if (mutation === "ignored") {
      writeFileSync(join(fixture.checkout, ".git/info/exclude"), "ignored\n");
      writeFileSync(join(fixture.checkout, "ignored"), "changed");
    }
    const commands = [{ mode: "verify", paths: [first] }, { mode: "compare", paths: [first, second] }];
    for (const command of commands) dirtyRefusal(fixture, command);
  });
}

function dirtyRefusal(fixture: ReturnType<typeof releaseFixture>, command: { mode: string; paths: string[] }) {
  const result = releaseCommand(fixture, command.mode, command.paths);
  expect(result.exitCode).toBe(1);
  expect(result.stderr.toString()).toContain("clean checkout");
}

for (const mode of ["dirty-ci", "head-ci", "error-dirty-ci", "dirty-absence", "head-absence"]) {
  test(`publication keeps source binding through ${mode}`, async () => {
    const fixture = releaseFixture();
    const directory = await artifacts(fixture);
    const result = releaseCommand(fixture, "publish", [directory], mode);
    expect(result.exitCode).toBe(1);
    expect(result.stderr.toString()).toContain("source changed");
    expect(calls(fixture.root)).not.toContain("publish");
  });
}

for (const mode of ["oversized", "oversized-length", "oversized-short-length"]) {
  test(`GitHub ${mode} response stops reading at the byte bound`, async () => {
    const fixture = releaseFixture();
    const directory = await artifacts(fixture);
    const result = releaseCommand(fixture, "publish", [directory], mode);
    expect(result.exitCode).toBe(1);
    expect(result.stderr.toString()).toContain("GitHub response exceeds 1 MiB");
    expect(calls(fixture.root).filter(call => call === "chunk").length).toBeLessThanOrEqual(18);
    expect(calls(fixture.root)).toContain("cancel");
    expect(calls(fixture.root)).not.toContain("publish");
  });
}

test("clean source publishes exactly once and an unknown create is never retried", async () => {
  const fixture = releaseFixture();
  const directory = await artifacts(fixture);
  const result = releaseCommand(fixture, "publish", [directory]);
  expect(result.exitCode).toBe(0);
  expect(calls(fixture.root).filter(call => call === "publish")).toHaveLength(1);
  const failed = releaseCommand(fixture, "publish", [directory], "clean", { FIXTURE_PUBLISH_FAIL: "1" });
  expect(failed.exitCode).toBe(1);
  expect(calls(fixture.root).filter(call => call === "publish")).toHaveLength(2);
});

for (const mode of ["existing", "unknown", "pending", "failed", "wrong-tag", "malformed", "stream-error"]) {
  test(`publication refuses ${mode} without publisher or response-body disclosure`, async () => {
    const fixture = releaseFixture();
    const directory = await artifacts(fixture);
    const result = releaseCommand(fixture, "publish", [directory], mode);
    expect(result.exitCode).toBe(1);
    expect(result.stderr.toString()).not.toContain("private body diagnostic");
    expect(calls(fixture.root)).not.toContain("publish");
  });
}

for (const mutation of ["hash-dirty", "hash-head"]) {
  test(`verification and comparison keep source binding during ${mutation}`, async () => {
    const fixture = releaseFixture();
    const first = await artifacts(fixture);
    const second = await artifacts(fixture, "second");
    const mode = mutation === "hash-dirty" ? "verify" : "compare";
    const paths = mode === "verify" ? [first] : [first, second];
    const result = releaseCommand(fixture, mode, paths, mutation);
    expect(result.exitCode).toBe(1);
    expect(result.stderr.toString()).toContain("source changed");
  });
}
