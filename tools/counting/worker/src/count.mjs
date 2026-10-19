// SPDX-License-Identifier: Apache-2.0
//
// The published fields of the update check (PLAN.md 1.8) and what each
// check adds to the counters. Nothing else in a request is ever read.

// The base fields, on every counted check.
const BASE = {
  v: /^\d{1,3}\.\d{1,3}\.\d{1,3}$/, // Portenv's version
  os: /^\d{2,3}$/, // macOS major version only (CNIL: minimised)
  arch: /^(arm64|x86_64)$/,
  c: /^20\d\d-(0[1-9]|1[0-2])$/, // install month
};

// On the weekly check (w=1) only.
const WEEKLY = {
  moved: /^1$/,
  agent: /^1$/,
  boxes: /^(1|2-5|6\+)$/,
  servers: /^(0|1|2-3|4\+)$/,
};

// One value of a field, or null when absent, repeated or malformed.
function one(params, name, pattern) {
  const all = params.getAll(name);
  if (all.length !== 1) return null;
  return pattern.test(all[0]) ? all[0] : null;
}

// The counters one check adds to, as [field, value] pairs. A request
// without the four base fields (a plain appcast fetch, from an app that
// doesn't count) adds nothing. Each pair is counted on its own: no row ever
// combines fields (R-0010 point 1), except the one cross-tab the snapshot
// needs, install month × week active (`cw`).
export function increments(params) {
  const base = Object.entries(BASE).map(([name, re]) => [name, one(params, name, re)]);
  if (base.some(([, value]) => value === null)) return [];
  const out = [['checks', '1'], ...base];
  const c = base.find(([name]) => name === 'c')[1];
  const weekly = one(params, 'w', /^1$/) !== null;
  if (weekly) out.push(['w', '1'], ['cw', c]);
  if (one(params, 'm', /^1$/) !== null) out.push(['m', '1']);
  if (weekly) {
    for (const [name, re] of Object.entries(WEEKLY)) {
      const value = one(params, name, re);
      if (value !== null) out.push([name, value]);
    }
  }
  return out;
}

// The UTC day a check is counted on.
export function dayOf(ms) {
  return new Date(ms).toISOString().slice(0, 10);
}
