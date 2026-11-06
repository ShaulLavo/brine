import { appendFileSync, writeFileSync } from "node:fs";
import * as filesystem from "node:fs";
import { mock } from "bun:test";
import { spawnSync } from "node:child_process";

function logPath() {
  const path = process.env.FIXTURE_CALLS;
  if (!path) throw new Error("missing fixture log");
  return path;
}
const calls = logPath();
const mode = process.env.FIXTURE_API_MODE ?? "clean";
const sha = process.env.GITHUB_SHA;

function mutation() {
  if (mode.includes("head")) {
    const result = spawnSync("git", ["-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "moved"]);
    if (result.status !== 0) throw new Error("fixture commit failed");
    return;
  }
  writeFileSync("untracked", "source changed");
}

function oversized() {
  let pulled = 0;
  const body = new ReadableStream<Uint8Array>({
    pull(controller) {
      appendFileSync(calls, "chunk\n");
      controller.enqueue(new Uint8Array(65536));
      if (++pulled === 64) controller.close();
    },
    cancel() { appendFileSync(calls, "cancel\n"); },
  });
  const headers = new Headers();
  if (mode === "oversized-length") headers.set("content-length", "4194304");
  if (mode === "oversized-short-length") headers.set("content-length", "1");
  return new Response(body, { headers });
}

async function response(input: RequestInfo | URL): Promise<Response> {
  const url = String(input);
  appendFileSync(calls, "fetch\n");
  if (url.includes("/compare/")) return Response.json({ status: "identical", merge_base_commit: { sha } });
  if (url.includes("/actions/")) return ciResponse();
  if (url.includes("/commits/")) return Response.json({ sha: mode === "wrong-tag" ? "b".repeat(40) : sha });
  if (url.includes("/releases/tags/")) return absenceResponse();
  return Response.json({});
}

function ciResponse() {
  if (mode.startsWith("oversized")) return oversized();
  if (mode === "malformed") return new Response("private body diagnostic", { status: 200 });
  if (mode === "stream-error") return new Response(new ReadableStream({ start(controller) { controller.error(new Error("fixture stream stopped")); } }));
  if (mode.endsWith("-ci")) mutation();
  if (mode === "error-dirty-ci") return Response.json({}, { status: 500 });
  return Response.json({ workflow_runs: [{ head_sha: sha, head_branch: "main", event: "push", path: ".github/workflows/ci.yml", status: mode === "pending" ? "in_progress" : "completed", conclusion: mode === "failed" ? "failure" : "success", repository: { full_name: "ShaulLavo/brine" } }] });
}

function absenceResponse() {
  if (mode.endsWith("-absence")) mutation();
  const status = mode === "existing" ? 200 : mode === "unknown" ? 403 : 404;
  return Response.json({ message: "Not Found" }, { status });
}

Object.defineProperty(globalThis, "fetch", { value: response });

if (mode.startsWith("hash-")) {
  const original = { ...filesystem };
  mock.module("node:fs", () => ({ ...original, createReadStream: (...args: Parameters<typeof filesystem.createReadStream>) => hashStream(original.createReadStream(...args)) }));
}

function hashStream(stream: ReturnType<typeof filesystem.createReadStream>) {
  stream.once("end", mutation);
  return stream;
}
