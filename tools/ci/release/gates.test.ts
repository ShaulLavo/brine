import { expect, test } from "bun:test";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync, readFileSync } from "node:fs";
import { YAML } from "bun";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { object, releaseContext, verifyAbsentRelease, verifyGreenRun, verifyMainComparison } from "../../../scripts/release.ts";
import { source, versionTag } from "../../../scripts/build-release.ts";

const sha = "a".repeat(40);
const tag = `v${readFileSync(new URL("../../../internal/cli/root.go", import.meta.url), "utf8").match(/^const Version = "([^"]+)"$/m)?.[1]}`;
const context = { GITHUB_REPOSITORY: "ShaulLavo/brine", GITHUB_SHA: sha, BRINE_RELEASE_TAG: tag, GITHUB_EVENT_NAME: "workflow_dispatch", GITHUB_REF: "refs/heads/main" };
const run = { head_sha: sha, head_branch: "main", event: "push", path: ".github/workflows/ci.yml", status: "completed", conclusion: "success", repository: { full_name: "ShaulLavo/brine" } };

test("manual main dispatch is build-only and arbitrary refs or repositories refuse", () => {
  expect(releaseContext(context).publish).toBe(false);
  for (const changed of [{ GITHUB_EVENT_NAME: "pull_request" }, { GITHUB_REF: "refs/heads/topic" }, { GITHUB_REPOSITORY: "other/brine" }, { GITHUB_SHA: "main" }, { BRINE_RELEASE_TAG: "v9.9.9" }]) {
    expect(() => releaseContext({ ...context, ...changed })).toThrow();
  }
  expect(() => releaseContext({ ...context, GITHUB_EVENT_NAME: "push", GITHUB_REF: `refs/tags/${tag}` })).toThrow();
});

test("only exact source ancestry is admitted, not ahead-of-main or unrelated source", () => {
  for (const status of ["ahead", "identical"]) expect(() => verifyMainComparison({ status, merge_base_commit: { sha } }, sha)).not.toThrow();
  for (const status of ["behind", "diverged", "unknown"]) expect(() => verifyMainComparison({ status, merge_base_commit: { sha } }, sha)).toThrow();
  expect(() => verifyMainComparison({ status: "ahead", merge_base_commit: { sha: "b".repeat(40) } }, sha)).toThrow();
});

test("CI gate requires latest completed successful main push for exact source and workflow", () => {
  expect(() => verifyGreenRun({ workflow_runs: [run] }, sha)).not.toThrow();
  for (const changed of [{ head_sha: "b".repeat(40) }, { head_branch: "topic" }, { event: "pull_request" }, { path: ".github/workflows/release.yml" }, { status: "in_progress" }, { conclusion: "failure" }, { repository: { full_name: "other/brine" } }]) {
    expect(() => verifyGreenRun({ workflow_runs: [{ ...run, ...changed }, run] }, sha)).toThrow();
  }
  for (const value of [null, {}, { workflow_runs: [] }]) expect(() => verifyGreenRun(value, sha)).toThrow();
});

test("release preflight refuses existing, unauthenticated, ambiguous and failed observations", () => {
  expect(() => verifyAbsentRelease({ status: 404, value: { message: "Not Found" } })).not.toThrow();
  for (const status of [0, 200, 401, 403, 429, 500]) expect(() => verifyAbsentRelease({ status, value: { message: "Not Found" } })).toThrow();
  expect(() => verifyAbsentRelease({ status: 404, value: {} })).toThrow();
});

test("receipt source is actual Git source, never a caller-supplied SHA", () => {
  expect(source().head).toMatch(/^[a-f0-9]{40}$/);
  expect(() => versionTag("v9.9.9")).toThrow(/version\/tag mismatch/);
});

