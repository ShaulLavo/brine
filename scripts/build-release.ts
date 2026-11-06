import { createHash } from "node:crypto";
import { createReadStream, existsSync, lstatSync, mkdirSync, openSync, readSync, closeSync, readFileSync, readdirSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { basename, dirname, isAbsolute, join, relative, resolve } from "node:path";
import { spawnSync } from "node:child_process";

export const targets = [
  { os: "linux", arch: "amd64", machine: 62 },
  { os: "linux", arch: "arm64", machine: 183 },
  { os: "darwin", arch: "amd64", machine: 0x01000007 },
  { os: "darwin", arch: "arm64", machine: 0x0100000c },
];
export const artifactNames = targets.map(target => `brine-${target.os}-${target.arch}`);
export const releaseFiles = artifactNames.concat(["receipt.json", "SHA256SUMS"]);
const maxReceipt = 16 * 1024;
const root = resolve(import.meta.dir, "..");

export function command(args: readonly string[], cwd = root, env = process.env) {
  const [program, ...rest] = args;
  if (!program) throw new Error("empty command");
  const result = spawnSync(program, rest, { cwd, env, encoding: "utf8", timeout: 15 * 60_000, maxBuffer: 1024 * 1024 });
  if (result.error || result.status !== 0) throw new Error(`${program} failed: ${result.error?.message ?? result.stderr}`);
  return result.stdout.trim();
}

function git(args: readonly string[], cwd = root) {
  return command(["git"].concat(args), cwd);
}

export function source(cwd = root) {
  const head = git(["rev-parse", "HEAD"], cwd);
  const tree = git(["rev-parse", "HEAD^{tree}"], cwd);
  const dirty = git(["status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching"], cwd);
  return { head, tree, dirty };
}

export function versionTag(tag: string, cwd = root) {
  const version = readFileSync(join(cwd, "internal/cli/root.go"), "utf8").match(/^const Version = "([^"]+)"$/m)?.[1];
  if (!version || !/^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$/.test(tag) || tag !== `v${version}`) throw new Error("version/tag mismatch");
  return version;
}

export function goPin(cwd = root) {
  const pin = readFileSync(join(cwd, "mise.toml"), "utf8").match(/^go = "([0-9]+\.[0-9]+\.[0-9]+)"$/m)?.[1];
  if (!pin) throw new Error("missing exact Go pin in mise.toml");
  return `go${pin}`;
}

function buildEnvironment() {
  return { ...process.env, GOENV: "off", GOTOOLCHAIN: "local", GOWORK: "off", GOFLAGS: "", GOEXPERIMENT: "", CGO_ENABLED: "0", GOAMD64: "v1", GOARM64: "v8.0" };
}

function checkHeader(path: string, target: typeof targets[number]) {
  const buffer = Buffer.alloc(32);
  const fd = openSync(path, "r");
  try { if (readSync(fd, buffer, 0, buffer.length, 0) !== buffer.length) throw new Error("short binary"); }
  finally { closeSync(fd); }
  if (target.os === "linux" && buffer.subarray(0, 6).equals(Buffer.from([127, 69, 76, 70, 2, 1])) && buffer.readUInt16LE(18) === target.machine) return;
  if (target.os === "darwin" && buffer.readUInt32LE(0) === 0xfeedfacf && buffer.readUInt32LE(4) === target.machine) return;
  throw new Error(`wrong executable format: ${path}`);
}

export async function digest(path: string) {
  const stat = lstatSync(path);
  if (!stat.isFile() || stat.size === 0 || stat.size > 256 * 1024 * 1024) throw new Error(`invalid release file: ${path}`);
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(path)) hash.update(chunk);
  return { sha256: hash.digest("hex"), bytes: stat.size };
}

function checkMetadata(path: string, target: typeof targets[number], snapshot: ReturnType<typeof source>, pin: string, env: ReturnType<typeof buildEnvironment>) {
  const metadata = command(["go", "version", "-m", path], root, env);
  const settings = new Set(metadata.split("\n").map(line => line.trim().replace(/^build\t/, "")));
  const required = [`GOOS=${target.os}`, `GOARCH=${target.arch}`, "CGO_ENABLED=0", "-trimpath=true", `vcs.revision=${snapshot.head}`, "vcs.modified=false", `vcs.time=${new Date(Number(git(["show", "-s", "--format=%ct", snapshot.head])) * 1000).toISOString().replace(".000Z", "Z")}`];
  if (!metadata.split("\n")[0]?.endsWith(`: ${pin}`) || required.some(value => !settings.has(value))) throw new Error(`incorrect toolchain or VCS stamp: ${path}`);
  checkHeader(path, target);
}

function unchanged(before: ReturnType<typeof source>) {
  const after = source();
  if (before.head !== after.head || before.tree !== after.tree || after.dirty !== "") throw new Error("source changed during the build");
}

function outputPath(path: string) {
  const output = join(realpathSync(dirname(resolve(path))), basename(resolve(path)));
  const rel = relative(realpathSync(root), output);
  if (!rel || (rel !== ".." && !rel.startsWith("../") && !isAbsolute(rel))) throw new Error("build output must be outside the checkout");
  if (existsSync(output)) throw new Error("build output must not exist");
  return output;
}

