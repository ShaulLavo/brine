import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { createHandler } from './worker.mjs';

const bytes = Buffer.from([0, 255, 19, 140, 12]);
const digest = createHash('sha256').update(bytes).digest('hex');
const policy = { hostname: 'game.example.test', prefix: '/files/', extensions: ['bin'], namespace: 'fixture', ttlSeconds: 31536000 };
const url = `https://${policy.hostname}${policy.prefix}${digest}.bin`;

function fixture(responseFields = {}, status = 200, selectedPolicy = policy) {
  const entries = new Map();
  const origin = [];
  const originOptions = [];
  const lookups = [];
  const writes = [];
  const pending = [];
  const keyOf = request => request.url;
  const handler = createHandler(selectedPolicy, {
    async fetch(request, options) {
      origin.push(request);
      originOptions.push(options);
      return new Response(bytes, { status, headers: {
        'Content-Type': 'application/octet-stream', 'Content-Length': String(bytes.length),
        'Cache-Control': 'public, max-age=31536000, immutable', ...responseFields,
      } });
    },
    async cache() { return {
      async match(request) { lookups.push(request); return entries.get(keyOf(request))?.clone(); },
      async put(request, response) { writes.push(request); entries.set(keyOf(request), new Response(await response.arrayBuffer(), response)); },
    }; },
  });
  const context = { waitUntil(promise) { pending.push(promise); } };
  return { origin, originOptions, lookups, writes, entries,
    async request(request = new Request(url)) {
      const response = await handler(request, context);
      await Promise.all(pending.splice(0));
      return response;
    },
  };
}

test('game-shaped no-store requests share an explicit edge entry and preserve exact bytes', async () => {
  const box = fixture();
  const request = () => new Request(url, { cache: 'no-store', headers: {
    'Cache-Control': 'no-cache', Pragma: 'no-cache', Accept: 'application/octet-stream',
  } });
  const first = await box.request(request());
  const second = await box.request(request());
  assert.equal(first.headers.get('x-brine-cdn-cache'), 'MISS');
  assert.equal(second.headers.get('x-brine-cdn-cache'), 'HIT');
  assert.deepEqual(Buffer.from(await first.arrayBuffer()), bytes);
  assert.deepEqual(Buffer.from(await second.arrayBuffer()), bytes);
  assert.equal(box.origin.length, 1);
  assert.equal(box.lookups[0].headers.get('cache-control'), null);
  assert.equal(box.lookups[0].headers.get('pragma'), null);
  assert.equal(box.lookups[0].headers.get('accept'), 'application/octet-stream');
  assert.equal(box.origin[0].headers.get('cache-control'), 'no-cache');
  assert.equal(box.origin[0].redirect, 'manual');
  assert.deepEqual(box.originOptions[0], { cf: { cacheTtlByStatus: { '100-599': -1 } } });
});

test('HEAD fills one full GET representation and preserves its length without a response body', async () => {
  const box = fixture();
  const head = await box.request(new Request(url, { method: 'HEAD' }));
  assert.equal(head.headers.get('content-length'), String(bytes.length));
  assert.equal((await head.arrayBuffer()).byteLength, 0);
  assert.equal(box.origin[0].method, 'GET');
  assert.equal(box.writes[0].method, 'GET');
  const get = await box.request();
  assert.equal(get.headers.get('x-brine-cdn-cache'), 'HIT');
  assert.deepEqual(Buffer.from(await get.arrayBuffer()), bytes);
});

test('response bytes stream before the origin closes, with cache storage in the request lifetime', { timeout: 2000 }, async () => {
  const body = Uint8Array.from([1, 2, 3, 4]);
  const hash = createHash('sha256').update(body).digest('hex');
  const pending = [];
  let finish;
  const source = new ReadableStream({ start(controller) {
    controller.enqueue(body.slice(0, 2));
    finish = () => { controller.enqueue(body.slice(2)); controller.close(); };
  } });
  const handler = createHandler(policy, {
    async fetch() { return new Response(source, { headers: { 'Content-Length': '4', 'Content-Type': 'application/octet-stream' } }); },
    async cache() { return { async match() {}, async put(key, response) { await response.arrayBuffer(); } }; },
  });
  const response = await handler(new Request(`https://${policy.hostname}${policy.prefix}${hash}.bin`), { waitUntil(value) { pending.push(value); } });
  const reader = response.body.getReader();
  assert.deepEqual((await reader.read()).value, body.slice(0, 2));
  finish();
  assert.deepEqual((await reader.read()).value, body.slice(2));
  assert.equal((await reader.read()).done, true);
  await Promise.all(pending);
});

const shared = JSON.parse(await readFile(new URL('../testdata/eligibility.json', import.meta.url), 'utf8'));
const sharedPolicy = { ...shared.policy, namespace: 'shared-fixture', ttlSeconds: shared.policy.ttl_seconds };
for (const entry of shared.cases) {
  test(`shared eligibility: ${entry.name}`, async () => {
    const box = fixture({}, 200, sharedPolicy);
    const request = new Request(entry.url, { method: entry.method, headers: entry.headers });
    const response = await box.request(request);
    assert.equal(box.lookups.length, entry.eligible ? 1 : 0);
    assert.equal(box.writes.length, entry.eligible ? 1 : 0);
    if (entry.eligible) return;
    assert.equal(box.origin[0], request);
    assert.deepEqual(box.originOptions[0], { cf: { cacheTtlByStatus: { '100-599': -1 } } });
    assert.equal(response.headers.get('x-brine-cdn-cache'), null);
  });
}

for (const status of [206, 302, 404, 500]) {
  test(`HTTP ${status} never enters the shared cache`, async () => {
    const box = fixture({}, status);
    const response = await box.request();
    await box.request();
    assert.equal(response.status, status);
    assert.equal(box.origin.length, 2);
    assert.equal(box.writes.length, 0);
  });
}

for (const headers of [
  { 'Cache-Control': 'private' }, { 'Cache-Control': 'public, no-store' }, { 'Cache-Control': 'no-cache' },
  { 'Set-Cookie': 'session=fixture' }, { 'Content-Encoding': 'gzip' }, { Vary: '*' },
  { Vary: 'Cookie' }, { 'Content-Length': '-1' }, { 'Content-Length': 'NaN' }, { 'Content-Type': '' },
  { 'Content-Type': 'text/html; charset=utf-8' }, { 'Content-Type': 'application/xhtml+xml' },
]) {
  test(`uncacheable response ${JSON.stringify(headers)} stays out of shared cache`, async () => {
    const box = fixture(headers);
    const first = await box.request();
    await box.request();
    assert.equal(box.origin.length, 2);
    assert.equal(box.writes.length, 0);
    assert.equal(first.headers.get('x-brine-cdn-cache'), null);
  });
}
