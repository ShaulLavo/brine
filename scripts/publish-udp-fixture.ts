import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { closeSync, createReadStream, existsSync, fsyncSync, mkdirSync, openSync, readFileSync, readdirSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
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
type Dispatch = { event: string; ref: string; repository: string; sha: string; source: string; publicAccess: string; bootstrapConfirmed: string };
type BuiltImage = { release: string; config: string; archive: string; archiveSHA256: string; binarySHA256: string };
type BootstrapState = { source: string; packageState: "absent"; packageWrite: "not_started" | "complete" | "unknown" };
type PackageMetadata = { name: string; package_type: string; visibility: string; repository: { full_name: string } };
type PackageRead = { status: number; value?: unknown };
type RegistryObservation = { tag: string; status: "absent" | "present" | "unknown"; raw?: string; digest?: string; error?: string };
type PublicationDecision = { decision: "retry" | "success" | "stop"; tag: string; manifestDigest?: string; configDigest?: string; indexDigest?: string; platforms?: Arch[]; reference?: string; reason?: string };
type PlatformPublicationIntent = { tag: string; source: string; release: string; platform: Arch; config: string };
type IndexPublicationIntent = { tag: string; platformRefs: Record<Arch, string> };
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
  const temporary = `${path}.${process.pid}.tmp`;
  const contents = JSON.stringify(value, null, 2) + "\n";
  try {
    writeFileSync(temporary, contents, { mode: 0o600, flag: "wx" });
    const file = openSync(temporary, "r");
    try {
      fsyncSync(file);
    } finally {
      closeSync(file);
    }
    renameSync(temporary, path);
  } finally {
    rmSync(temporary, { force: true });
  }
}

