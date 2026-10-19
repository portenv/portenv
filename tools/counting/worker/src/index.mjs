// SPDX-License-Identifier: Apache-2.0
//
// portenv.com/appcast.xml: counts the update check's published fields
// (PLAN.md 1.8) into per-day counters, then redirects to the appcast on the
// GitHub release. It reads only the query string: never the IP address,
// never a header. It logs nothing (Workers Logs and Logpush are off in
// wrangler.toml).

import { dayOf, increments } from './count.mjs';

const UPSERT =
  'INSERT INTO counts (day, field, value, n) VALUES (?1, ?2, ?3, 1) ON CONFLICT (day, field, value) DO UPDATE SET n = n + 1';

export async function handle(request, env, ctx, now = Date.now()) {
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    return new Response(null, { status: 405, headers: { Allow: 'GET, HEAD' } });
  }
  const pairs = increments(new URL(request.url).searchParams);
  if (pairs.length > 0) {
    const day = dayOf(now);
    const stmts = pairs.map(([field, value]) => env.DB.prepare(UPSERT).bind(day, field, value));
    // After the answer: a slow or failing database never delays or fails
    // the update check, and a failure is dropped, never logged.
    ctx.waitUntil(env.DB.batch(stmts).catch(() => {}));
  }
  return new Response(null, {
    status: 302,
    headers: { Location: env.APPCAST_URL, 'Cache-Control': 'no-store' },
  });
}

export default {
  fetch(request, env, ctx) {
    return handle(request, env, ctx);
  },
};
