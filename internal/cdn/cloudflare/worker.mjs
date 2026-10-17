const POLICY = /*BRINE_POLICY*/null;

function eligible(request, policy) {
  const url = new URL(request.url);
  if (!['GET', 'HEAD'].includes(request.method) || url.protocol !== 'https:' || url.host !== policy.hostname) return false;
  if (url.search || request.url.includes('?')) return false;
  if (['authorization', 'cookie', 'range'].some(name => request.headers.has(name))) return false;
  if (!url.pathname.startsWith(policy.prefix)) return false;
  const name = url.pathname.slice(policy.prefix.length);
  const match = /^[a-f0-9]{64}\.([a-z0-9]{1,16})$/.exec(name);
  return Boolean(match && policy.extensions.includes(match[1]));
}

function lookupRequest(request) {
  const headers = new Headers(request.headers);
  headers.delete('cache-control');
  headers.delete('pragma');
  return new Request(request.url, { method: 'GET', headers });
}

function cacheable(response) {
  if (response.status !== 200 || response.headers.has('set-cookie') || response.headers.has('content-range')) return false;
  const encoding = response.headers.get('content-encoding');
  if (encoding && encoding.toLowerCase() !== 'identity') return false;
  const vary = response.headers.get('vary');
  if (vary && vary.split(',').some(name => name.trim().toLowerCase() !== 'accept-encoding')) return false;
  const directives = (response.headers.get('cache-control') || '').toLowerCase().split(',');
  if (directives.some(value => ['private', 'no-store', 'no-cache'].includes(value.trim().split('=')[0]))) return false;
  const length = response.headers.get('content-length');
  if (!length || !/^\d+$/.test(length) || !Number.isSafeInteger(Number(length))) return false;
  const type = (response.headers.get('content-type') || '').split(';')[0].trim().toLowerCase();
  return Boolean(type && type !== 'text/html' && type !== 'application/xhtml+xml');
}

function forMethod(response, method) {
  return method === 'HEAD' ? new Response(null, response) : response;
}

function marked(response, result, method) {
  const headers = new Headers(response.headers);
  headers.set('X-Brine-CDN-Cache', result);
  return new Response(method === 'HEAD' ? null : response.body, { status: response.status, statusText: response.statusText, headers });
}

async function match(cache, key) {
  try { return await cache.match(key); }
  catch { return undefined; }
}

const originFetchOptions = { cf: { cacheTtlByStatus: { '100-599': -1 } } };

export function createHandler(policy, runtime = {
  fetch: (request, options) => fetch(request, options),
  cache: () => caches.open(policy.namespace),
}) {
  return async function handle(request, ctx) {
    if (!eligible(request, policy)) return runtime.fetch(request, originFetchOptions);
    return cached(request, ctx, policy, runtime);
  };
}

async function cached(request, ctx, policy, runtime) {
  const originRequest = new Request(request, { method: 'GET', redirect: 'manual' });
  const key = lookupRequest(request);
  let cache;
  try { cache = await runtime.cache(); }
  catch { return forMethod(await runtime.fetch(originRequest, originFetchOptions), request.method); }
  const hit = await match(cache, key);
  if (hit) return marked(hit, 'HIT', request.method);
  const response = await runtime.fetch(originRequest, originFetchOptions);
  if (!cacheable(response)) return forMethod(response, request.method);
  const headers = new Headers(response.headers);
  headers.set('Cache-Control', `public, max-age=${policy.ttlSeconds}, immutable`);
  const stored = new Response(response.body, { status: 200, headers });
  ctx.waitUntil(cache.put(key, stored.clone()).catch(() => {
    console.warn(JSON.stringify({ event: 'cdn_cache_store_failed' }));
  }));
  return marked(stored, 'MISS', request.method);
}

export default {
  fetch(request, env, ctx) { return createHandler(POLICY)(request, ctx); },
};