export async function build(outputArgument: string, tag: string) {
  const snapshot = source();
  if (snapshot.dirty) throw new Error("build requires a clean checkout, including ignored files");
  const version = versionTag(tag);
  const pin = goPin();
  const env = buildEnvironment();
  if (command(["go", "version"], root, env) !== `go version ${pin} linux/amd64`) throw new Error(`requires pinned ${pin} on Linux amd64`);
  const output = outputPath(outputArgument);
  mkdirSync(output);
  let completed = false;
  const cleanup = () => { if (!completed) rmSync(output, { recursive: true, force: true }); };
  const interrupt = () => { cleanup(); process.exit(130); };
  process.once("SIGINT", interrupt);
  process.once("SIGTERM", interrupt);
  try {
    await buildArtifacts({ output, tag, version, snapshot, pin, env });
    unchanged(snapshot);
    completed = true;
  } catch (error) {
    unchanged(snapshot);
    throw error;
  } finally {
    cleanup();
    process.removeListener("SIGINT", interrupt);
    process.removeListener("SIGTERM", interrupt);
  }
}

async function buildArtifacts(options: { output: string; tag: string; version: string; snapshot: ReturnType<typeof source>; pin: string; env: ReturnType<typeof buildEnvironment> }) {
  const { output, tag, version, snapshot, pin, env } = options;
  const artifacts = [];
  for (const target of targets) {
    const name = `brine-${target.os}-${target.arch}`;
    const path = join(output, name);
    command(["go", "build", "-mod=readonly", "-buildvcs=true", "-trimpath", "-ldflags=-s -w", "-o", path, "./cmd/brine"], root, { ...env, GOOS: target.os, GOARCH: target.arch });
    checkMetadata(path, target, snapshot, pin, env);
    artifacts.push({ name, os: target.os, architecture: target.arch, ...await digest(path) });
  }
  const observed: unknown = JSON.parse(command([join(output, "brine-linux-amd64"), "version", "--json"]));
  if (!observed || typeof observed !== "object" || !("ok" in observed) || observed.ok !== true || !("data" in observed) || !observed.data || typeof observed.data !== "object" || !("version" in observed.data) || observed.data.version !== version) throw new Error("binary version/tag mismatch");
  const receipt = JSON.stringify({ schema_version: 1, source_head: snapshot.head, source_tree: snapshot.tree, version, tag, go_version: pin, vcs_modified: false, artifacts }, null, 2) + "\n";
  if (Buffer.byteLength(receipt) > maxReceipt) throw new Error("receipt exceeds 16 KiB");
  writeFileSync(join(output, "receipt.json"), receipt, { flag: "wx" });
  const receiptHash = await digest(join(output, "receipt.json"));
  writeFileSync(join(output, "SHA256SUMS"), artifacts.concat([{ name: "receipt.json", os: "", architecture: "", ...receiptHash }]).map(item => `${item.sha256}  ${item.name}\n`).join(""), { flag: "wx" });
}

export async function verifyDirectory(directory: string, tag: string) {
  const files = readdirSync(directory).sort();
  if (JSON.stringify(files) !== JSON.stringify(releaseFiles.toSorted())) throw new Error("unexpected release files");
  for (const name of releaseFiles) {
    if (!lstatSync(join(directory, name)).isFile()) throw new Error(`invalid release file: ${name}`);
  }
  if (lstatSync(join(directory, "SHA256SUMS")).size > 4096) throw new Error("oversized SHA256SUMS");
  const receiptFile = join(directory, "receipt.json");
  if (lstatSync(receiptFile).size > maxReceipt) throw new Error("receipt exceeds 16 KiB");
  const raw = readFileSync(receiptFile, "utf8");
  const receipt: unknown = JSON.parse(raw);
  const snapshot = source();
  const artifacts = [];
  for (const target of targets) {
    const name = `brine-${target.os}-${target.arch}`;
    const path = join(directory, name);
    checkHeader(path, target);
    artifacts.push({ name, os: target.os, architecture: target.arch, ...await digest(path) });
  }
  const expected = { schema_version: 1, source_head: snapshot.head, source_tree: snapshot.tree, version: versionTag(tag), tag, go_version: goPin(), vcs_modified: false, artifacts };
  if (JSON.stringify(receipt) !== JSON.stringify(expected)) throw new Error("receipt does not match source or artifact bytes");
  const sums = artifacts.map(item => `${item.sha256}  ${item.name}\n`).join("") + `${(await digest(receiptFile)).sha256}  receipt.json\n`;
  if (readFileSync(join(directory, "SHA256SUMS"), "utf8") !== sums) throw new Error("SHA256SUMS does not match release files");
  return sums;
}

export async function compare(first: string, second: string, tag: string) {
  if (await verifyDirectory(first, tag) !== await verifyDirectory(second, tag)) throw new Error("independent rebuild differs");
}

if (import.meta.main) {
  try {
    if (process.argv.length !== 3 || !process.env.BRINE_RELEASE_TAG) throw new Error("usage: BRINE_RELEASE_TAG=vVERSION build-release.sh OUTPUT_DIRECTORY");
    const output = process.argv[2];
    if (!output) throw new Error("missing output directory");
    await build(output, process.env.BRINE_RELEASE_TAG);
  } catch (error) {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  }
}
