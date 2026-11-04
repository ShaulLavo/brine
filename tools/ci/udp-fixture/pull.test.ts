import { expect, test } from "bun:test";
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const helperPath = resolve(import.meta.dir, "../../../scripts/publish-udp-fixture.ts");
const repository = "ghcr.io/shaullavo/brine-udp-fixture";
const index = `${repository}@sha256:${"1".repeat(64)}`;
const configs = { amd64: "sha256:" + "a".repeat(64), arm64: "sha256:" + "b".repeat(64) } as const;
const release = "p08-08-" + "2".repeat(40) + "-a";
const tags = { amd64: `${repository}:${release}-amd64-9-1`, arm64: `${repository}:${release}-arm64-9-1` } as const;
const images = (["amd64", "arm64"] as const).map(platform => ({ platform, config: configs[platform], tag: tags[platform] }));

// Models the Docker 28.0.4 classic (graphdriver) image store used by the hosted runner:
// moby reference/store.go binds one digest reference to one image ID and refuses to rebind it,
// and distribution/pull_v2.go skips every download when the resolved config already exists locally.
class ClassicStore {
  readonly refs = new Map<string, string>();
  readonly images = new Set<string>();
  readonly pulls: string[] = [];
  readonly removed: string[] = [];
  resolve: Record<string, string> = { ...configs };
  pullOutput = (_platform: string) => "abc: Pull complete\nStatus: Downloaded newer image for " + index + "\n";
  keep = new Set<string>();

  load(config: string, tag: string, manifest: string): void {
    this.images.add(config);
    this.refs.set(tag, config);
    this.refs.set(`${repository}@${manifest}`, config);
  }

  imageID(reference: string): string | undefined {
    if (this.images.has(reference)) return reference;
    return this.refs.get(reference);
  }

  pull(platform: string, reference: string): string {
    const config = this.resolve[platform];
    const downloaded = !this.images.has(config);
    this.images.add(config);
    this.pulls.push(`${platform}:${downloaded ? "downloaded" : "skipped"}`);
    const old = this.refs.get(reference);
    if (old !== undefined && old !== config) throw new Error(`cannot overwrite digest ${reference.slice(repository.length + 1)}`);
    this.refs.set(reference, config);
    return this.pullOutput(platform);
  }

  remove(reference: string): void {
    this.removed.push(reference);
    if (this.keep.has(reference)) return;
    const id = this.imageID(reference);
    if (id === undefined) return;
    if (reference === id) {
      for (const [name, target] of this.refs) if (target === id) this.refs.delete(name);
    } else {
      this.refs.delete(reference);
      if (Array.from(this.refs.values()).includes(id)) return;
    }
    this.images.delete(id);
  }

  verify(id: string, platform: string): void {
    if (id !== configs[platform as keyof typeof configs]) throw new Error("Image platform differs from qualification");
  }
}

function published(): ClassicStore {
  const store = new ClassicStore();
  store.load(configs.amd64, tags.amd64, "sha256:" + "c".repeat(64));
  store.load(configs.arm64, tags.arm64, "sha256:" + "d".repeat(64));
  return store;
}

test("boundary control: the previous sequential index pull reproduces the run 41809740785 failure", () => {
  const store = published();
  store.pull("amd64", index);
  expect(() => store.pull("arm64", index)).toThrow(`cannot overwrite digest sha256:${"1".repeat(64)}`);
  expect(store.pulls).toEqual(["amd64:skipped", "arm64:skipped"]);
});

test("anonymous pulls download each exact platform config through the immutable index", async () => {
  const { qualifyAnonymousPulls } = await import(helperPath);
  const store = published();
  const receipts = qualifyAnonymousPulls({ reference: index, images, baseline: new Set() }, store);
  expect(store.pulls).toEqual(["amd64:downloaded", "arm64:downloaded"]);
  expect(receipts.map((receipt: { platform: string }) => receipt.platform)).toEqual(["amd64", "arm64"]);
  expect(receipts[1]).toMatchObject({ reference: index, platform: "arm64", config: configs.arm64, imageID: configs.arm64, absentBeforePull: true, removedAfterVerification: true });
  expect(store.images.size).toBe(0);
  expect(store.refs.size).toBe(0);
});

