import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { createReadStream, existsSync, mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from "node:fs";
import { isAbsolute, join, resolve } from "node:path";

const repository = "ShaulLavo/brine";
const image = "ghcr.io/shaullavo/brine-udp-fixture";
const sourceURL = `https://github.com/${repository}`;
const root = resolve(import.meta.dir, "..");
const minimumSource = "369744357524dc81019afc334a2f9376cf8c87b9";
const arches = ["amd64", "arm64"] as const;
type Arch = typeof arches[number];
type Target = "server" | "client";
type ObjectValue = Record<string, unknown>;
type Descriptor = { mediaType: string; digest: string; size: number };
type PlatformDescriptor = Descriptor & { platform: { os: "linux"; architecture: Arch } };
type Dispatch = { event: string; ref: string; repository: string; sha: string; source: string; publicAccess: string };
type BuiltImage = { release: string; config: string; archive: string; archiveSHA256: string; binarySHA256: string };
const maxJSON = 2 * 1024 * 1024;
const activeContainers = new Set<string>();

function requireThat(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

function object(value: unknown): ObjectValue {
  requireThat(typeof value === "object" && value !== null && !Array.isArray(value), "Expected a JSON object");
  return value as ObjectValue;
}

function text(value: unknown): string {
  requireThat(typeof value === "string", "Expected a string");
  return value;
}

function digest(value: unknown): string {
  const result = text(value);
  requireThat(result.length === 71 && /^sha256:[a-f0-9]{64}$/.test(result), "Expected an immutable SHA-256 digest");
  return result;
}

function arch(value: unknown): Arch {
  requireThat(value === "amd64" || value === "arm64", "Expected amd64 or arm64");
  return value;
}

function sha256(value: string | Uint8Array): string {
  return "sha256:" + createHash("sha256").update(value).digest("hex");
}

async function fileSHA256(path: string): Promise<string> {
  const hasher = createHash("sha256");
  for await (const chunk of createReadStream(path)) hasher.update(chunk);
  return "sha256:" + hasher.digest("hex");
}

function jsonFile(path: string): unknown {
  requireThat(statSync(path).size <= maxJSON, "JSON receipt exceeds the byte bound");
  return JSON.parse(readFileSync(path, "utf8"));
}

function save(path: string, value: unknown): void {
  writeFileSync(path, JSON.stringify(value, null, 2) + "\n", { mode: 0o600 });
}

export function validateDispatch(value: Dispatch): string {
  requireThat(value.event === "workflow_dispatch", "Only manual dispatch is permitted");
  requireThat(value.ref === "refs/heads/main" && value.repository === repository, "Only this repository's main branch is permitted");
  requireThat(/^[a-f0-9]{40}$/.test(value.source) && value.source.length === 40, "Supply the exact reviewed main SHA");
  requireThat(value.source === value.sha, "Input SHA must equal the dispatch SHA");
  requireThat(value.publicAccess === "true", "Public package access must be explicitly confirmed");
  return value.source;
}

export function verifySourceFacts(source: string, facts: { head: string; main: string; dirty: string }): void {
  requireThat(facts.head === source && facts.main === source, "HEAD, dispatch and current main must match exactly");
  requireThat(facts.dirty === "", "Publication requires a clean source checkout");
}

function command(args: readonly string[], timeout = 60_000, trim = true): string {
  const result = spawnSync(args[0], args.slice(1), { cwd: root, encoding: "utf8", timeout, maxBuffer: 4 * 1024 * 1024 });
  requireThat(!result.error && result.status === 0, `${args[0]} ${args[1] ?? ""} failed or timed out\n${result.stderr ?? ""}`);
  return trim ? result.stdout.trim() : result.stdout;
}

function sourceGate(): string {
  const env = process.env;
  const source = validateDispatch({ event: env.GITHUB_EVENT_NAME ?? "", ref: env.GITHUB_REF ?? "", repository: env.GITHUB_REPOSITORY ?? "", sha: env.GITHUB_SHA ?? "", source: env.INPUT_SOURCE_SHA ?? "", publicAccess: env.INPUT_PUBLIC_ACCESS_CONFIRMED ?? "" });
  const remote = command(["git", "ls-remote", "--exit-code", "origin", "refs/heads/main"]).split(/\s+/)[0];
  verifySourceFacts(source, { head: command(["git", "rev-parse", "HEAD"]), main: remote, dirty: command(["git", "status", "--porcelain", "--untracked-files=all"]) });
  command(["git", "merge-base", "--is-ancestor", minimumSource, source]);
  requireThat(Bun.version === "1.4.4", "Bun 1.4.4 is required");
  return source;
}

function descriptor(value: unknown): Descriptor {
  const entry = object(value);
  requireThat(Number.isSafeInteger(entry.size) && typeof entry.size === "number" && entry.size > 0 && entry.size <= 128 * 1024 * 1024, "Invalid descriptor byte bound");
  return { mediaType: text(entry.mediaType), digest: digest(entry.digest), size: entry.size };
}

function verifiedJSON(raw: string, expected: string): ObjectValue {
  requireThat(Buffer.byteLength(raw) <= maxJSON && sha256(raw) === digest(expected), "OCI content does not match its digest");
  const value = object(JSON.parse(raw));
  requireThat(value.schemaVersion === 2, "Unsupported OCI schema");
  return value;
}

export function parseIndex(raw: string, expected: string): PlatformDescriptor[] {
  const value = verifiedJSON(raw, expected);
  requireThat(Array.isArray(value.manifests) && value.manifests.length === 2, "Index must contain exactly two platform manifests");
  const entries = value.manifests.map((item: unknown): PlatformDescriptor => {
    const entry = object(item);
    const platform = object(entry.platform);
    requireThat(platform.os === "linux" && (platform.variant === undefined || platform.variant === "v8" && platform.architecture === "arm64"), "Unexpected image OS or variant");
    const info = descriptor(entry);
    requireThat(info.mediaType === "application/vnd.oci.image.manifest.v1+json" || info.mediaType === "application/vnd.docker.distribution.manifest.v2+json", "Unexpected platform manifest type");
    return { ...info, platform: { os: "linux", architecture: arch(platform.architecture) } };
  });
  requireThat(new Set(entries.map(entry => entry.platform.architecture)).size === 2, "Duplicate or missing platform");
  return entries;
}

export function parseManifest(raw: string, expected: string): { config: Descriptor; layers: Descriptor[] } {
  const value = verifiedJSON(raw, expected);
  const config = descriptor(value.config);
  requireThat(Array.isArray(value.layers) && value.layers.length === 1, "Scratch fixture must have one binary layer");
  return { config, layers: value.layers.map(descriptor) };
}

export function verifyConfig(value: unknown, platform: Arch, source: string, release: string, target: Target): void {
  const document = object(value);
  requireThat(document.os === "linux" && document.architecture === platform, "Image platform differs from qualification");
  const config = object(document.config);
  const labels = object(config.Labels);
  requireThat(config.User === "65532:65532", "Image must declare non-root UID and GID 65532");
  requireThat(JSON.stringify(config.Entrypoint) === JSON.stringify([`/udp-echo-${target}`]), "Unexpected fixture entrypoint");
  requireThat(labels["org.opencontainers.image.revision"] === source && labels["org.opencontainers.image.source"] === sourceURL && labels["org.opencontainers.image.version"] === release, "Image source or release identity differs");
}

export function verifyPublicPackage(value: unknown): void {
  const info = object(value);
  requireThat(info.name === "brine-udp-fixture" && info.package_type === "container" && info.visibility === "public", "Existing GHCR package must already be public; no visibility changes are automated");
  requireThat(object(info.repository).full_name === repository, "GHCR package must be linked to the source repository");
}

async function consumeBody(reader: ReadableStreamDefaultReader<Uint8Array>, limit: number): Promise<Uint8Array> {
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const part = await reader.read();
    if (part.done) break;
    size += part.value.byteLength;
    requireThat(size <= limit, "HTTP response exceeds byte bound");
    chunks.push(part.value);
  }
  return Buffer.concat(chunks, size);
}

async function responseBytes(response: Response, limit: number): Promise<Uint8Array> {
  requireThat(response.body !== null, "HTTP response body is missing");
  const reader = response.body.getReader();
  try {
    return await consumeBody(reader, limit);
  } finally {
    await reader.cancel();
  }
}

async function readURL(url: string, headers: Record<string, string> = {}): Promise<string> {
  const response = await fetch(url, { headers, redirect: "error", signal: AbortSignal.timeout(15_000) });
  requireThat(response.ok, `Read-only HTTP request failed with status ${response.status}`);
  return new TextDecoder("utf-8", { fatal: true }).decode(await responseBytes(response, maxJSON));
}

async function anonymousRegistry(): Promise<(kind: "manifests" | "blobs", reference: string) => Promise<string>> {
  const token = object(JSON.parse(await readURL("https://ghcr.io/token?service=ghcr.io&scope=repository:shaullavo/brine-udp-fixture:pull")));
  const bearer = text(token.token);
  return async (kind, reference) => {
    digest(reference);
    let response = await fetch(`https://ghcr.io/v2/shaullavo/brine-udp-fixture/${kind}/${reference}`, {
      headers: { Authorization: `Bearer ${bearer}`, Accept: "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json" },
      redirect: "manual", signal: AbortSignal.timeout(15_000),
    });
    if (response.status === 307 || response.status === 302) {
      const target = new URL(text(response.headers.get("location")));
      requireThat(kind === "blobs" && target.protocol === "https:" && target.hostname === "pkg-containers.githubusercontent.com" && !target.username && !target.password && !target.port, "Unexpected registry redirect");
      response = await fetch(target, { redirect: "error", signal: AbortSignal.timeout(15_000) });
    }
    requireThat(response.ok, `Anonymous digest read failed with status ${response.status}`);
    return new TextDecoder("utf-8", { fatal: true }).decode(await responseBytes(response, maxJSON));
  };
}

async function publicPreflight(): Promise<unknown> {
  requireThat(process.env.GH_TOKEN, "The workflow's built-in GitHub token is required");
  const info: unknown = JSON.parse(await readURL("https://api.github.com/users/shaullavo/packages/container/brine-udp-fixture", { Authorization: `Bearer ${process.env.GH_TOKEN}`, Accept: "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28", "User-Agent": "brine-udp-fixture" }));
  verifyPublicPackage(info);
  const token = object(JSON.parse(await readURL("https://ghcr.io/token?service=ghcr.io&scope=repository:shaullavo/brine-udp-fixture:pull")));
  await readURL("https://ghcr.io/v2/shaullavo/brine-udp-fixture/tags/list?n=1", { Authorization: `Bearer ${text(token.token)}` });
  return { package: "brine-udp-fixture", visibility: "public", repository, anonymousRead: true, checkedAt: new Date().toISOString() };
}

function newDirectory(path: string): string {
  requireThat(isAbsolute(path) && !existsSync(path) && !path.startsWith(root + "/"), "Output must be a new absolute directory outside the checkout");
  mkdirSync(path, { mode: 0o700, recursive: true });
  return path;
}

function releaseIDs(source: string): string[] {
  return ["a", "b"].map(variant => `p08-08-${source}-${variant}`);
}

function pins(): unknown {
  const value = object(jsonFile(join(root, "fixtures/udp-echo/pins.json")));
  requireThat(value.go_version === "1.27.3" && value.runtime_base === "scratch" && value.runtime_user === "65532:65532", "Fixture pins changed; review publication pins together");
  requireThat(value.go_builder === "docker.io/library/golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b", "Unexpected builder pin");
  requireThat(Array.isArray(value.third_party_dependencies) && value.third_party_dependencies.length === 0, "Fixture must remain standard-library-only");
  return value;
}

function inputHashes(): Record<string, string> {
  const names = ["fixtures/udp-echo/Dockerfile", "fixtures/udp-echo/Dockerfile.dockerignore", "fixtures/udp-echo/pins.json", "mise.toml", "go.mod", "go.sum", "scripts/publish-udp-fixture.ts", ".github/workflows/udp-fixture.yml"];
  return Object.fromEntries(names.map(name => [name, sha256(readFileSync(join(root, name)))]));
}

function removeContainer(name: string): void {
  if (command(["docker", "ps", "-aq", "--filter", `name=^/${name}$`]) !== "") command(["docker", "rm", "-f", name]);
  requireThat(command(["docker", "ps", "-aq", "--filter", `name=^/${name}$`]) === "", "Task container cleanup failed");
  activeContainers.delete(name);
}

function cleanupContainers(): string[] {
  const errors: string[] = [];
  for (const name of activeContainers) {
    try { removeContainer(name); } catch (error) { errors.push(String(error)); }
  }
  return errors;
}

function cleanupImages(owned: Set<string>, baseline: Set<string>): string[] {
  const errors: string[] = [];
  for (const id of Array.from(owned).reverse()) {
    if (baseline.has(id)) continue;
    try { removeImage(id); } catch (error) { errors.push(String(error)); }
  }
  return errors;
}

function removeImage(reference: string): void {
  const result = spawnSync("docker", ["image", "inspect", "--format", "{{.Id}}", reference], { encoding: "utf8", timeout: 15_000, maxBuffer: 4096 });
  if (!result.error && result.status === 1 && result.stderr.includes("No such image")) return;
  requireThat(!result.error && result.status === 0, "Cannot inspect task image during cleanup");
  command(["docker", "image", "rm", reference]);
}

function localConfig(id: string): unknown {
  return JSON.parse(command(["docker", "image", "inspect", "--format", "{{json .}}", id]));
}

function verifyLocalImage(id: string, platform: Arch, source: string, release: string, target: Target): void {
  const info = object(localConfig(id));
  verifyConfig({ os: info.Os, architecture: info.Architecture, config: info.Config }, platform, source, release, target);
}

function buildImage(output: string, platform: Arch, source: string, release: string, target: Target, owned: Set<string>): string {
  const metadata = join(output, `${release}-${target}-build.json`);
  command(["docker", "buildx", "build", "--platform", `linux/${platform}`, "--target", `udp-echo-${target}`, "--build-arg", `RELEASE=${release}`, "--network", "none", "--provenance=false", "--sbom=false", "--load", "--metadata-file", metadata,
    "--label", `org.opencontainers.image.source=${sourceURL}`, "--label", `org.opencontainers.image.revision=${source}`, "--label", `org.opencontainers.image.version=${release}`, "-f", "fixtures/udp-echo/Dockerfile", "."], 600_000);
  const id = digest(object(jsonFile(metadata))["containerimage.config.digest"]);
  owned.add(id);
  verifyLocalImage(id, platform, source, release, target);
  return id;
}

function containerOptions(): string[] {
  return ["--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--pids-limit=64", "--memory=64m", "--cpus=1"];
}

function oneJSON(raw: string): ObjectValue {
  return object(JSON.parse(raw));
}

export function verifyEcho(value: unknown, release: string, wrong: boolean): void {
  const proof = object(value);
  const reply = object(proof.response);
  requireThat(proof.schema === 1 && proof.fixture === "brine-udp-echo" && proof.endpoint === "127.0.0.1:7777", "UDP client identity or endpoint differs");
  requireThat(reply.release === release && reply.nonce === proof.nonce && proof.outcome === (wrong ? "protocol_error" : "ok") && (!wrong || proof.error === "response release mismatch"), "UDP nonce or compiled release qualification failed");
  requireThat(proof.expected_release === (wrong ? "wrong-release" : release) && proof.attempts === 1 && proof.attempt_timeout_ms === 1000 && proof.overall_timeout_ms === 2000, "UDP client bounds or expected release differs");
}

function echo(client: string, server: string, release: string, wrong: boolean): unknown {
  const name = `${server}-client`;
  requireThat(command(["docker", "ps", "-aq", "--filter", `name=^/${name}$`]) === "", "Client name already exists; inspect before retrying");
  activeContainers.add(name);
  try {
    command(["docker", "create", "--name", name].concat(containerOptions(), ["--network", `container:${server}`, client, "-endpoint", "127.0.0.1:7777", "-release", wrong ? "wrong-release" : release, "-vantage", "ci-native-container", "-attempts", "1", "-attempt-timeout", "1s", "-overall-timeout", "2s"]));
    const result = spawnSync("docker", ["start", "--attach", name], { cwd: root, encoding: "utf8", timeout: 15_000, maxBuffer: 4096 });
    requireThat(!result.error && (result.status === 0 || result.status === 1), "Bounded UDP client did not finish");
    const state = oneJSON(command(["docker", "inspect", "--format", "{{json .State}}", name]));
    requireThat(state.Running === false && state.ExitCode === (wrong ? 1 : 0), "Bounded UDP client returned the wrong container exit status");
    const proof = oneJSON(result.stdout);
    verifyEcho(proof, release, wrong);
    return proof;
  } finally {
    removeContainer(name);
  }
}

async function healthOnce(binding: string, release: string, failHealth: boolean): Promise<{ kind: "ready"; proof: unknown } | { kind: "pending"; error: unknown }> {
  try {
    const response = await fetch(`http://${binding}/healthz`, { redirect: "error", signal: AbortSignal.timeout(1000) });
    const body = new TextDecoder("utf-8", { fatal: true }).decode(await responseBytes(response, 2048));
    const proof = oneJSON(body);
    requireThat(response.status === (failHealth ? 503 : 200) && proof.fixture === "brine-udp-echo" && proof.release === release && proof.ok === !failHealth, "HTTP release or failed-health qualification differs");
    return { kind: "ready", proof: { status: response.status, body: proof } };
  } catch (error) {
    return { kind: "pending", error };
  }
}

async function health(server: string, release: string, failHealth: boolean): Promise<unknown> {
  const binding = command(["docker", "port", server, "8080/tcp"]);
  requireThat(/^127\.0\.0\.1:[0-9]+$/.test(binding), "HTTP port must be loopback-only");
  for (let attempt = 0; attempt < 20; attempt++) {
    const result = await healthOnce(binding, release, failHealth);
    if (result.kind === "ready") return result.proof;
    if (attempt === 19) throw result.error;
    await Bun.sleep(100);
  }
  throw new Error("HTTP qualification deadline exceeded");
}

async function inspectEntrypoint(output: string, name: string): Promise<string> {
  const binary = join(output, `${name}-server`);
  try {
    command(["docker", "cp", `${name}:/udp-echo-server`, binary]);
    requireThat((statSync(binary).mode & 0o777) === 0o555, "Image entrypoint mode must be 0555");
    return await fileSHA256(binary);
  } finally {
    rmSync(binary, { force: true });
  }
}

async function runtimeProof(output: string, id: string, client: string, release: string, failHealth: boolean): Promise<unknown> {
  const name = `brine-udp-${process.env.GITHUB_RUN_ID}-${process.env.GITHUB_RUN_ATTEMPT}-${release.slice(-1)}-${failHealth ? "failed" : "ok"}`;
  requireThat(/^brine-udp-[0-9]+-[0-9]+-[ab]-(ok|failed)$/.test(name), "Invalid run identity");
  requireThat(command(["docker", "ps", "-aq", "--filter", `name=^/${name}$`]) === "", "Owned container name already exists; inspect before retrying");
  activeContainers.add(name);
  try {
    command(["docker", "create", "--name", name].concat(containerOptions(), ["--network", "bridge", "-p", "127.0.0.1::8080/tcp", "-e", `UDP_ECHO_FAIL_HEALTH=${failHealth}`, id]));
    const binarySHA256 = await inspectEntrypoint(output, name);
    command(["docker", "start", name]);
    const http = await health(name, release, failHealth);
    const processIdentity = command(["docker", "top", name, "-eo", "uid,gid,args"]);
    requireThat(processIdentity.trim().split("\n").length === 2 && /^\s*65532\s+65532\s/m.test(processIdentity), "Running process must have UID and GID 65532");
    const udp = echo(client, name, release, false);
    const wrongRelease = echo(client, name, release, true);
    return { release, binarySHA256, processIdentity, mode: "0555", user: "65532:65532", http, udp, wrongRelease, failHealth, checkedAt: new Date().toISOString() };
  } finally {
    removeContainer(name);
  }
}

async function qualify(source: string, platform: Arch, output: string): Promise<void> {
  requireThat(process.platform === "linux" && process.arch === (platform === "amd64" ? "x64" : "arm64"), "Qualification must run natively on its named architecture");
  newDirectory(output);
  const buildx = command(["docker", "buildx", "version"]);
  requireThat(/\bv0\.29\.1\b/.test(buildx), "Buildx v0.29.1 is required");
  const builder = command(["docker", "buildx", "inspect", "--bootstrap"]);
  requireThat(/BuildKit version:\s+v0\.25\.1\b/.test(builder), "BuildKit v0.25.1 is required");
  const baseline = command(["docker", "ps", "-a", "--no-trunc"]);
  save(join(output, "baseline.json"), { baseline, images: command(["docker", "image", "ls", "--no-trunc"]), docker: command(["docker", "version"]), buildx, builder, pins: pins(), inputs: inputHashes(), source, platform, startedAt: new Date().toISOString() });
  const baselineImages = new Set(command(["docker", "image", "ls", "-q", "--no-trunc"]).split("\n"));
  const owned = new Set<string>();
  const interrupted = () => { save(join(output, "interrupted-cleanup.json"), { errors: cleanupContainers().concat(cleanupImages(owned, baselineImages)), checkedAt: new Date().toISOString() }); process.exit(130); };
  process.once("SIGINT", interrupted);
  process.once("SIGTERM", interrupted);
  try {
    const releases = releaseIDs(source);
    const client = buildImage(output, platform, source, releases[0], "client", owned);
    const entries: BuiltImage[] = [];
    for (const release of releases) entries.push(await qualifyRelease({ output, platform, source, release, client, owned }));
    requireThat(entries[0].config !== entries[1].config && entries[0].binarySHA256 !== entries[1].binarySHA256, "Compiled releases must have different image and server bytes");
    save(join(output, "qualification.json"), { schema: 1, source, platform, entries, pins: pins(), inputs: inputHashes(), completedAt: new Date().toISOString() });
  } finally {
    process.off("SIGINT", interrupted);
    process.off("SIGTERM", interrupted);
    const cleanupErrors = cleanupContainers().concat(cleanupImages(owned, baselineImages));
    save(join(output, "cleanup.json"), { images: Array.from(owned), cleanupErrors, containers: command(["docker", "ps", "-a", "--no-trunc"]), checkedAt: new Date().toISOString() });
    requireThat(cleanupErrors.length === 0, "Qualification image cleanup failed");
  }
}

async function qualifyRelease(input: { output: string; platform: Arch; source: string; release: string; client: string; owned: Set<string> }): Promise<BuiltImage> {
  const { output, platform, source, release, client, owned } = input;
  const id = buildImage(output, platform, source, release, "server", owned);
  const proof = object(await runtimeProof(output, id, client, release, false));
  save(join(output, `${release}-runtime.json`), proof);
  if (release.endsWith("-b")) save(join(output, `${release}-failed-health.json`), await runtimeProof(output, id, client, release, true));
  const archive = `${release}.tar`;
  command(["docker", "image", "save", "-o", join(output, archive), id]);
  return { release, config: id, archive, archiveSHA256: await fileSHA256(join(output, archive)), binarySHA256: digest(proof.binarySHA256) };
}

async function readQualification(input: string, source: string, platform: Arch): Promise<BuiltImage[]> {
  const directory = join(input, platform);
  const qualification = object(jsonFile(join(directory, "qualification.json")));
  requireThat(qualification.schema === 1 && qualification.source === source && qualification.platform === platform && JSON.stringify(qualification.pins) === JSON.stringify(pins()) && JSON.stringify(qualification.inputs) === JSON.stringify(inputHashes()), "Qualification source, pins or architecture differs");
  requireThat(Array.isArray(qualification.entries) && qualification.entries.length === 2, "Two qualified releases are required");
  const entries: BuiltImage[] = [];
  for (const item of qualification.entries) entries.push(await readQualifiedImage(directory, object(item)));
  requireThat(JSON.stringify(entries.map(entry => entry.release)) === JSON.stringify(releaseIDs(source)), "Qualified release identities differ");
  requireThat(entries[0].config !== entries[1].config && entries[0].binarySHA256 !== entries[1].binarySHA256, "Qualified releases are not distinguishable");
  return entries;
}

async function readQualifiedImage(directory: string, item: ObjectValue): Promise<BuiltImage> {
  const release = text(item.release);
  requireThat(/^p08-08-[a-f0-9]{40}-[ab]$/.test(release), "Invalid qualified release identity");
  requireThat(item.archive === `${release}.tar`, "Archive name does not match its release");
  const archive = join(directory, item.archive);
  requireThat(statSync(archive).isFile() && statSync(archive).size <= 128 * 1024 * 1024, "Invalid qualified image archive");
  const archiveSHA256 = digest(item.archiveSHA256);
  requireThat(await fileSHA256(archive) === archiveSHA256, "Qualified image archive changed");
  return { release, archive, archiveSHA256, config: digest(item.config), binarySHA256: digest(item.binarySHA256) };
}

async function recordAnonymousIndex(source: string, release: string, raw: string, expectedConfigs: Map<Arch, string>, expectedManifests: Map<Arch, string>, output: string): Promise<unknown> {
  const indexDigest = sha256(raw);
  const entries = parseIndex(raw, indexDigest);
  const anonymous = await anonymousRegistry();
  requireThat(await anonymous("manifests", indexDigest) === raw, "Anonymous index bytes differ from publication");
  const platforms: unknown[] = [];
  for (const entry of entries) {
    requireThat(entry.digest === expectedManifests.get(entry.platform.architecture), "Index platform differs from its pushed manifest receipt");
    platforms.push(await recordAnonymousPlatform({ source, release, entry, expectedConfigs, anonymous, output }));
  }
  return { release, reference: `${image}@${indexDigest}`, indexDigest, platforms, anonymousDigestPull: true, qualifiedContainerRuntime: "native Docker, not Brine rootless Podman", brinePhysicalAcceptance: "unverified" };
}

async function recordAnonymousPlatform(input: { source: string; release: string; entry: PlatformDescriptor; expectedConfigs: Map<Arch, string>; anonymous: Awaited<ReturnType<typeof anonymousRegistry>>; output: string }): Promise<unknown> {
  const { source, release, entry, expectedConfigs, anonymous, output } = input;
  const raw = await anonymous("manifests", entry.digest);
  requireThat(Buffer.byteLength(raw) === entry.size, "Platform manifest byte size differs");
  const manifest = parseManifest(raw, entry.digest);
  requireThat(manifest.config.digest === expectedConfigs.get(entry.platform.architecture), "Published platform config differs from qualified archive");
  const config = await anonymous("blobs", manifest.config.digest);
  requireThat(sha256(config) === manifest.config.digest && Buffer.byteLength(config) === manifest.config.size, "Published config bytes differ");
  verifyConfig(JSON.parse(config), entry.platform.architecture, source, release, "server");
  writeFileSync(join(output, `${release}-${entry.platform.architecture}-manifest.json`), raw);
  writeFileSync(join(output, `${release}-${entry.platform.architecture}-config.json`), config);
  return { platform: entry.platform, manifestDigest: entry.digest, configDigest: manifest.config.digest, layers: manifest.layers };
}

function pushPlatform(entry: BuiltImage, platform: Arch, source: string, output: string, owned: Set<string>): string {
  owned.add(entry.config);
  command(["docker", "image", "load", "-i", entry.archive]);
  verifyLocalImage(entry.config, platform, source, entry.release, "server");
  const tag = `${image}:${entry.release}-${platform}-${process.env.GITHUB_RUN_ID}-${process.env.GITHUB_RUN_ATTEMPT}`;
  save(join(output, `${entry.release}-${platform}-push-intent.json`), { source, platform, tag, config: entry.config, archiveSHA256: entry.archiveSHA256 });
  owned.add(tag);
  command(["docker", "tag", entry.config, tag]);
  command(["docker", "push", tag], 300_000);
  const repoDigests: unknown = JSON.parse(command(["docker", "image", "inspect", "--format", "{{json .RepoDigests}}", tag]));
  requireThat(Array.isArray(repoDigests) && repoDigests.length === 1, "Expected one pushed immutable repository digest");
  const reference = text(repoDigests[0]);
  requireThat(reference.startsWith(image + "@"), "Unexpected pushed repository");
  digest(reference.slice(image.length + 1));
  save(join(output, `${entry.release}-${platform}-push-receipt.json`), { tag, reference, config: entry.config });
  return reference;
}

async function publish(source: string, input: string, output: string): Promise<void> {
  newDirectory(output);
  save(join(output, "public-preflight.json"), await publicPreflight());
  const qualified = new Map<Arch, BuiltImage[]>();
  for (const platform of arches) qualified.set(platform, await readQualification(input, source, platform));
  const baseline = new Set(command(["docker", "image", "ls", "-q", "--no-trunc"]).split("\n").concat(command(["docker", "image", "ls", "--format", "{{.Repository}}:{{.Tag}}"]).split("\n"), command(["docker", "image", "ls", "--digests", "--format", "{{.Repository}}@{{.Digest}}"]).split("\n")));
  const owned = new Set<string>();
  const interrupted = () => { save(join(output, "interrupted-cleanup.json"), { errors: cleanupImages(owned, baseline), checkedAt: new Date().toISOString() }); process.exit(130); };
  process.once("SIGINT", interrupted);
  process.once("SIGTERM", interrupted);
  try {
    const results: unknown[] = [];
    for (const release of releaseIDs(source)) results.push(await publishRelease(source, release, qualified, output, owned));
    save(join(output, "publication.json"), { schema: 1, source, repository, pins: pins(), inputs: inputHashes(), releases: results, completedAt: new Date().toISOString() });
    process.stdout.write(JSON.stringify(results, null, 2) + "\n");
  } finally {
    process.off("SIGINT", interrupted);
    process.off("SIGTERM", interrupted);
    const errors = cleanupImages(owned, baseline);
    save(join(output, "cleanup.json"), { localImages: Array.from(owned), errors, remoteImages: "retained publication artifacts; never deleted automatically", checkedAt: new Date().toISOString() });
    requireThat(errors.length === 0, "Publication local-image cleanup failed");
  }
}

async function publishRelease(source: string, release: string, qualified: Map<Arch, BuiltImage[]>, output: string, owned: Set<string>): Promise<unknown> {
  const references: string[] = [];
  const configs = new Map<Arch, string>();
  const manifests = new Map<Arch, string>();
  for (const platform of arches) {
    const entry = qualified.get(platform)?.find(item => item.release === release);
    requireThat(entry, "Missing native qualification");
    configs.set(platform, entry.config);
    const reference = pushPlatform(entry, platform, source, output, owned);
    references.push(reference);
    manifests.set(platform, reference.slice(image.length + 1));
  }
  const tag = `${image}:${release}-${process.env.GITHUB_RUN_ID}-${process.env.GITHUB_RUN_ATTEMPT}`;
  save(join(output, `${release}-index-intent.json`), { source, release, tag, references });
  command(["docker", "buildx", "imagetools", "create", "--tag", tag].concat(references), 120_000);
  const exact = command(["docker", "buildx", "imagetools", "inspect", "--raw", tag], 60_000, false);
  writeFileSync(join(output, `${release}-index.json`), exact);
  const result = await recordAnonymousIndex(source, release, exact, configs, manifests, output);
  const reference = text(object(result).reference);
  owned.add(reference);
  await anonymousDockerPull(reference, output, release);
  save(join(output, `${release}-publication.json`), result);
  return result;
}

async function anonymousDockerPull(reference: string, output: string, release: string): Promise<void> {
  const config = join(output, `${release}-anonymous-docker`);
  mkdirSync(config, { mode: 0o700 });
  writeFileSync(join(config, "config.json"), "{}\n", { mode: 0o600 });
  try {
    for (const platform of arches) command(["docker", "--config", config, "pull", "--platform", `linux/${platform}`, reference], 120_000);
    save(join(output, `${release}-anonymous-pulls.json`), { reference, platforms: arches, credentials: "empty isolated Docker config", checkedAt: new Date().toISOString() });
  } finally {
    rmSync(config, { recursive: true });
  }
}

export async function main(args: string[]): Promise<void> {
  const [mode, ...rest] = args;
  requireThat(["gate", "preflight", "qualify", "publish"].includes(mode), "Usage: bun scripts/publish-udp-fixture.ts gate|preflight|qualify ARCH NEW_OUTPUT|publish INPUT NEW_OUTPUT");
  const source = sourceGate();
  requireThat(/^[0-9]+$/.test(process.env.GITHUB_RUN_ID ?? "") && /^[0-9]+$/.test(process.env.GITHUB_RUN_ATTEMPT ?? ""), "Missing workflow run identity");
  if (mode === "gate") { requireThat(rest.length === 0, "Unexpected gate arguments"); process.stdout.write(JSON.stringify({ source, releases: releaseIDs(source), pins: pins() }) + "\n"); return; }
  if (mode === "preflight") { requireThat(rest.length === 0, "Unexpected preflight arguments"); process.stdout.write(JSON.stringify(await publicPreflight()) + "\n"); return; }
  requireThat(rest.length === 2, "Expected exactly two command arguments");
  if (mode === "qualify") { await qualify(source, arch(rest[0]), rest[1]); return; }
  requireThat(isAbsolute(rest[0]) && readdirSync(rest[0]).length === 2, "Expected only amd64 and arm64 input directories");
  await publish(source, rest[0], rest[1]);
}

if (import.meta.main) main(process.argv.slice(2)).catch(error => { process.stderr.write(String(error) + "\n"); process.exitCode = 1; });