test("workflow permissions, runners, source pinning and attestation subjects stay separated", () => {
  const workflow = object(YAML.parse(readFileSync(new URL("../../../.github/workflows/release.yml", import.meta.url), "utf8")));
  const events = object(workflow.on);
  const jobs = object(workflow.jobs);
  const publish = object(jobs.publish);
  expect(workflow.permissions).toEqual({ contents: "read" });
  expect(object(events.workflow_dispatch).inputs).toHaveProperty("tag");
  expect(object(events.push).tags).toEqual(["v*"]);
  for (const [name, job] of Object.entries(jobs)) checkJob(name, job);
  expect(object(jobs.build).needs).toBe("gate");
  expect(object(jobs.rebuild).needs).toBe("gate");
  expect(object(jobs.compare).needs).toEqual(["build", "rebuild"]);
  expect(publish.needs).toEqual(["gate", "compare"]);
  expect(publish.if).toContain("github.event_name == 'push'");
  expect(publish.if).toContain("refs/tags/v");
  const steps = array(publish.steps).map(object);
  const attest = steps.find(step => typeof step.uses === "string" && step.uses.startsWith("actions/attest-build-provenance@"));
  const subjects = object(object(attest).with)["subject-path"];
  if (typeof subjects !== "string") throw new Error("missing attestation subjects");
  expect(subjects.trim().split("\n").map(line => line.split("/").at(-1))).toEqual([
    "brine-linux-amd64", "brine-linux-arm64", "brine-darwin-amd64", "brine-darwin-arm64", "receipt.json", "SHA256SUMS",
  ]);
  expect(object(steps.at(-1)).run).toContain("release.ts publish");
});

function array(value: unknown): unknown[] {
  if (!Array.isArray(value)) throw new Error("expected array");
  return value;
}

function checkJob(name: string, value: unknown) {
  if (!value || typeof value !== "object" || !("steps" in value) || !Array.isArray(value.steps)) throw new Error("invalid job");
  const steps = value.steps;
  const job = value;
  if (name !== "publish") expect(JSON.stringify(job)).not.toMatch(/"(?:id-token|attestations)":"write"|"contents":"write"/);
  if (name === "publish") expect(job).toHaveProperty("permissions", { contents: "write", actions: "read", "id-token": "write", attestations: "write" });
  for (const step of steps) checkStep(step);
}

function checkStep(value: unknown) {
  if (!value || typeof value !== "object") throw new Error("invalid step");
  if (!("uses" in value) || typeof value.uses !== "string") return;
  expect(value.uses).toMatch(/@[a-f0-9]{40}$/);
  if (!value.uses.startsWith("actions/checkout@")) return;
  expect(value).toHaveProperty("with.ref", "${{ github.sha }}");
  expect(value).toHaveProperty("with.persist-credentials", false);
}


test("stable publication requires an exact version tag push, never dispatch", () => {
  const root = mkdtempSync(join(tmpdir(), "brine-release-tag-"));
  try {
    mkdirSync(join(root, "internal/cli"), { recursive: true });
    writeFileSync(join(root, "internal/cli/root.go"), 'const Version = "0.1.74"\n');
    const stable = { ...context, BRINE_RELEASE_TAG: "v0.1.74", GITHUB_EVENT_NAME: "push", GITHUB_REF: "refs/tags/v0.1.74" };
    expect(releaseContext(stable, root).publish).toBe(true);
    expect(() => releaseContext({ ...stable, GITHUB_REF: "refs/tags/v0.1.75" }, root)).toThrow();
    expect(() => releaseContext({ ...stable, BRINE_RELEASE_TAG: "v0.1.75" }, root)).toThrow();
    expect(releaseContext({ ...stable, GITHUB_EVENT_NAME: "workflow_dispatch", GITHUB_REF: "refs/heads/main" }, root).publish).toBe(false);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test("real publication CLI refuses manual dispatch before credentials or network", () => {
  for (const mode of ["preflight", "publish"]) {
    const result = Bun.spawnSync([process.execPath, "scripts/release.ts", mode, "/unused"], { env: { ...process.env, ...context, GH_TOKEN: "" }, timeout: 5000 });
    expect(result.exitCode).toBe(1);
    expect(result.stderr.toString()).toContain("manual dispatch cannot publish or attest");
  }
});