export function validateDispatch(value: Dispatch): string {
  requireThat(value.event === "workflow_dispatch", "Only manual dispatch is permitted");
  requireThat(value.ref === "refs/heads/main" && value.repository === repository, "Only this repository's main branch is permitted");
  requireThat(/^[a-f0-9]{40}$/.test(value.source) && value.source.length === 40, "Supply the exact reviewed main SHA");
  requireThat(value.source === value.sha, "Input SHA must equal the dispatch SHA");
  requireThat(["true", "false"].includes(value.publicAccess) && ["true", "false"].includes(value.bootstrapConfirmed), "Publication confirmations must be explicit booleans");
  requireThat((value.publicAccess === "true") !== (value.bootstrapConfirmed === "true"), "Select exactly one explicit publication confirmation");
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
  const source = validateDispatch({ event: env.GITHUB_EVENT_NAME ?? "", ref: env.GITHUB_REF ?? "", repository: env.GITHUB_REPOSITORY ?? "", sha: env.GITHUB_SHA ?? "", source: env.INPUT_SOURCE_SHA ?? "", publicAccess: env.INPUT_PUBLIC_ACCESS_CONFIRMED ?? "", bootstrapConfirmed: env.INPUT_BOOTSTRAP_CONFIRMED ?? "" });
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

export function verifyBootstrapPackage(value: unknown): PackageMetadata {
  const info = object(value);
  requireThat(info.name === "brine-udp-fixture" && info.package_type === "container" && info.visibility === "public" && object(info.repository).full_name === repository, "Bootstrap requires a public package linked to ShaulLavo/brine; GITHUB_TOKEN cannot legally change visibility or linkage");
  return info as unknown as PackageMetadata;
}

export function classifyBootstrapAnonymousStatus(tag: string, packageRead: PackageRead, status: number, error?: string): RegistryObservation | undefined {
  if (error !== undefined) return { tag, status: "unknown", error };
  if (packageRead.status === 404 && packageRead.value === undefined && (status === 403 || status === 404)) {
    return { tag, status: "absent", error: `anonymous bootstrap read returned ${status} for an absent package` };
  }
  if (status !== 200) return { tag, status: "unknown", error: `anonymous bootstrap read failed with status ${status}` };
  return undefined;
}

export function verifyBootstrapPreflight(packageRead: PackageRead, tags: readonly RegistryObservation[]): { packageState: "absent"; tags: string[] } {
  requireThat(packageRead.status === 404 && packageRead.value === undefined, "Bootstrap requires the exact package to be absent; use public_access_confirmed for an existing package");
  requireThat(tags.every(tag => tag.status === "absent"), "Bootstrap requires every run-scoped tag or digest to be absent");
  return { packageState: "absent", tags: tags.map(tag => tag.tag) };
}

export function classifyPackageRead(packageRead: PackageRead): "absent" | "public" | "unknown" {
  if (packageRead.status === 404 && packageRead.value === undefined) return "absent";
  if (packageRead.status !== 200 || packageRead.value === undefined) return "unknown";
  try {
    verifyBootstrapPackage(packageRead.value);
    return "public";
  } catch {
    return "unknown";
  }
}

export function classifyUnknownPackageWrite(packageRead: PackageRead, tag: PublicationDecision): "unperformed" | "reconciled" | "unknown" {
  const packageState = classifyPackageRead(packageRead);
  if (packageState === "absent" && tag.decision === "retry") return "unperformed";
  if (packageState === "public" && tag.decision === "success") return "reconciled";
  return "unknown";
}

export function reconcilePlatformPublication(intent: PlatformPublicationIntent, observation: RegistryObservation): PublicationDecision {
  if (observation.status === "absent") return { decision: "retry", tag: intent.tag, reason: "exact tag is absent" };
  if (observation.status === "unknown" || observation.raw === undefined) return { decision: "stop", tag: intent.tag, reason: observation.error ?? "exact tag state is unknown" };
  try {
    const manifestDigest = sha256(observation.raw);
    const manifest = parseManifest(observation.raw, manifestDigest);
    requireThat(observation.tag === intent.tag && (observation.digest === undefined || observation.digest === manifestDigest) && manifest.config.digest === intent.config, "Platform tag, digest or config differs from the saved push intent");
    return { decision: "success", tag: intent.tag, reference: `${image}@${manifestDigest}`, manifestDigest, configDigest: manifest.config.digest, platforms: [intent.platform] };
  } catch (error) {
    return { decision: "stop", tag: intent.tag, reason: String(error) };
  }
}

export function reconcileIndexPublication(intent: IndexPublicationIntent, observation: RegistryObservation): PublicationDecision {
  if (observation.status === "absent") return { decision: "retry", tag: intent.tag, reason: "exact index tag is absent" };
  if (observation.status === "unknown" || observation.raw === undefined) return { decision: "stop", tag: intent.tag, reason: observation.error ?? "exact index tag state is unknown" };
  try {
    const indexDigest = sha256(observation.raw);
    const entries = parseIndex(observation.raw, indexDigest);
    const expected = new Map<Arch, string>(Object.entries(intent.platformRefs).map(([platform, reference]) => {
      requireThat(reference.startsWith(image + "@"), "Index intent contains an unexpected repository");
      return [platform as Arch, digest(reference.slice(image.length + 1))];
    }));
    requireThat(observation.tag === intent.tag && (observation.digest === undefined || observation.digest === indexDigest) && entries.every(entry => entry.digest === expected.get(entry.platform.architecture)), "Index tag, digest or platform set differs from the saved index intent");
    return { decision: "success", tag: intent.tag, reference: `${image}@${indexDigest}`, indexDigest, platforms: entries.map(entry => entry.platform.architecture) };
  } catch (error) {
    return { decision: "stop", tag: intent.tag, reason: String(error) };
  }
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

function githubHeaders(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}`, Accept: "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28", "User-Agent": "brine-udp-fixture" };
}

async function githubPackageRead(): Promise<PackageRead> {
  const token = process.env.GITHUB_TOKEN;
  requireThat(token, "The workflow's built-in GITHUB_TOKEN is required");
  const response = await fetch("https://api.github.com/users/shaullavo/packages/container/brine-udp-fixture", { headers: githubHeaders(token), redirect: "error", signal: AbortSignal.timeout(15_000) });
  const body = new TextDecoder("utf-8", { fatal: true }).decode(await responseBytes(response, maxJSON));
  if (response.status !== 200) return { status: response.status };
  return { status: response.status, value: JSON.parse(body) };
}

async function registryToken(): Promise<string> {
  const token = object(JSON.parse(await readURL("https://ghcr.io/token?service=ghcr.io&scope=repository:shaullavo/brine-udp-fixture:pull")));
  return text(token.token);
}

type BootstrapRegistryTokenRead = { status: number; body?: string; error?: string };

async function bootstrapRegistryToken(): Promise<BootstrapRegistryTokenRead> {
  try {
    const response = await fetch("https://ghcr.io/token?service=ghcr.io&scope=repository:shaullavo/brine-udp-fixture:pull", { redirect: "error", signal: AbortSignal.timeout(15_000) });
    if (response.status !== 200) return { status: response.status };
    return { status: response.status, body: new TextDecoder("utf-8", { fatal: true }).decode(await responseBytes(response, maxJSON)) };
  } catch (error) {
    return { status: 0, error: String(error) };
  }
}

async function anonymousTagObservation(tag: string, packageRead: PackageRead): Promise<RegistryObservation> {
  try {
    const tokenRead = await bootstrapRegistryToken();
    const tokenObservation = classifyBootstrapAnonymousStatus(tag, packageRead, tokenRead.status, tokenRead.error);
    if (tokenObservation !== undefined) return tokenObservation;
    requireThat(tokenRead.body !== undefined, "Anonymous bootstrap token response body is missing");
    const bearer = text(object(JSON.parse(tokenRead.body)).token);
    const reference = tag.slice(`${image}:`.length);
    const response = await fetch(`https://ghcr.io/v2/shaullavo/brine-udp-fixture/manifests/${reference}`, {
      headers: { Authorization: `Bearer ${bearer}`, Accept: "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json" },
      redirect: "error", signal: AbortSignal.timeout(15_000),
    });
    const observation = classifyBootstrapAnonymousStatus(tag, packageRead, response.status);
    if (observation !== undefined) return observation;
    const raw = new TextDecoder("utf-8", { fatal: true }).decode(await responseBytes(response, maxJSON));
    return { tag, status: "present", raw, digest: sha256(raw) };
  } catch (error) {
    return { tag, status: "unknown", error: String(error) };
  }
}

