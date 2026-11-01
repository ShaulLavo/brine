import { describe, expect, test } from "bun:test";
import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";

const root = resolve(import.meta.dir, "../../..");
const script = resolve(root, "scripts/publish-udp-fixture.ts");
const workflow = resolve(root, ".github/workflows/udp-fixture.yml");

test("publication has an executable typed helper", () => {
  expect(existsSync(script)).toBe(true);
});

test("workflow is manual and has a source gate before native qualification and publication", () => {
  expect(existsSync(workflow)).toBe(true);
  if (!existsSync(workflow)) return;
  const parsed = Bun.YAML.parse(readFileSync(workflow, "utf8"));
  expect(field(parsed, "on")).toEqual({ workflow_dispatch: { inputs: {
    source_sha: { description: "Exact reviewed main commit", required: true, type: "string" },
    public_access_confirmed: { description: "Existing GHCR package is approved for anonymous public pulls", required: true, default: false, type: "boolean" },
    bootstrap_confirmed: { description: "One-time bootstrap: create this package from this workflow and require public linked readback", required: true, default: false, type: "boolean" },
  } } });
  expect(field(parsed, "permissions")).toEqual({ contents: "read" });
  expect(field(parsed, "jobs", "gate", "if")).toBe("github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main' && github.repository == 'ShaulLavo/brine'");
  expect(field(parsed, "jobs", "qualify", "needs")).toBe("gate");
  expect(field(parsed, "jobs", "gate", "steps", "4", "if")).toBe("inputs.bootstrap_confirmed == false");
  expect(field(parsed, "jobs", "gate", "steps", "5", "if")).toBe("inputs.bootstrap_confirmed == true");
  expect(field(parsed, "jobs", "publish", "needs")).toEqual(["gate", "qualify"]);
  expect(field(parsed, "jobs", "publish", "steps", "4", "if")).toBe("inputs.bootstrap_confirmed == false");
  expect(field(parsed, "jobs", "publish", "steps", "5", "if")).toBe("inputs.bootstrap_confirmed == true");
  expect(field(parsed, "jobs", "qualify", "strategy", "matrix", "include")).toEqual([
    { arch: "amd64", runner: "ubuntu-24.04" },
    { arch: "arm64", runner: "ubuntu-24.04-arm" },
  ]);
  expect(field(parsed, "jobs", "publish", "permissions")).toEqual({ contents: "read", packages: "write" });
  const serialized = readFileSync(workflow, "utf8");
  expect(serialized).not.toContain("secrets.GHCR");
  expect(serialized).not.toMatch(/GH_TOKEN|PAT|PERSONAL_ACCESS_TOKEN|repository_id|PATCH/);
  expect(serialized).not.toContain("latest");
  for (const match of serialized.matchAll(/uses: ([^\n]+)/g)) {
    expect(match[1]).toMatch(/^[\w/-]+@[a-f0-9]{40}(?:\s+#.*)?$/);
  }
});

describe("publication boundary", () => {
  test("helper exports independently testable source and OCI parsers", async () => {
    expect(existsSync(script)).toBe(true);
    if (!existsSync(script)) return;
    const helper = await import(script);
    expect(helper.validateDispatch).toBeFunction();
    expect(helper.parseIndex).toBeFunction();
    expect(helper.parseManifest).toBeFunction();
    expect(helper.verifyConfig).toBeFunction();
  });
});

function field(value: unknown, ...keys: string[]): unknown {
  let result = value;
  for (const key of keys) {
    if (typeof result !== "object" || result === null || !(key in result)) throw new Error(`Missing workflow key ${key}`);
    result = Reflect.get(result, key);
  }
  return result;
}

test("native jobs have no write credential and both publication boundaries recheck public access", () => {
  const parsed = Bun.YAML.parse(readFileSync(workflow, "utf8"));
  expect(field(parsed, "jobs", "gate", "permissions")).toEqual({ contents: "read", packages: "read" });
  expect(Object.keys(object(field(parsed, "jobs", "qualify")))).not.toContain("permissions");
  for (const job of ["gate", "qualify", "publish"]) {
    expect(field(parsed, "jobs", job, "steps", "0", "with", "ref")).toBe("${{ github.sha }}");
    expect(field(parsed, "jobs", job, "steps", "0", "with", "persist-credentials")).toBe(false);
    expect(field(parsed, "jobs", job, "steps", "1", "with", "bun-version")).toBe("1.4.4");
  }
  expect(field(parsed, "jobs", "gate", "steps", "3", "run")).toBe("bun scripts/publish-udp-fixture.ts gate");
  expect(field(parsed, "jobs", "gate", "steps", "4", "run")).toBe("bun scripts/publish-udp-fixture.ts preflight");
  expect(field(parsed, "jobs", "gate", "steps", "5", "run")).toBe("bun scripts/publish-udp-fixture.ts bootstrap-preflight");
  expect(field(parsed, "jobs", "publish", "steps", "4", "run")).toBe("bun scripts/publish-udp-fixture.ts preflight");
  expect(field(parsed, "jobs", "publish", "steps", "5", "run")).toBe("bun scripts/publish-udp-fixture.ts bootstrap-preflight");
  expect(field(parsed, "jobs", "publish", "steps", "7", "uses")).toBe("docker/login-action@c94ce9fb468520275223c153574b00df6fe4bcc9");
  expect(field(parsed, "jobs", "qualify", "steps", "3", "with", "version")).toBe("v0.29.1");
  expect(field(parsed, "jobs", "qualify", "steps", "3", "with", "driver-opts")).toBe("image=docker.io/moby/buildkit:v0.25.1@sha256:79cc6476ab1a3371c9afd8b44e7c55610057c43e18d9b39b68e2b0c2475cc1b6");
});

function object(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error("Expected a workflow object");
  return value as Record<string, unknown>;
}


test("bootstrap path records atomic intent and outcome without remote visibility or linkage mutation", () => {
  const serialized = readFileSync(script, "utf8");
  expect(serialized).toContain("bootstrap-intent.json");
  expect(serialized).toContain("bootstrap-outcome.json");
  expect(serialized).toContain("inspectUnknownPackageWrite");
  expect(serialized).toContain("GITHUB_TOKEN");
  expect(serialized).not.toMatch(/gh api|PATCH|repository_id/);
});
