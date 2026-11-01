import { expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { resolve } from "node:path";

const helperPath = resolve(import.meta.dir, "../../../scripts/publish-udp-fixture.ts");
const sha = "1".repeat(40);
const digest = (text: string) => "sha256:" + createHash("sha256").update(text).digest("hex");
const dispatch = {
  event: "workflow_dispatch", ref: "refs/heads/main", repository: "ShaulLavo/brine",
  sha, source: sha, publicAccess: "true", bootstrapConfirmed: "false",
};

test("dispatch refuses every non-main, mismatched, missing and unapproved input", async () => {
  const { validateDispatch } = await import(helperPath);
  expect(validateDispatch(dispatch)).toBe(sha);
  expect(validateDispatch({ ...dispatch, publicAccess: "false", bootstrapConfirmed: "true" })).toBe(sha);
  for (const changed of [
    { event: "push" }, { ref: "refs/heads/feature" }, { repository: "other/brine" },
    { source: "main" }, { source: "2".repeat(40) }, { sha: "" },
    { publicAccess: "false" }, { publicAccess: "" }, { bootstrapConfirmed: "true" }, { publicAccess: "true", bootstrapConfirmed: "true" }, { source: sha + "\n" },
  ]) {
    expect(() => validateDispatch({ ...dispatch, ...changed })).toThrow();
  }
});

test("source evidence refuses dirty files, wrong HEAD and moving main", async () => {
  const { verifySourceFacts } = await import(helperPath);
  expect(() => verifySourceFacts(sha, { head: sha, main: sha, dirty: "" })).not.toThrow();
  for (const changed of [{ head: "2".repeat(40) }, { main: "2".repeat(40) }, { dirty: "?? new.ts" }]) {
    expect(() => verifySourceFacts(sha, { head: sha, main: sha, dirty: "", ...changed })).toThrow();
  }
});

test("index custody requires exact bytes and exactly two supported platform manifests", async () => {
  const { parseIndex } = await import(helperPath);
  const manifests = ["amd64", "arm64"].map((architecture, i) => ({
    mediaType: "application/vnd.oci.image.manifest.v1+json", digest: "sha256:" + String(i + 1).repeat(64), size: 500,
    platform: { os: "linux", architecture },
  }));
  const index = JSON.stringify({ schemaVersion: 2, mediaType: "application/vnd.oci.image.index.v1+json", manifests });
  expect(parseIndex(index, digest(index))).toEqual(manifests);
  expect(() => parseIndex(index, "sha256:" + "f".repeat(64))).toThrow();
  for (const values of [[], manifests.slice(0, 1), manifests.concat(manifests[0]), [manifests[0], manifests[0]],
    [manifests[0], { ...manifests[1], platform: { os: "linux", architecture: "s390x" } }]]) {
    const raw = JSON.stringify({ schemaVersion: 2, manifests: values });
    expect(() => parseIndex(raw, digest(raw))).toThrow();
  }
});

test("manifest custody binds the config and layer descriptors", async () => {
  const { parseManifest } = await import(helperPath);
  const descriptor = { mediaType: "application/vnd.oci.image.config.v1+json", digest: "sha256:" + "a".repeat(64), size: 400 };
  const raw = JSON.stringify({ schemaVersion: 2, config: descriptor, layers: [{ ...descriptor, mediaType: "application/vnd.oci.image.layer.v1.tar+gzip" }] });
  expect(parseManifest(raw, digest(raw)).config).toEqual(descriptor);
  expect(() => parseManifest(raw, "sha256:" + "b".repeat(64))).toThrow();
  const broken = raw.replace('"size":400', '"size":-1');
  expect(() => parseManifest(broken, digest(broken))).toThrow();
});

test("runtime config refuses root, wrong platform, entrypoint and source or release substitution", async () => {
  const { verifyConfig } = await import(helperPath);
  const config = {
    os: "linux", architecture: "amd64", config: {
      User: "65532:65532", Entrypoint: ["/udp-echo-server"],
      Labels: { "org.opencontainers.image.revision": sha, "org.opencontainers.image.source": "https://github.com/ShaulLavo/brine", "org.opencontainers.image.version": `p08-08-${sha}-a` },
    },
  };
  expect(() => verifyConfig(config, "amd64", sha, `p08-08-${sha}-a`, "server")).not.toThrow();
  for (const changed of [{ User: "0" }, { Entrypoint: ["/bin/sh"] }, { Labels: {} }]) {
    expect(() => verifyConfig({ ...config, config: { ...config.config, ...changed } }, "amd64", sha, `p08-08-${sha}-a`, "server")).toThrow();
  }
  expect(() => verifyConfig(config, "arm64", sha, `p08-08-${sha}-a`, "server")).toThrow();
  expect(() => verifyConfig(config, "amd64", sha, `p08-08-${sha}-b`, "server")).toThrow();
});

test("package visibility is a gate, never an automatic change", async () => {
  const { verifyPublicPackage } = await import(helperPath);
  const value = { name: "brine-udp-fixture", package_type: "container", visibility: "public", repository: { full_name: "ShaulLavo/brine" } };
  expect(() => verifyPublicPackage(value)).not.toThrow();
  for (const changed of [{ visibility: "private" }, { visibility: "internal" }, { repository: null }, { repository: { full_name: "other/brine" } }]) {
    expect(() => verifyPublicPackage({ ...value, ...changed })).toThrow();
  }
});

test("bootstrap rejects any existing package or run-scoped tag and digest", async () => {
  const { verifyBootstrapPreflight } = await import(helperPath);
  const absent = { status: 404 };
  expect(() => verifyBootstrapPreflight({ status: 200, value: { name: "brine-udp-fixture" } }, [])).toThrow(/package to be absent/);
  expect(() => verifyBootstrapPreflight(absent, [{ tag: "ghcr.io/shaullavo/brine-udp-fixture:run-tag", status: "present", digest: "sha256:" + "a".repeat(64) }])).toThrow(/run-scoped tag or digest to be absent/);
  expect(verifyBootstrapPreflight(absent, [{ tag: "ghcr.io/shaullavo/brine-udp-fixture:run-tag", status: "absent" }]).packageState).toBe("absent");
});

test("unknown platform push reconciliation binds the exact tag, digest, manifest and config", async () => {
  const { reconcilePlatformPublication } = await import(helperPath);
  const configText = JSON.stringify({ os: "linux", architecture: "amd64", config: { User: "65532:65532", Entrypoint: ["/udp-echo-server"], Labels: { "org.opencontainers.image.revision": sha, "org.opencontainers.image.source": "https://github.com/ShaulLavo/brine", "org.opencontainers.image.version": `p08-08-${sha}-a` } } });
  const configDigest = digest(configText);
  const manifest = JSON.stringify({ schemaVersion: 2, config: { mediaType: "application/vnd.oci.image.config.v1+json", digest: configDigest, size: Buffer.byteLength(configText) }, layers: [{ mediaType: "application/vnd.oci.image.layer.v1.tar+gzip", digest: "sha256:" + "b".repeat(64), size: 10 }] });
  const tag = "ghcr.io/shaullavo/brine-udp-fixture:platform-tag";
  const result = reconcilePlatformPublication({ tag, source: sha, release: `p08-08-${sha}-a`, platform: "amd64", config: configDigest }, { tag, status: "present", raw: manifest });
  expect(result).toMatchObject({ decision: "success", tag, manifestDigest: digest(manifest), configDigest });
  expect(reconcilePlatformPublication({ tag, source: sha, release: `p08-08-${sha}-a`, platform: "amd64", config: configDigest }, { tag, status: "absent" }).decision).toBe("retry");
  expect(reconcilePlatformPublication({ tag, source: sha, release: `p08-08-${sha}-a`, platform: "amd64", config: configDigest }, { tag, status: "unknown" }).decision).toBe("stop");
});

test("unknown index creation reconciliation binds the exact digest and platform set", async () => {
  const { reconcileIndexPublication } = await import(helperPath);
  const platformRefs = { amd64: "ghcr.io/shaullavo/brine-udp-fixture@sha256:" + "a".repeat(64), arm64: "ghcr.io/shaullavo/brine-udp-fixture@sha256:" + "b".repeat(64) };
  const index = JSON.stringify({ schemaVersion: 2, manifests: [
    { mediaType: "application/vnd.oci.image.manifest.v1+json", digest: platformRefs.amd64.slice("ghcr.io/shaullavo/brine-udp-fixture@".length), size: 500, platform: { os: "linux", architecture: "amd64" } },
    { mediaType: "application/vnd.oci.image.manifest.v1+json", digest: platformRefs.arm64.slice("ghcr.io/shaullavo/brine-udp-fixture@".length), size: 500, platform: { os: "linux", architecture: "arm64" } },
  ] });
  const tag = "ghcr.io/shaullavo/brine-udp-fixture:index-tag";
  const intent = { tag, platformRefs };
  expect(reconcileIndexPublication(intent, { tag, status: "present", raw: index })).toMatchObject({ decision: "success", tag, indexDigest: digest(index), platforms: ["amd64", "arm64"] });
  expect(reconcileIndexPublication(intent, { tag, status: "absent" }).decision).toBe("retry");
  const broken = JSON.stringify({ ...JSON.parse(index), manifests: JSON.parse(index).manifests.slice(0, 1) });
  expect(reconcileIndexPublication(intent, { tag, status: "present", raw: broken }).decision).toBe("stop");
});

test("runtime echo proof requires the exact nonce, compiled identity and explicit bounds", async () => {
  const { verifyEcho } = await import(helperPath);
  const proof = { schema: 1, fixture: "brine-udp-echo", endpoint: "127.0.0.1:7777", expected_release: "release-a", attempts: 1, attempt_timeout_ms: 1000, overall_timeout_ms: 2000, nonce: "nonce-a", outcome: "ok", response: { release: "release-a", nonce: "nonce-a" } };
  expect(() => verifyEcho(proof, "release-a", false)).not.toThrow();
  for (const changed of [{ response: { release: "release-b", nonce: "nonce-a" } }, { response: { release: "release-a", nonce: "other" } }, { overall_timeout_ms: 15000 }, { attempts: 3 }, { endpoint: "192.0.2.1:7777" }]) {
    expect(() => verifyEcho({ ...proof, ...changed }, "release-a", false)).toThrow();
  }
  expect(() => verifyEcho({ ...proof, expected_release: "wrong-release", outcome: "protocol_error", error: "response release mismatch" }, "release-a", true)).not.toThrow();
});


test("normal publication accepts the already-public exact package while bootstrap rejects it", async () => {
  const { verifyPublicPackage, verifyBootstrapPackage, classifyPackageRead, classifyUnknownPackageWrite } = await import(helperPath);
  const value = { name: "brine-udp-fixture", package_type: "container", visibility: "public", repository: { full_name: "ShaulLavo/brine" } };
  expect(() => verifyPublicPackage(value)).not.toThrow();
  expect(() => verifyBootstrapPackage(value)).not.toThrow();
  expect(classifyPackageRead({ status: 200, value })).toBe("public");
  expect(classifyUnknownPackageWrite({ status: 404 }, { decision: "retry", tag: "tag" })).toBe("unperformed");
  expect(classifyUnknownPackageWrite({ status: 200, value }, { decision: "success", tag: "tag" })).toBe("reconciled");
  expect(classifyUnknownPackageWrite({ status: 200, value: { ...value, visibility: "private" } }, { decision: "success", tag: "tag" })).toBe("unknown");
  expect(classifyUnknownPackageWrite({ status: 403 }, { decision: "success", tag: "tag" })).toBe("unknown");
});
