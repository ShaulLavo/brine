import { compare, command, unchanged, withCleanSource, verifyDirectory, versionTag, releaseFiles } from "./build-release.ts";
import { join } from "node:path";

const repository = "ShaulLavo/brine";
const apiRoot = `https://api.github.com/repos/${repository}`;

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function object(value: unknown): Record<string, unknown> {
  if (!isRecord(value)) throw new Error("expected GitHub object");
  return value;
}

async function api(path: string) {
  const token = process.env.GH_TOKEN;
  if (!token) throw new Error("missing GitHub token");
  const response = await fetch(`${apiRoot}${path ? `/${path}` : ""}`, {
    headers: { Authorization: `Bearer ${token}`, Accept: "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28" },
    signal: AbortSignal.timeout(30_000),
  });
  const raw = await boundedBody(response);
  const value = parseJSON(raw);
  return { status: response.status, value };
}

async function boundedBody(response: Response) {
  const reader = response.body?.getReader();
  if (!reader) return "";
  try { return await readBody(reader, response.headers.get("content-length")); }
  catch (error) {
    await reader.cancel().catch(() => {});
    throw error;
  } finally { reader.releaseLock(); }
}

async function readBody(reader: ReadableStreamDefaultReader<Uint8Array>, length: string | null) {
  const cap = 1024 * 1024;
  if (length !== null && Number(length) > cap) throw new Error("GitHub response exceeds 1 MiB");
  const chunks: Uint8Array[] = [];
  let bytes = 0;
  while (true) {
    const chunk = await reader.read();
    if (chunk.done) break;
    bytes += chunk.value.byteLength;
    if (bytes > cap) throw new Error("GitHub response exceeds 1 MiB");
    chunks.push(chunk.value);
  }
  return Buffer.concat(chunks, bytes).toString("utf8");
}

function parseJSON(raw: string): unknown {
  if (!raw) return null;
  try { return JSON.parse(raw); }
  catch { throw new Error("invalid GitHub JSON response"); }
}

async function readAPI(path: string) {
  const result = await api(path);
  if (result.status !== 200) throw new Error(`GitHub read failed with HTTP ${result.status}`);
  return object(result.value);
}

export function releaseContext(env = process.env, cwd?: string) {
  const tag = env.BRINE_RELEASE_TAG ?? "";
  const sha = env.GITHUB_SHA ?? "";
  if (env.GITHUB_REPOSITORY !== repository || !/^[0-9a-f]{40}$/.test(sha)) throw new Error("release requires the canonical repository and exact source SHA");
  versionTag(tag, cwd);
  if (env.GITHUB_EVENT_NAME === "workflow_dispatch" && env.GITHUB_REF === "refs/heads/main") return { tag, sha, publish: false };
  if (env.GITHUB_EVENT_NAME === "push" && env.GITHUB_REF === `refs/tags/${tag}` && /^v[0-9]+\.[0-9]+\.[0-9]+$/.test(tag)) return { tag, sha, publish: true };
  throw new Error("release requires main dry-run or stable version tag push");
}

export function verifyMainComparison(value: unknown, sha: string) {
  const comparison = object(value);
  if ((comparison.status !== "ahead" && comparison.status !== "identical") || object(comparison.merge_base_commit).sha !== sha) throw new Error("source is not on main");
}

export function verifyGreenRun(value: unknown, sha: string) {
  const runs = object(value).workflow_runs;
  if (!Array.isArray(runs) || runs.length === 0) throw new Error("no main CI run for source");
  // The API returns newest first. Never accept an older green attempt over a newer failure or pending run.
  const latest = object(runs[0]);
  if (latest.head_sha !== sha || latest.head_branch !== "main" || latest.event !== "push" || latest.path !== ".github/workflows/ci.yml" || latest.status !== "completed" || latest.conclusion !== "success" || object(latest.repository).full_name !== repository) throw new Error("latest source main CI is not successful");
}

export function verifyAbsentRelease(result: { status: number; value: unknown }) {
  if (result.status !== 404 || object(result.value).message !== "Not Found") throw new Error("release exists or absence is unknown; refusing overwrite");
}

async function gate() {
  return withCleanSource(async snapshot => {
    const context = releaseContext();
    if (snapshot.head !== context.sha) throw new Error("checkout does not match clean workflow source");
    await gateReads(context);
    return context;
  });
}

async function gateReads(context: ReturnType<typeof releaseContext>) {
  verifyMainComparison(await readAPI(`compare/${context.sha}...main`), context.sha);
  verifyGreenRun(await readAPI(`actions/workflows/ci.yml/runs?head_sha=${context.sha}&event=push&branch=main&per_page=1`), context.sha);
  if (!context.publish) return;
  const commit = await readAPI(`commits/${encodeURIComponent(context.tag)}`);
  if (commit.sha !== context.sha) throw new Error("tag does not identify workflow source");
}

async function preflight(directory: string) {
  if (!releaseContext().publish) throw new Error("manual dispatch cannot publish or attest");
  return withCleanSource(async () => {
    const context = await gate();
    await verifyDirectory(directory, context.tag);
    await readAPI(""); // A successful repository read rules out a hidden/unauthorized repository 404.
    verifyAbsentRelease(await api(`releases/tags/${context.tag}`));
    return context;
  });
}

async function publish(directory: string) {
  if (!releaseContext().publish) throw new Error("manual dispatch cannot publish or attest");
  return withCleanSource(async snapshot => {
    const { tag, sha } = await preflight(directory);
    const paths = releaseFiles.map(name => join(directory, name));
    unchanged(snapshot, "release verification");
    // No upload --clobber, edit, delete, or retry: an ambiguous create must be inspected, never replayed.
    command(["gh", "release", "create", tag].concat(paths, ["--repo", repository, "--verify-tag", "--target", sha, "--title", `Brine ${tag}`, "--notes", `Experimental Brine client binaries from ${sha}. Verify attestations and SHA256SUMS as described in docs/releases.md.`, "--latest=false"]));
  });
}

async function main() {
  const [mode, first, second] = process.argv.slice(2);
  if (mode === "gate" && !first) { await gate(); return; }
  if (mode === "compare" && first && second) { await compare(first, second, process.env.BRINE_RELEASE_TAG ?? ""); return; }
  if (mode === "verify" && first && !second) { await verifyDirectory(first, process.env.BRINE_RELEASE_TAG ?? ""); return; }
  if (mode === "preflight" && first && !second) { await preflight(first); return; }
  if (mode === "publish" && first && !second) { await publish(first); return; }
  throw new Error("usage: release.ts gate|compare DIR DIR|verify DIR|preflight DIR|publish DIR");
}

if (import.meta.main) {
  try { await main(); }
  catch (error) {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  }
}
