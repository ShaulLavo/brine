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
  expect(parsed.on).toEqual({ workflow_dispatch: { inputs: {
    source_sha: { description: "Exact reviewed main commit", required: true, type: "string" },
    public_access_confirmed: { description: "Existing GHCR package is approved for anonymous public pulls", required: true, default: false, type: "boolean" },
  } } });
  expect(parsed.permissions).toEqual({ contents: "read" });
  expect(parsed.jobs.gate.if).toBe("github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main' && github.repository == 'ShaulLavo/brine'");
  expect(parsed.jobs.qualify.needs).toBe("gate");
  expect(parsed.jobs.publish.needs).toEqual(["gate", "qualify"]);
  expect(parsed.jobs.qualify.strategy.matrix.include).toEqual([
    { arch: "amd64", runner: "ubuntu-24.04" },
    { arch: "arm64", runner: "ubuntu-24.04-arm" },
  ]);
  expect(parsed.jobs.publish.permissions).toEqual({ contents: "read", packages: "write" });
  const serialized = readFileSync(workflow, "utf8");
  expect(serialized).not.toContain("secrets.GHCR");
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
