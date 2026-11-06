import { afterEach, expect, test } from "bun:test";
import { writeFileSync, rmSync, readFileSync, symlinkSync } from "node:fs";
import { join } from "node:path";
import { goPin } from "../../../scripts/build-release.ts";
import { artifacts, cleanupFixtures, releaseCommand, releaseFixture } from "./fixture.ts";

afterEach(cleanupFixtures);

async function verify(fixture: ReturnType<typeof releaseFixture>, directory: string) {
  const result = releaseCommand(fixture, "verify", [directory]);
  if (result.exitCode !== 0) throw new Error(result.stderr.toString());
  return readFileSync(join(directory, "SHA256SUMS"), "utf8");
}

async function compare(fixture: ReturnType<typeof releaseFixture>, first: string, second: string) {
  const result = releaseCommand(fixture, "compare", [first, second]);
  if (result.exitCode !== 0) throw new Error(result.stderr.toString());
}

test("receipt binds exact source, version, Go pin, matrix, format, size and bytes", async () => {
  const fixture = releaseFixture();
  const directory = await artifacts(fixture);
  expect(await verify(fixture, directory)).toContain("brine-darwin-arm64");
  const receiptPath = join(directory, "receipt.json");
  const original = readFileSync(receiptPath, "utf8");
  for (const [old, replacement] of [[fixture.head, "b".repeat(40)], [fixture.tree, "c".repeat(40)], [goPin(fixture.checkout), "go0.0.0"], ['"vcs_modified": false', '"vcs_modified": true'], ['"schema_version": 1', '"schema_version": 2'], ['"bytes": 32', '"bytes": 33']]) {
    writeFileSync(receiptPath, original.replace(old, replacement));
    await expect(verify(fixture, directory)).rejects.toThrow(/receipt/);
  }
  writeFileSync(receiptPath, original);
  writeFileSync(join(directory, "brine-linux-amd64"), Buffer.alloc(32));
  await expect(verify(fixture, directory)).rejects.toThrow(/executable format/);
});

test("compare validates actual files before comparing manifests", async () => {
  const fixture = releaseFixture();
  const first = await artifacts(fixture);
  const second = await artifacts(fixture, "second");
  await expect(compare(fixture, first, second)).resolves.toBeUndefined();
  const path = join(second, "brine-darwin-arm64");
  const bytes = readFileSync(path);
  bytes[31] = 1;
  writeFileSync(path, bytes);
  await expect(compare(fixture, first, second)).rejects.toThrow(/artifact bytes/);
});

test("extra files, symlinks, oversized receipt and altered checksums refuse", async () => {
  const fixture = releaseFixture();
  const directory = await artifacts(fixture);
  const sumPath = join(directory, "SHA256SUMS");
  const sums = readFileSync(sumPath, "utf8");
  writeFileSync(sumPath, "0" + sums.slice(1));
  await expect(verify(fixture, directory)).rejects.toThrow(/SHA256SUMS/);
  writeFileSync(sumPath, sums);
  writeFileSync(join(directory, "extra"), "unlisted");
  await expect(verify(fixture, directory)).rejects.toThrow(/unexpected release files/);
  rmSync(join(directory, "extra"));
  const originalArm = readFileSync(join(directory, "brine-linux-arm64"));
  rmSync(join(directory, "brine-linux-arm64"));
  symlinkSync("brine-linux-amd64", join(directory, "brine-linux-arm64"));
  await expect(verify(fixture, directory)).rejects.toThrow();
  rmSync(join(directory, "brine-linux-arm64"));
  writeFileSync(join(directory, "brine-linux-arm64"), originalArm);
  writeFileSync(join(directory, "receipt.json"), " ".repeat(16385));
  await expect(verify(fixture, directory)).rejects.toThrow(/16 KiB/);
});
