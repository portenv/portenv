#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
//
// The owner's daily snapshot (PLAN.md 1.8): downloads, daily, weekly and
// monthly actives, and install-month cohorts, from the counting Worker's
// counters and the download archive. It writes CSV files and one
// self-contained page to open locally; nothing is published.
//
//   node tools/counting/snapshot.mjs --counts counts.json --metrics DIR --out DIR [--share]
//
// --counts: `wrangler d1 execute portenv-counting --remote --json --command
//           "SELECT day, field, value, n FROM counts"` (scripts/counting-snapshot.sh)
// --metrics: the download archive's folder (scripts/archive-downloads.sh)
// --share:  for anything shown outside the owner: cells from 1 to 9 become
//           "<10" (R-0010 point 2).

import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

// yyyy-mm-dd -> yyyy-Www (ISO 8601).
export function isoWeek(day) {
  const d = new Date(`${day}T00:00:00Z`);
  const dow = (d.getUTCDay() + 6) % 7; // Monday 0
  d.setUTCDate(d.getUTCDate() - dow + 3); // the week's Thursday
  const year = d.getUTCFullYear();
  const week = Math.ceil(((d - Date.UTC(year, 0, 1)) / 86400000 + 1) / 7);
  return `${year}-W${String(week).padStart(2, '0')}`;
}

export function parseCounts(text) {
  const json = JSON.parse(text);
  const blocks = Array.isArray(json) ? json : [json];
  return blocks.flatMap((b) => b.results ?? []).map((r) => ({ ...r, n: Number(r.n) }));
}

// downloads.csv (date,tag,asset,downloads) -> date -> total of every asset.
export function parseDownloads(text) {
  const totals = new Map();
  for (const line of text.trim().split('\n').slice(1)) {
    const cells = line.split(',');
    const [date, n] = [cells[0], Number(cells[cells.length - 1])];
    if (!date || Number.isNaN(n)) continue;
    totals.set(date, (totals.get(date) ?? 0) + n);
  }
  return totals;
}

function sum(rows, field, pick) {
  return rows.filter((r) => r.field === field && pick(r)).reduce((a, r) => a + r.n, 0);
}

export function daily(rows, downloads = new Map()) {
  const days = [...new Set([...rows.map((r) => r.day), ...downloads.keys()])].sort();
  const out = [];
  let previous;
  for (const day of days) {
    const total = downloads.get(day);
    const fresh = total !== undefined && previous !== undefined ? total - previous : '';
    if (total !== undefined) previous = total;
    const week = isoWeek(day);
    const month = day.slice(0, 7);
    out.push([
      day,
      total ?? '',
      fresh,
      sum(rows, 'checks', (r) => r.day === day),
      sum(rows, 'w', (r) => isoWeek(r.day) === week && r.day <= day),
      sum(rows, 'm', (r) => r.day.slice(0, 7) === month && r.day <= day),
    ]);
  }
  return { columns: ['date', 'downloads_total', 'downloads_new', 'dau', 'wau_week_to_date', 'mau_month_to_date'], rows: out };
}

export function cohorts(rows) {
  const cw = rows.filter((r) => r.field === 'cw');
  const weeks = [...new Set(cw.map((r) => isoWeek(r.day)))].sort();
  const months = [...new Set(cw.map((r) => r.value))].sort();
  return {
    columns: ['install_month', ...weeks],
    rows: months.map((m) => [m, ...weeks.map((w) => sum(cw, 'cw', (r) => r.value === m && isoWeek(r.day) === w))]),
  };
}

// The first column is a label; every other number from 1 to 9 is hidden.
export function hideSmall(table) {
  return {
    columns: table.columns,
    rows: table.rows.map(([label, ...cells]) => [label, ...cells.map((v) => (typeof v === 'number' && v > 0 && v < 10 ? '<10' : v))]),
  };
}

export function toCSV(t) {
  return [t.columns, ...t.rows].map((r) => r.join(',')).join('\n') + '\n';
}

const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]);

// One page, no scripts and nothing loaded from anywhere.
export function toHTML(title, sections, note) {
  const tables = sections
    .map(([name, t]) => {
      const head = `<tr>${t.columns.map((c) => `<th>${esc(c)}</th>`).join('')}</tr>`;
      const body = t.rows.map((r) => `<tr>${r.map((c) => `<td>${esc(c)}</td>`).join('')}</tr>`).join('\n');
      return `<h2>${esc(name)}</h2>\n<table>\n${head}\n${body}\n</table>`;
    })
    .join('\n');
  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>${esc(title)}</title>
<style>
:root { color-scheme: light dark; --fg: #1d1d1f; --bg: #fff; --line: #d2d2d7; }
@media (prefers-color-scheme: dark) { :root { --fg: #f5f5f7; --bg: #1d1d1f; --line: #424245; } }
body { font: 14px/1.4 -apple-system, system-ui, sans-serif; color: var(--fg); background: var(--bg); margin: 24px 16px; }
table { border-collapse: collapse; margin-bottom: 24px; font-variant-numeric: tabular-nums; }
th, td { border-bottom: 1px solid var(--line); padding: 4px 10px; text-align: right; }
th:first-child, td:first-child { text-align: left; }
.note { opacity: .7; }
</style></head><body>
<h1>${esc(title)}</h1>
<p class="note">${esc(note)}</p>
${tables}
</body></html>
`;
}

async function main(argv) {
  const args = {};
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--share') args.share = true;
    else if (argv[i].startsWith('--')) args[argv[i].slice(2)] = argv[++i];
  }
  if (!args.counts || !args.out) {
    console.error('usage: snapshot.mjs --counts counts.json [--metrics DIR] --out DIR [--share]');
    process.exit(2);
  }
  const rows = parseCounts(await readFile(args.counts, 'utf8'));
  let downloads = new Map();
  if (args.metrics) {
    try {
      downloads = parseDownloads(await readFile(join(args.metrics, 'downloads.csv'), 'utf8'));
    } catch (e) {
      if (e.code !== 'ENOENT') throw e;
    }
  }
  let d = daily(rows, downloads);
  let c = cohorts(rows);
  if (args.share) [d, c] = [hideSmall(d), hideSmall(c)];
  const stamp = new Date().toISOString().slice(0, 10);
  const note = args.share
    ? `Made ${stamp}. Cells from 1 to 9 are shown as <10.`
    : `Made ${stamp}. Owner only: small cells are not hidden; use --share for anything shown to others.`;
  await mkdir(args.out, { recursive: true });
  await writeFile(join(args.out, 'daily.csv'), toCSV(d));
  await writeFile(join(args.out, 'cohorts.csv'), toCSV(c));
  await writeFile(join(args.out, 'snapshot.html'), toHTML('Portenv counts', [['Daily', d], ['Install-month cohorts (actives per week)', c]], note));
  console.log(join(args.out, 'snapshot.html'));
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  await main(process.argv.slice(2));
}
