import { cpSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { digest, goPin, source, targets } from "../../../scripts/build-release.ts";

const roots: string[] = [];
export function cleanupFixtures() {
  for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true });
}
const repo = resolve(import.meta.dir, "../../..");

export function git(cwd: string, ...args: string[]) {
  const result = Bun.spawnSync(["git"].concat(args), { cwd, env: { ...process.env, GIT_CONFIG_GLOBAL: "/dev/null", GIT_CONFIG_NOSYSTEM: "1" } });
  if (result.exitCode !== 0) throw new Error(result.stderr.toString());
  return result.stdout.toString().trim();
}

export function releaseFixture() {
  const root = mkdtempSync(join(tmpdir(), "brine-release-runtime-"));
  roots.push(root);
  const checkout = join(root, "source");
  mkdirSync(join(checkout, "scripts"), { recursive: true });
  mkdirSync(join(checkout, "internal/cli"), { recursive: true });
  for (const name of ["build-release.ts", "release.ts"]) cpSync(join(repo, "scripts", name), join(checkout, "scripts", name));
  cpSync(join(repo, "tools/ci/release/api.fixture.ts"), join(checkout, "scripts/api-fixture.ts"));
  writeFileSync(join(checkout, "internal/cli/root.go"), 'const Version = "0.1.74"\n');
  writeFileSync(join(checkout, "mise.toml"), '[tools]\ngo = "1.27.3"\n');
  git(checkout, "init", "-q");
  git(checkout, "add", ".");
  git(checkout, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture");
  mkdirSync(join(root, "bin"));
  writeFileSync(join(root, "bin/gh"), `#!/usr/bin/env bun\nimport { appendFileSync } from "node:fs";\nappendFileSync(process.env.FIXTURE_CALLS, "publish\\n");\nprocess.exit(process.env.FIXTURE_PUBLISH_FAIL ? 1 : 0);\n`, { mode: 0o755 });
  return { root, checkout, tag: "v0.1.74", ...source(checkout) };
}

export async function artifacts(fixture: ReturnType<typeof releaseFixture>, name = "artifacts") {
  const directory = join(fixture.root, name);
  mkdirSync(directory);
  const entries = [];
  for (const target of targets) {
    const name = `brine-${target.os}-${target.arch}`;
    const bytes = Buffer.alloc(32);
    if (target.os === "linux") { bytes.set([127, 69, 76, 70, 2, 1]); bytes.writeUInt16LE(target.machine, 18); }
    if (target.os === "darwin") { bytes.writeUInt32LE(0xfeedfacf, 0); bytes.writeUInt32LE(target.machine, 4); }
    writeFileSync(join(directory, name), bytes);
    entries.push({ name, os: target.os, architecture: target.arch, ...await digest(join(directory, name)) });
  }
  writeFileSync(join(directory, "receipt.json"), JSON.stringify({ schema_version: 1, source_head: fixture.head, source_tree: fixture.tree, version: "0.1.74", tag: fixture.tag, go_version: goPin(fixture.checkout), vcs_modified: false, artifacts: entries }, null, 2) + "\n");
  writeFileSync(join(directory, "SHA256SUMS"), entries.map(item => `${item.sha256}  ${item.name}\n`).join("") + `${(await digest(join(directory, "receipt.json"))).sha256}  receipt.json\n`);
  return directory;
}

export function releaseCommand(fixture: ReturnType<typeof releaseFixture>, mode: string, paths: string[], apiMode = "clean", extra: Record<string, string> = {}) {
  return Bun.spawnSync([process.execPath, "--preload", "./scripts/api-fixture.ts", "scripts/release.ts", mode].concat(paths), {
    cwd: fixture.checkout, timeout: 10_000,
    env: { ...process.env, PATH: `${join(fixture.root, "bin")}:${process.env.PATH}`, GH_TOKEN: "fixture", BRINE_RELEASE_TAG: fixture.tag,
      GITHUB_REPOSITORY: "ShaulLavo/brine", GITHUB_SHA: fixture.head, GITHUB_EVENT_NAME: "push", GITHUB_REF: `refs/tags/${fixture.tag}`,
      FIXTURE_CALLS: join(fixture.root, "calls"), FIXTURE_API_MODE: apiMode, ...extra },
  });
}
