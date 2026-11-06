import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, existsSync, cpSync } from "node:fs";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";

const repo = resolve(import.meta.dir, "../../..");
const roots: string[] = [];
afterEach(() => { for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true }); });

function git(root: string, ...args: string[]) {
  const result = Bun.spawnSync(["git"].concat(args), { cwd: root, env: { ...process.env, GIT_CONFIG_GLOBAL: "/dev/null", GIT_CONFIG_NOSYSTEM: "1" } });
  if (result.exitCode !== 0) throw new Error(result.stderr.toString());
  return result.stdout.toString().trim();
}

function fixture() {
  const root = mkdtempSync(join(tmpdir(), "brine-release-test-"));
  roots.push(root);
  const source = join(root, "source");
  mkdirSync(join(source, "scripts"), { recursive: true });
  mkdirSync(join(source, "internal/cli"), { recursive: true });
  mkdirSync(join(root, "bin"));
  for (const file of ["build-release.sh", "build-release.ts"]) cpSync(join(repo, "scripts", file), join(source, "scripts", file));
  writeFileSync(join(source, "internal/cli/root.go"), 'package cli\nconst Version = "0.1.74-dev"\n');
  writeFileSync(join(source, "mise.toml"), '[tools]\ngo = "1.27.3"\n');
  writeFileSync(join(source, "go.mod"), 'module fixture\n\ngo 1.26.0\n');
  git(source, "init", "-q");
  git(source, "add", ".");
  git(source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture");
  writeFileSync(join(root, "bin/go"), `#!/usr/bin/env bun
import { writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
const args = process.argv.slice(2);
if (args[0] === "version") { process.stdout.write("go version " + (process.env.FIXTURE_GO_VERSION ?? "go1.27.3 linux/amd64") + "\\n"); process.exit(0); }
if (args[0] === "env") { process.stdout.write(args.length === 2 ? (args[1] === "GOHOSTOS" ? "linux\\n" : "amd64\\n") : "linux\\namd64\\n"); process.exit(0); }
if (args[0] !== "build") process.exit(2);
if (process.env.FIXTURE_CHANGE === "dirty") writeFileSync("untracked", "changed");
if (process.env.FIXTURE_CHANGE === "head") spawnSync("git", ["-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "moved"]);
process.stderr.write("fixture executor stopped after source mutation\\n");
process.exit(1);
`, { mode: 0o755 });
  return { root, source, output: join(root, "output") };
}

function build(f: ReturnType<typeof fixture>, tag = "v0.1.74-dev", change = "", extra: Record<string, string> = {}) {
  return Bun.spawnSync(["bash", "scripts/build-release.sh", f.output], { cwd: f.source, env: {
    ...process.env, PATH: `${join(f.root, "bin")}:${process.env.PATH}`, BRINE_RELEASE_TAG: tag, FIXTURE_CHANGE: change, ...extra,
  }, timeout: 5000 });
}

test("version/tag mismatch refuses before build or output creation", () => {
  const f = fixture();
  const result = build(f, "v0.1.75");
  expect(result.exitCode).not.toBe(0);
  expect(result.stderr.toString()).toContain("version/tag mismatch");
  expect(existsSync(f.output)).toBe(false);
});

test("dirty checkout refuses before build", () => {
  const f = fixture();
  writeFileSync(join(f.source, "untracked"), "dirty");
  const result = build(f);
  expect(result.exitCode).not.toBe(0);
  expect(result.stderr.toString()).toContain("clean checkout");
  expect(existsSync(f.output)).toBe(false);
});

for (const change of ["dirty", "head"]) {
  test(`source ${change} movement refuses and removes partial output even after compiler failure`, () => {
    const f = fixture();
    const result = build(f, "v0.1.74-dev", change);
    expect(result.exitCode).not.toBe(0);
    expect(result.stderr.toString()).toContain("source changed during the build");
    expect(existsSync(f.output)).toBe(false);
  });
}


test("wrong Go pin or host refuses before creating output", () => {
  for (const pin of ["go1.27.2 linux/amd64", "go1.27.3 darwin/arm64"]) {
    const f = fixture();
    const result = build(f, "v0.1.74-dev", "", { FIXTURE_GO_VERSION: pin });
    expect(result.exitCode).not.toBe(0);
    expect(result.stderr.toString()).toContain("requires pinned go1.27.3 on Linux amd64");
    expect(existsSync(f.output)).toBe(false);
  }
});

test("existing output is never adopted or removed", () => {
  const f = fixture();
  mkdirSync(f.output);
  writeFileSync(join(f.output, "owner-data"), "keep");
  const result = build(f);
  expect(result.exitCode).not.toBe(0);
  expect(result.stderr.toString()).toContain("output must not exist");
  expect(existsSync(join(f.output, "owner-data"))).toBe(true);
});

test("ignored source input still refuses a supposedly clean checkout", () => {
  const f = fixture();
  writeFileSync(join(f.source, ".git/info/exclude"), "ignored.go\n");
  writeFileSync(join(f.source, "ignored.go"), "ignored but compiled");
  const result = build(f);
  expect(result.exitCode).not.toBe(0);
  expect(result.stderr.toString()).toContain("clean checkout");
  expect(existsSync(f.output)).toBe(false);
});