test("anonymous pulls never delete images that existed before publication", async () => {
  const { qualifyAnonymousPulls } = await import(helperPath);
  for (const preexisting of [configs.arm64, tags.amd64]) {
    const store = published();
    expect(() => qualifyAnonymousPulls({ reference: index, images, baseline: new Set([preexisting]) }, store)).toThrow(/existed before publication/);
    expect(store.removed).toEqual([]);
    expect(store.pulls).toEqual([]);
  }
});

test("anonymous pull failures name the exact platform, index reference and qualified config", async () => {
  const { qualifyAnonymousPulls } = await import(helperPath);
  const substituted = published();
  substituted.resolve = { amd64: configs.amd64, arm64: configs.amd64 };
  expect(() => qualifyAnonymousPulls({ reference: index, images, baseline: new Set() }, substituted))
    .toThrow(`linux/arm64 ${index} (qualified config ${configs.arm64})`);

  const failing = published();
  failing.pull = () => { throw new Error("docker --config failed or timed out\nunauthorized"); };
  expect(() => qualifyAnonymousPulls({ reference: index, images, baseline: new Set() }, failing))
    .toThrow(new RegExp(`linux/amd64 ${index.replace(/[.@:]/g, "\\$&")} \\(qualified config ${configs.amd64}\\): .*unauthorized`, "s"));
});

test("anonymous pulls refuse reused layers and unremoved local images", async () => {
  const { qualifyAnonymousPulls } = await import(helperPath);
  const reused = published();
  reused.pullOutput = () => "abc: Already exists\nStatus: Downloaded newer image for " + index + "\n";
  expect(() => qualifyAnonymousPulls({ reference: index, images, baseline: new Set() }, reused)).toThrow(/linux\/amd64.*reused a local layer/s);

  const pinned = published();
  pinned.keep.add(configs.amd64);
  expect(() => qualifyAnonymousPulls({ reference: index, images, baseline: new Set() }, pinned)).toThrow(/linux\/amd64.*still present/s);
  expect(pinned.pulls).toEqual([]);

  const leftover = published();
  leftover.keep.add(index);
  expect(() => qualifyAnonymousPulls({ reference: index, images, baseline: new Set() }, leftover)).toThrow(/linux\/amd64.*still present after verification/s);
  expect(leftover.pulls).toEqual(["amd64:downloaded"]);
});

test("the Docker pull store refuses the containerd image store, where config absence proves nothing", () => {
  const directory = mkdtempSync(join(process.env.TMPDIR ?? "/tmp", "udp-pull-store-"));
  try {
    writeFileSync(join(directory, "docker"), `#!/bin/sh
[ "$*" = 'info --format {{json .DriverStatus}}' ] && printf '%s\\n' "$DRIVER_STATUS" && exit 0
exit 1
`);
    chmodSync(join(directory, "docker"), 0o700);
    const open = (status: string) => Bun.spawnSync([process.execPath, "-e", `import { dockerPullStore } from ${JSON.stringify(helperPath)}; dockerPullStore("/nonexistent", () => {});`], {
      env: { ...process.env, PATH: `${directory}:${process.env.PATH}`, DRIVER_STATUS: status }, timeout: 5000,
    });
    expect(open('[["Backing Filesystem","extfs"],["Supports d_type","true"]]').exitCode).toBe(0);
    const containerd = open('[["driver-type","io.containerd.snapshotter.v1"]]');
    expect(containerd.exitCode).toBe(1);
    expect(containerd.stderr.toString()).toContain("classic image store");
  } finally {
    rmSync(directory, { recursive: true });
  }
});
