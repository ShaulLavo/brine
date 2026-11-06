import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, writeFileSync, rmSync, readFileSync, symlinkSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { compare, digest, goPin, source, targets, verifyDirectory, versionTag } from "../../../scripts/build-release.ts";

const roots: string[] = [];
afterEach(() => { for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true }); });
const version = readFileSync(new URL("../../../internal/cli/root.go", import.meta.url), "utf8").match(/^const Version = "([^"]+)"$/m)?.[1];
const tag = `v${version}`;

// Tiny format fixtures exercise receipt validation, not Go compilation or reproducibility.
async function fixture() {
  const directory = mkdtempSync(join(tmpdir(), "brine-release-receipt-"));
  roots.push(directory);
  const snapshot = source();
  const artifacts = [];
  for (const target of targets) {
    const name = `brine-${target.os}-${target.arch}`;
    const bytes = Buffer.alloc(32);
    if (target.os === "linux") { bytes.set([127, 69, 76, 70, 2, 1]); bytes.writeUInt16LE(target.machine, 18); }
    if (target.os === "darwin") { bytes.writeUInt32LE(0xfeedfacf, 0); bytes.writeUInt32LE(target.machine, 4); }
    writeFileSync(join(directory, name), bytes);
    artifacts.push({ name, os: target.os, architecture: target.arch, ...await digest(join(directory, name)) });
  }
  writeFileSync(join(directory, "receipt.json"), JSON.stringify({ schema_version: 1, source_head: snapshot.head, source_tree: snapshot.tree, version: versionTag(tag), tag, go_version: goPin(), vcs_modified: false, artifacts }, null, 2) + "\n");
  writeFileSync(join(directory, "SHA256SUMS"), artifacts.map(item => `${item.sha256}  ${item.name}\n`).join("") + `${(await digest(join(directory, "receipt.json"))).sha256}  receipt.json\n`);
  return directory;
}

test("receipt binds exact source, version, Go pin, matrix, format, size and bytes", async () => {
  const directory = await fixture();
  expect(await verifyDirectory(directory, tag)).toContain("brine-darwin-arm64");
  const receiptPath = join(directory, "receipt.json");
  const original = readFileSync(receiptPath, "utf8");
  for (const [old, replacement] of [[source().head, "b".repeat(40)], [source().tree, "c".repeat(40)], [goPin(), "go0.0.0"], ['"vcs_modified": false', '"vcs_modified": true'], ['"schema_version": 1', '"schema_version": 2'], ['"bytes": 32', '"bytes": 33']]) {
    writeFileSync(receiptPath, original.replace(old, replacement));
    await expect(verifyDirectory(directory, tag)).rejects.toThrow(/receipt/);
  }
  writeFileSync(receiptPath, original);
  writeFileSync(join(directory, "brine-linux-amd64"), Buffer.alloc(32));
  await expect(verifyDirectory(directory, tag)).rejects.toThrow(/executable format/);
});

test("compare validates actual files before comparing manifests", async () => {
  const first = await fixture();
  const second = await fixture();
  await expect(compare(first, second, tag)).resolves.toBeUndefined();
  const path = join(second, "brine-darwin-arm64");
  const bytes = readFileSync(path);
  bytes[31] = 1;
  writeFileSync(path, bytes);
  await expect(compare(first, second, tag)).rejects.toThrow(/artifact bytes/);
});

test("extra files, symlinks, oversized receipt and altered checksums refuse", async () => {
  const directory = await fixture();
  const sumPath = join(directory, "SHA256SUMS");
  const sums = readFileSync(sumPath, "utf8");
  writeFileSync(sumPath, "0" + sums.slice(1));
  await expect(verifyDirectory(directory, tag)).rejects.toThrow(/SHA256SUMS/);
  writeFileSync(sumPath, sums);
  writeFileSync(join(directory, "extra"), "unlisted");
  await expect(verifyDirectory(directory, tag)).rejects.toThrow(/unexpected release files/);
  rmSync(join(directory, "extra"));
  const originalArm = readFileSync(join(directory, "brine-linux-arm64"));
  rmSync(join(directory, "brine-linux-arm64"));
  symlinkSync("brine-linux-amd64", join(directory, "brine-linux-arm64"));
  await expect(verifyDirectory(directory, tag)).rejects.toThrow();
  rmSync(join(directory, "brine-linux-arm64"));
  writeFileSync(join(directory, "brine-linux-arm64"), originalArm);
  writeFileSync(join(directory, "receipt.json"), " ".repeat(16385));
  await expect(verifyDirectory(directory, tag)).rejects.toThrow(/16 KiB/);
});