async function anonymousRegistry(): Promise<(kind: "manifests" | "blobs", reference: string) => Promise<string>> {
  const bearer = await registryToken();
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
  const packageRead = await githubPackageRead();
  requireThat(packageRead.status === 200 && packageRead.value !== undefined, `GHCR package metadata read failed with status ${packageRead.status}; normal publication is fail-closed`);
  const info = object(packageRead.value) as unknown as PackageMetadata;
  verifyPublicPackage(info);
  const token = object(JSON.parse(await readURL("https://ghcr.io/token?service=ghcr.io&scope=repository:shaullavo/brine-udp-fixture:pull")));
  await readURL("https://ghcr.io/v2/shaullavo/brine-udp-fixture/tags/list?n=1", { Authorization: `Bearer ${text(token.token)}` });
  return { package: info.name, visibility: info.visibility, repository: info.repository.full_name, anonymousRead: true, checkedAt: new Date().toISOString() };
}

function runTag(release: string, platform?: Arch): string {
  const suffix = `${process.env.GITHUB_RUN_ID}-${process.env.GITHUB_RUN_ATTEMPT}`;
  return platform === undefined ? `${image}:${release}-${suffix}` : `${image}:${release}-${platform}-${suffix}`;
}

function bootstrapTags(source: string): string[] {
  return releaseIDs(source).flatMap(release => arches.map(platform => runTag(release, platform)).concat(runTag(release)));
}

