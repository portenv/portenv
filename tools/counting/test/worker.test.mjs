// SPDX-License-Identifier: Apache-2.0
//
// The counting Worker (PLAN.md 1.8, R-0010): aggregate counters only, one
// per day, field and value; never a row per check, never an IP or a header.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { increments } from '../worker/src/count.mjs';
import worker, { handle } from '../worker/src/index.mjs';

const APPCAST = 'https://github.com/portenv/portenv/releases/latest/download/appcast.xml';

// A D1 stand-in that records every statement and binding.
function fakeDB() {
  const db = {
    statements: [],
    counts: new Map(),
    prepare(sql) {
      return {
        bind(...args) {
          return { sql, args };
        },
      };
    },
    async batch(stmts) {
      for (const s of stmts) {
        db.statements.push(s);
        const key = s.args.slice(0, 3).join('|');
        db.counts.set(key, (db.counts.get(key) ?? 0) + 1);
      }
      return [];
    },
  };
  return db;
}

function ctx() {
  const waits = [];
  return { waits, waitUntil: (p) => waits.push(p), done: () => Promise.all(waits) };
}

const weekly = 'v=0.2.0&os=26&arch=arm64&c=2026-10&w=1&m=1&moved=1&agent=1&boxes=2-5&servers=1';

async function hit(query, { headers = {}, method = 'GET', db = fakeDB(), now } = {}) {
  const c = ctx();
  const req = new Request(`https://portenv.com/appcast.xml?${query}`, { method, headers });
  const env = { DB: db, APPCAST_URL: APPCAST };
  const res = now === undefined ? await worker.fetch(req, env, c) : await handle(req, env, c, now);
  await c.done();
  return { res, db };
}

test('a weekly check becomes one counter per field value, plus the cohort × week cross-tab', () => {
  const got = increments(new URLSearchParams(weekly));
  assert.deepEqual(got, [
    ['checks', '1'],
    ['v', '0.2.0'],
    ['os', '26'],
    ['arch', 'arm64'],
    ['c', '2026-10'],
    ['w', '1'],
    ['cw', '2026-10'],
    ['m', '1'],
    ['moved', '1'],
    ['agent', '1'],
    ['boxes', '2-5'],
    ['servers', '1'],
  ]);
});

test('a daily check counts the four base fields only', () => {
  assert.deepEqual(increments(new URLSearchParams('v=0.2.0&os=26&arch=arm64&c=2026-10')), [
    ['checks', '1'],
    ['v', '0.2.0'],
    ['os', '26'],
    ['arch', 'arm64'],
    ['c', '2026-10'],
  ]);
});

test('weekly-only fields count only on the weekly check', () => {
  const got = increments(new URLSearchParams('v=0.2.0&os=26&arch=arm64&c=2026-10&moved=1&boxes=6%2B'));
  assert.deepEqual(got.map(([f]) => f), ['checks', 'v', 'os', 'arch', 'c']);
});

test('a plain appcast fetch, or a malformed check, counts nothing', () => {
  for (const q of [
    '',
    'v=0.2.0&os=26&arch=arm64', // no install month
    'v=0.2.0&os=26.1&arch=arm64&c=2026-10', // not the major version only
    'v=0.2.0&os=26&arch=ppc&c=2026-10',
    'v=0.2.0&os=26&arch=arm64&c=2026-13',
    'v=<script>&os=26&arch=arm64&c=2026-10',
    'v=0.2.0&os=26&arch=arm64&c=2026-10&c=2026-11', // repeated field
  ]) {
    assert.deepEqual(increments(new URLSearchParams(q)), [], q);
  }
});

test('unknown parameters are never stored, and bad weekly values are dropped alone', () => {
  const got = increments(new URLSearchParams(`${weekly.replace('boxes=2-5', 'boxes=7')}&id=abc&email=x%40y.z`));
  const fields = got.map(([f]) => f);
  assert.ok(!fields.includes('boxes'));
  assert.ok(!fields.includes('id') && !fields.includes('email'));
  assert.ok(fields.includes('servers'));
});

