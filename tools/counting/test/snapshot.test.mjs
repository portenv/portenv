// SPDX-License-Identifier: Apache-2.0
//
// The owner's daily snapshot (PLAN.md 1.8): downloads, daily, weekly and
// monthly actives, and install-month cohorts, from the counters; cells
// under 10 hidden in anything shown outside the owner (R-0010 point 2).

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { cohorts, daily, hideSmall, isoWeek, parseCounts, parseDownloads, toCSV, toHTML } from '../snapshot.mjs';

const rows = [
  // Monday 12 Oct (W42): 12 checks, 12 first of the week, 3 first of the month.
  { day: '2026-10-12', field: 'checks', value: '1', n: 12 },
  { day: '2026-10-12', field: 'w', value: '1', n: 12 },
  { day: '2026-10-12', field: 'm', value: '1', n: 3 },
  { day: '2026-10-12', field: 'cw', value: '2026-09', n: 8 },
  { day: '2026-10-12', field: 'cw', value: '2026-10', n: 4 },
  // Tuesday 13 Oct: 15 checks, 5 more first-of-week.
  { day: '2026-10-13', field: 'checks', value: '1', n: 15 },
  { day: '2026-10-13', field: 'w', value: '1', n: 5 },
  { day: '2026-10-13', field: 'cw', value: '2026-10', n: 5 },
  // Monday 19 Oct (W43).
  { day: '2026-10-19', field: 'checks', value: '1', n: 20 },
  { day: '2026-10-19', field: 'w', value: '1', n: 20 },
  { day: '2026-10-19', field: 'm', value: '1', n: 1 },
  { day: '2026-10-19', field: 'cw', value: '2026-09', n: 6 },
  { day: '2026-10-19', field: 'cw', value: '2026-10', n: 14 },
];

test('ISO weeks', () => {
  assert.equal(isoWeek('2026-10-12'), '2026-W42');
  assert.equal(isoWeek('2026-10-18'), '2026-W42');
  assert.equal(isoWeek('2026-10-19'), '2026-W43');
  assert.equal(isoWeek('2027-01-01'), '2026-W53');
  assert.equal(isoWeek('2024-12-30'), '2025-W01');
});

test('wrangler d1 --json output, either shape', () => {
  const one = [{ results: rows.slice(0, 2), success: true, meta: {} }];
  assert.deepEqual(parseCounts(JSON.stringify(one)), rows.slice(0, 2));
  assert.deepEqual(parseCounts(JSON.stringify({ results: rows.slice(0, 1) })), rows.slice(0, 1));
});

test('downloads.csv totals per day', () => {
  const csv = 'date,tag,asset,downloads\n2026-10-12,v0.2.0,Portenv.dmg,40\n2026-10-12,v0.2.0,portenv-cli.tar.gz,2\n2026-10-13,v0.2.0,Portenv.dmg,45\n';
  assert.deepEqual(parseDownloads(csv), new Map([['2026-10-12', 42], ['2026-10-13', 45]]));
});

test('daily actives, week to date and month to date', () => {
  const dl = new Map([['2026-10-12', 42], ['2026-10-13', 45]]);
  const t = daily(rows, dl);
  assert.deepEqual(t.columns, ['date', 'downloads_total', 'downloads_new', 'dau', 'wau_week_to_date', 'mau_month_to_date']);
  assert.deepEqual(t.rows, [
    ['2026-10-12', 42, '', 12, 12, 3],
    ['2026-10-13', 45, 3, 15, 17, 3],
    ['2026-10-19', '', '', 20, 20, 4],
  ]);
});

test('install-month cohorts: actives per install month per week', () => {
  const t = cohorts(rows);
  assert.deepEqual(t.columns, ['install_month', '2026-W42', '2026-W43']);
  assert.deepEqual(t.rows, [
    ['2026-09', 8, 6],
    ['2026-10', 9, 14],
  ]);
});

test('cells from 1 to 9 are hidden for anything shown outside the owner; labels never', () => {
  const t = hideSmall(cohorts(rows));
  assert.deepEqual(t.rows, [
    ['2026-09', '<10', '<10'],
    ['2026-10', '<10', 14],
  ]);
  const d = hideSmall(daily(rows, new Map([['2026-10-12', 42], ['2026-10-13', 45]])));
  assert.deepEqual(d.rows[0], ['2026-10-12', 42, '', 12, 12, '<10']);
  assert.deepEqual(d.rows[1][2], '<10');
  // 0 singles nobody out and stays.
  assert.deepEqual(hideSmall({ columns: ['a', 'b'], rows: [['x', 0]] }).rows, [['x', 0]]);
});

test('CSV and a self-contained page', () => {
  const t = { columns: ['date', 'dau'], rows: [['2026-10-12', 12]] };
  assert.equal(toCSV(t), 'date,dau\n2026-10-12,12\n');
  const html = toHTML('Portenv counts', [['Daily', t]], 'owner only');
  assert.match(html, /<td>2026-10-12<\/td><td>12<\/td>/);
  assert.ok(!/<script|https?:\/\//.test(html), 'nothing loaded from anywhere');
  assert.ok(html.includes('owner only'));
});