async function bootstrapPreflight(source: string): Promise<unknown> {
  const packageRead = await githubPackageRead();
  const tags: RegistryObservation[] = [];
  for (const tag of bootstrapTags(source)) tags.push(await anonymousTagObservation(tag, packageRead));
  const state = verifyBootstrapPreflight(packageRead, tags);
  return { package: "brine-udp-fixture", packageState: state.packageState, repository, visibility: "pending-from-GITHUB_TOKEN-push", anonymousRead: false, tags: state.tags, checkedAt: new Date().toISOString() };
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

export function inspectProcessIdentity(name: string): string {
  const processIdentity = command(["docker", "top", name, "-eo", "pid,uid,gid,args"]);
  const lines = processIdentity.split("\n");
  requireThat(lines.length === 2 && lines[0].trim().split(/\s+/).join(" ") === "PID UID GID COMMAND" && /^[1-9][0-9]*\s+65532\s+65532\s+\/udp-echo-server$/.test(lines[1].trim()), "Running process must be exactly one /udp-echo-server with a numeric PID and UID and GID 65532");
  return processIdentity;
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
    const processIdentity = inspectProcessIdentity(name);
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

function inspectRemoteTag(tag: string): RegistryObservation {
  const result = spawnSync("docker", ["buildx", "imagetools", "inspect", "--raw", tag], { cwd: root, encoding: "utf8", timeout: 60_000, maxBuffer: maxJSON });
  if (!result.error && result.status === 0) {
    const raw = result.stdout;
    return { tag, status: "present", raw, digest: sha256(raw) };
  }
  const detail = `${result.stderr ?? ""} ${result.stdout ?? ""}`;
  if (/manifest unknown|name unknown|status code: 404|404 not found|does not exist/i.test(detail)) return { tag, status: "absent", error: detail.trim() };
  return { tag, status: "unknown", error: detail.trim() || String(result.error ?? "remote tag inspection failed") };
}

async function reconcilePlatformFailure(intent: PlatformPublicationIntent, error: unknown, bootstrap: BootstrapState | undefined, output: string, attempt: number): Promise<{ decision: PublicationDecision; raw?: string }> {
  const observation = inspectRemoteTag(intent.tag);
  const decision = reconcilePlatformPublication(intent, observation);
  let packageRead: PackageRead | undefined;
  let packageDecision: "unperformed" | "reconciled" | "unknown" | undefined;
  if (bootstrap?.packageWrite === "not_started") {
    try {
      packageRead = await githubPackageRead();
    } catch (packageError) {
      packageRead = { status: 0, value: String(packageError) };
    }
    packageDecision = classifyUnknownPackageWrite(packageRead, decision);
    if (packageDecision === "reconciled") bootstrap.packageWrite = "complete";
    if (packageDecision === "unknown") decision.decision = "stop";
  }
  save(join(output, `${intent.release}-${intent.platform}-push-reconciliation-${attempt}.json`), { intent, observation, reconciliation: decision, packageRead, packageDecision, error: String(error) });
  return { decision, raw: observation.raw };
}

async function pushPlatformOnce(entry: BuiltImage, platform: Arch, source: string, output: string, owned: Set<string>, bootstrap: BootstrapState | undefined, attempt: number): Promise<{ decision: PublicationDecision; raw?: string }> {
  const tag = runTag(entry.release, platform);
  const intent: PlatformPublicationIntent = { tag, source, release: entry.release, platform, config: entry.config };
  try {
    command(["docker", "push", tag], 300_000);
    const observation = inspectRemoteTag(tag);
    const decision = reconcilePlatformPublication(intent, observation);
    if (decision.decision === "success" && bootstrap?.packageWrite === "not_started") bootstrap.packageWrite = "complete";
    if (decision.decision === "success") return { decision, raw: observation.raw };
    return await reconcilePlatformFailure(intent, new Error(decision.reason), bootstrap, output, attempt);
  } catch (error) {
    return await reconcilePlatformFailure(intent, error, bootstrap, output, attempt);
  }
}

async function pushPlatform(entry: BuiltImage, platform: Arch, source: string, output: string, owned: Set<string>, bootstrap: BootstrapState | undefined): Promise<string> {
  owned.add(entry.config);
  command(["docker", "image", "load", "-i", entry.archive]);
  verifyLocalImage(entry.config, platform, source, entry.release, "server");
  const tag = runTag(entry.release, platform);
  save(join(output, `${entry.release}-${platform}-push-intent.json`), { source, platform, tag, config: entry.config, archiveSHA256: entry.archiveSHA256 });
  owned.add(tag);
  command(["docker", "tag", entry.config, tag]);
  let result = await pushPlatformOnce(entry, platform, source, output, owned, bootstrap, 1);
  if (result.decision.decision === "retry") result = await pushPlatformOnce(entry, platform, source, output, owned, bootstrap, 2);
  requireThat(result.decision.decision === "success" && result.decision.reference !== undefined, `Platform push did not reconcile safely: ${result.decision.reason ?? "unknown outcome"}`);
  save(join(output, `${entry.release}-${platform}-push-receipt.json`), { tag, reference: result.decision.reference, config: entry.config, manifestDigest: result.decision.manifestDigest, configDigest: result.decision.configDigest, reconciliation: result.decision.decision });
  return result.decision.reference;
}

async function publish(source: string, input: string, output: string): Promise<void> {
  newDirectory(output);
  const bootstrap = process.env.INPUT_BOOTSTRAP_CONFIRMED === "true";
  const bootstrapState: BootstrapState | undefined = bootstrap ? { source, packageState: "absent", packageWrite: "not_started" } : undefined;
  if (bootstrap) {
    const preflight = await bootstrapPreflight(source);
    save(join(output, "bootstrap-intent.json"), { schema: 1, source, package: image, repository, visibility: "public", linkage: repository, write: "first qualified image push using workflow GITHUB_TOKEN", sourceCustody: "unchanged", qualificationCustody: "unchanged", inspectionBeforeRetry: true, postDrillKeepDeleteDecision: "pending" });
    save(join(output, "bootstrap-preflight.json"), preflight);
  } else {
    save(join(output, "public-preflight.json"), await publicPreflight());
  }
  const qualified = new Map<Arch, BuiltImage[]>();
  for (const platform of arches) qualified.set(platform, await readQualification(input, source, platform));
  const baseline = new Set(command(["docker", "image", "ls", "-q", "--no-trunc"]).split("\n").concat(command(["docker", "image", "ls", "--format", "{{.Repository}}:{{.Tag}}"]).split("\n"), command(["docker", "image", "ls", "--digests", "--format", "{{.Repository}}@{{.Digest}}"]).split("\n")));
  const owned = new Set<string>();
  const interrupted = () => { save(join(output, "interrupted-cleanup.json"), { errors: cleanupImages(owned, baseline), checkedAt: new Date().toISOString() }); process.exit(130); };
  process.once("SIGINT", interrupted);
  process.once("SIGTERM", interrupted);
  let failure: string | undefined;
  try {
    const results: unknown[] = [];
    for (const release of releaseIDs(source)) results.push(await publishRelease({ source, release, qualified, output, owned, baseline, bootstrap: bootstrapState }));
    if (bootstrap) {
      const readback = await publicPreflight();
      save(join(output, "bootstrap-outcome.json"), { schema: 1, source, package: image, repository, packageWrite: bootstrapState!.packageWrite, packageReadback: readback, anonymousAccessReadback: true, decision: "verified", postDrillKeepDeleteDecision: "pending" });
    }
    save(join(output, "publication.json"), { schema: 1, source, repository, pins: pins(), inputs: inputHashes(), releases: results, completedAt: new Date().toISOString() });
    process.stdout.write(JSON.stringify(results, null, 2) + "\n");
  } catch (error) {
    failure = String(error);
    throw error;
  } finally {
    process.off("SIGINT", interrupted);
    process.off("SIGTERM", interrupted);
    if (bootstrap && !existsSync(join(output, "bootstrap-outcome.json"))) save(join(output, "bootstrap-outcome.json"), { schema: 1, source, package: image, repository, packageWrite: bootstrapState!.packageWrite, decision: "stop-and-inspect", error: failure ?? "interrupted", postDrillKeepDeleteDecision: "pending" });
    const errors = cleanupImages(owned, baseline);
    save(join(output, "cleanup.json"), { localImages: Array.from(owned), errors, remoteImages: "retained publication artifacts; never deleted automatically", checkedAt: new Date().toISOString() });
    requireThat(errors.length === 0, "Publication local-image cleanup failed");
  }
}

async function reconcileIndexFailure(intent: IndexPublicationIntent, error: unknown, output: string, attempt: number): Promise<{ decision: PublicationDecision; raw?: string }> {
  const observation = inspectRemoteTag(intent.tag);
  const decision = reconcileIndexPublication(intent, observation);
  save(join(output, `${intent.tag.slice(`${image}:`.length)}-index-reconciliation-${attempt}.json`), { intent, observation, reconciliation: decision, error: String(error) });
  return { decision, raw: observation.raw };
}

async function createIndexOnce(intent: IndexPublicationIntent, references: string[], output: string, attempt: number): Promise<{ decision: PublicationDecision; raw?: string }> {
  try {
    command(["docker", "buildx", "imagetools", "create", "--tag", intent.tag].concat(references), 120_000);
    const observation = inspectRemoteTag(intent.tag);
    const decision = reconcileIndexPublication(intent, observation);
    if (decision.decision === "success") return { decision, raw: observation.raw };
    return await reconcileIndexFailure(intent, new Error(decision.reason), output, attempt);
  } catch (error) {
    return await reconcileIndexFailure(intent, error, output, attempt);
  }
}

async function publishRelease(input: { source: string; release: string; qualified: Map<Arch, BuiltImage[]>; output: string; owned: Set<string>; baseline: ReadonlySet<string>; bootstrap: BootstrapState | undefined }): Promise<unknown> {
  const { source, release, qualified, output, owned, baseline, bootstrap } = input;
  const references: string[] = [];
  const platformRefs: Record<Arch, string> = { amd64: "", arm64: "" };
  const configs = new Map<Arch, string>();
  const manifests = new Map<Arch, string>();
  for (const platform of arches) {
    const entry = qualified.get(platform)?.find(item => item.release === release);
    requireThat(entry, "Missing native qualification");
    configs.set(platform, entry.config);
    const reference = await pushPlatform(entry, platform, source, output, owned, bootstrap);
    references.push(reference);
    platformRefs[platform] = reference;
    manifests.set(platform, reference.slice(image.length + 1));
  }
  const tag = runTag(release);
  const intent: IndexPublicationIntent = { tag, platformRefs };
  save(join(output, `${release}-index-intent.json`), { source, release, tag, references, platformRefs });
  let result = await createIndexOnce(intent, references, output, 1);
  if (result.decision.decision === "retry") result = await createIndexOnce(intent, references, output, 2);
  requireThat(result.decision.decision === "success" && result.raw !== undefined && result.decision.reference !== undefined, `Index creation did not reconcile safely: ${result.decision.reason ?? "unknown outcome"}`);
  const exact = result.raw;
  writeFileSync(join(output, `${release}-index.json`), exact);
  const publication = await recordAnonymousIndex(source, release, exact, configs, manifests, output);
  const reference = text(object(publication).reference);
  owned.add(reference);
  const images = arches.map(platform => ({ platform, config: text(configs.get(platform)), tag: runTag(release, platform) }));
  await anonymousDockerPull({ reference, release, source, images, baseline, output });
  save(join(output, `${release}-publication.json`), publication);
  return publication;
}

export type PullImageStore = { imageID(reference: string): string | undefined; pull(platform: Arch, reference: string): string; remove(reference: string): void; verify(id: string, platform: Arch): void };
type PulledImage = { platform: Arch; config: string; tag: string };

// Docker's classic image store binds a digest reference to one image ID and skips the whole
// download when the resolved config already exists. So each platform starts without this run's
// loaded image, and the index reference is dropped before the next platform resolves it.
export function qualifyAnonymousPulls(input: { reference: string; images: readonly PulledImage[]; baseline: ReadonlySet<string> }, store: PullImageStore): unknown[] {
  const { reference, images, baseline } = input;
  for (const item of images) requireThat(!baseline.has(item.config) && !baseline.has(item.tag), `Qualified image for linux/${item.platform} ${reference} (qualified config ${item.config}) existed before publication; refusing to delete it or claim an anonymous pull`);
  requireThat(!baseline.has(reference) && store.imageID(reference) === undefined, `Index reference ${reference} is already local; an anonymous pull cannot be proven`);
  for (const item of images) {
    store.remove(item.tag);
    store.remove(item.config);
    requireThat(store.imageID(item.config) === undefined, `linux/${item.platform} ${reference} (qualified config ${item.config}) is still present after removing this run's loaded image`);
  }
  return images.map(item => pullPlatform(reference, item, store));
}

function pullPlatform(reference: string, item: PulledImage, store: PullImageStore): unknown {
  const context = `Anonymous Docker pull of linux/${item.platform} ${reference} (qualified config ${item.config})`;
  requireThat(store.imageID(item.config) === undefined, `${context} found the config already local`);
  let pullOutput: string;
  try {
    pullOutput = store.pull(item.platform, reference);
  } catch (error) {
    throw new Error(`${context}: ${String(error)}`);
  }
  requireThat(!/: Already exists$/m.test(pullOutput), `${context} reused a local layer instead of downloading it`);
  const imageID = store.imageID(reference);
  requireThat(imageID === item.config, `${context} resolved image ${imageID ?? "none"}`);
  store.verify(imageID, item.platform);
  store.remove(reference);
  requireThat(store.imageID(reference) === undefined && store.imageID(item.config) === undefined, `${context} is still present after verification`);
  return { reference, platform: item.platform, config: item.config, imageID, absentBeforePull: true, removedAfterVerification: true, pullOutput: pullOutput.slice(-4096) };
}

function imageID(reference: string): string | undefined {
  const result = spawnSync("docker", ["image", "inspect", "--format", "{{.Id}}", reference], { encoding: "utf8", timeout: 15_000, maxBuffer: 4096 });
  if (!result.error && result.status === 1 && result.stderr.includes("No such image")) return undefined;
  requireThat(!result.error && result.status === 0, `Cannot inspect local image ${reference}\n${result.stderr ?? ""}`);
  return digest(result.stdout.trim());
}

export function dockerPullStore(config: string, verify: PullImageStore["verify"]): PullImageStore {
  // The containerd image store keys images by index digest and shares content, so a config's absence proves nothing there.
  requireThat(!command(["docker", "info", "--format", "{{json .DriverStatus}}"]).includes("io.containerd.snapshotter"), "Anonymous pull qualification requires Docker's classic image store; the containerd image store cannot prove a fresh per-platform download");
  return {
    imageID,
    pull: (platform, reference) => command(["docker", "--config", config, "pull", "--platform", `linux/${platform}`, reference], 120_000, false),
    remove: removeImage,
    verify,
  };
}

async function anonymousDockerPull(input: { reference: string; release: string; source: string; images: readonly PulledImage[]; baseline: ReadonlySet<string>; output: string }): Promise<void> {
  const { reference, release, source, output } = input;
  const config = join(output, `${release}-anonymous-docker`);
  mkdirSync(config, { mode: 0o700 });
  writeFileSync(join(config, "config.json"), "{}\n", { mode: 0o600 });
  const store = dockerPullStore(config, (id, platform) => verifyLocalImage(id, platform, source, release, "server"));
  try {
    const platforms = qualifyAnonymousPulls(input, store);
    save(join(output, `${release}-anonymous-pulls.json`), { reference, platforms, credentials: "empty isolated Docker config", checkedAt: new Date().toISOString() });
  } catch (error) {
    save(join(output, `${release}-anonymous-pull-failure.json`), { reference, images: input.images, error: String(error), checkedAt: new Date().toISOString() });
    throw error;
  } finally {
    rmSync(config, { recursive: true });
  }
}

export async function main(args: string[]): Promise<void> {
  const [mode, ...rest] = args;
  requireThat(["gate", "preflight", "bootstrap-preflight", "qualify", "publish"].includes(mode), "Usage: bun scripts/publish-udp-fixture.ts gate|preflight|bootstrap-preflight|qualify ARCH NEW_OUTPUT|publish INPUT NEW_OUTPUT");
  const source = sourceGate();
  requireThat(/^[0-9]+$/.test(process.env.GITHUB_RUN_ID ?? "") && /^[0-9]+$/.test(process.env.GITHUB_RUN_ATTEMPT ?? ""), "Missing workflow run identity");
  if (mode === "gate") { requireThat(rest.length === 0, "Unexpected gate arguments"); process.stdout.write(JSON.stringify({ source, releases: releaseIDs(source), pins: pins() }) + "\n"); return; }
  if (mode === "preflight") { requireThat(rest.length === 0, "Unexpected preflight arguments"); process.stdout.write(JSON.stringify(await publicPreflight()) + "\n"); return; }
  if (mode === "bootstrap-preflight") { requireThat(rest.length === 0, "Unexpected bootstrap-preflight arguments"); requireThat(process.env.INPUT_BOOTSTRAP_CONFIRMED === "true", "Bootstrap preflight requires explicit bootstrap confirmation"); process.stdout.write(JSON.stringify(await bootstrapPreflight(source)) + "\n"); return; }
  requireThat(rest.length === 2, "Expected exactly two command arguments");
  if (mode === "qualify") { await qualify(source, arch(rest[0]), rest[1]); return; }
  requireThat(isAbsolute(rest[0]) && readdirSync(rest[0]).length === 2, "Expected only amd64 and arm64 input directories");
  await publish(source, rest[0], rest[1]);
}

if (import.meta.main) main(process.argv.slice(2)).catch(error => { process.stderr.write(String(error) + "\n"); process.exitCode = 1; });