test('the Worker stores counters only: one upsert per field value, keyed by day', async () => {
  const { res, db } = await hit(weekly, { now: Date.parse('2026-10-19T09:00:00Z') });
  assert.equal(res.status, 302);
  assert.equal(res.headers.get('Location'), APPCAST);
  assert.equal(db.statements.length, 12);
  for (const s of db.statements) {
    assert.match(s.sql, /^INSERT INTO counts \(day, field, value, n\) VALUES \(\?1, \?2, \?3, 1\) ON CONFLICT/);
    assert.equal(s.args.length, 3);
    assert.equal(s.args[0], '2026-10-19');
  }
  // Never a row combining fields: each binding is one field and one value.
  assert.ok(db.statements.every((s) => !String(s.args[2]).includes('&')));
});

test('no IP address or header reaches storage', async () => {
  const ip = '203.0.113.77';
  const { db } = await hit(weekly, {
    headers: {
      'CF-Connecting-IP': ip,
      'X-Forwarded-For': ip,
      'User-Agent': 'Portenv',
      'Accept-Language': 'fr-FR',
      'CF-IPCountry': 'FR',
    },
  });
  const stored = JSON.stringify(db.statements);
  for (const s of [ip, 'Portenv', 'fr-FR', 'FR"']) assert.ok(!stored.includes(s), s);
});

test('counts add up across checks', async () => {
  const db = fakeDB();
  const now = Date.parse('2026-10-19T09:00:00Z');
  await hit(weekly, { db, now });
  await hit('v=0.2.0&os=26&arch=arm64&c=2026-09', { db, now });
  assert.equal(db.counts.get('2026-10-19|checks|1'), 2);
  assert.equal(db.counts.get('2026-10-19|c|2026-10'), 1);
  assert.equal(db.counts.get('2026-10-19|c|2026-09'), 1);
  assert.equal(db.counts.get('2026-10-19|w|1'), 1);
});

test('a plain fetch redirects to the appcast and writes nothing', async () => {
  const { res, db } = await hit('');
  assert.equal(res.status, 302);
  assert.equal(res.headers.get('Location'), APPCAST);
  assert.equal(res.headers.get('Cache-Control'), 'no-store');
  assert.equal(db.statements.length, 0);
});

test('only GET and HEAD', async () => {
  const { res, db } = await hit(weekly, { method: 'POST' });
  assert.equal(res.status, 405);
  assert.equal(db.statements.length, 0);
});

test('storage failing never fails the redirect', async () => {
  const db = fakeDB();
  db.batch = async () => {
    throw new Error('D1 down');
  };
  const { res } = await hit(weekly, { db });
  assert.equal(res.status, 302);
});

test('wrangler.toml keeps logs, Logpush and extra outputs off, on the appcast route only', async () => {
  const { readFile } = await import('node:fs/promises');
  const toml = await readFile(new URL('../worker/wrangler.toml', import.meta.url), 'utf8');
  const lines = toml.split('\n').filter((l) => !l.trim().startsWith('#'));
  const conf = lines.join('\n');
  assert.match(conf, /^logpush = false$/m);
  assert.match(conf, /\[observability\]\nenabled = false/);
  assert.match(conf, /\[observability\.logs\]\nenabled = false\ninvocation_logs = false/);
  assert.match(conf, /^workers_dev = false$/m);
  for (const banned of ['tail_consumers', 'analytics_engine', 'kv_namespaces', 'r2_buckets', 'queues', 'account_id']) {
    assert.ok(!conf.includes(banned), banned);
  }
  const routes = [...conf.matchAll(/pattern = "([^"]+)"/g)].map((m) => m[1]);
  assert.deepEqual(routes, ['portenv.com/appcast.xml']);
});

test('the table holds counters only: day, field, value, n', async () => {
  const { readFile } = await import('node:fs/promises');
  const sql = await readFile(new URL('../worker/schema.sql', import.meta.url), 'utf8');
  const body = sql.split('\n').filter((l) => !l.trim().startsWith('--')).join('\n');
  assert.equal([...body.matchAll(/CREATE TABLE/g)].length, 1);
  const columns = [...body.matchAll(/^\s+(\w+)\s+(TEXT|INTEGER)/gm)].map((m) => m[1]);
  assert.deepEqual(columns, ['day', 'field', 'value', 'n']);
});
