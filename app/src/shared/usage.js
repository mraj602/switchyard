// Local reading of a claude.ai usage response, for the strip under an account tab
// (docs/spec/desktop.md C5, C8) and the URL rules the tab and usage modules share.
// Ported from the prototype's planOf / windowsOf (golden-tested against its output). The hub's
// numbers win when it knows the seat; this is the fallback, marked "local". Owned by WP-B.

import { NOT_READABLE_PATHS } from './constants.js';

/** A plan name from an /api/organizations entry. */
export function planOf(org) {
  const caps = org.capabilities || [];
  const tier = String(org.rate_limit_tier || '');
  if (caps.includes('raven') || org.raven_type) return 'Team';
  if (caps.includes('enterprise')) return 'Enterprise';
  if (/max_20x/.test(tier)) return 'Max 20x';
  if (/max/.test(tier) || caps.includes('claude_max')) return 'Max 5x';
  if (caps.includes('claude_pro')) return 'Pro';
  return caps.includes('chat') ? 'Free' : tier || 'Unknown';
}

// Friendly names for the windows we recognise; anything else the API reports is shown under
// its raw key (e.g. a monthly dollar budget) rather than hidden.
const LABELS = {
  five_hour: '5-hour',
  seven_day: '7-day',
  seven_day_opus: '7-day · Opus',
  seven_day_sonnet: '7-day · Sonnet',
  seven_day_oauth_apps: '7-day · apps',
  seven_day_cowork: '7-day · Cowork',
  iguana_necktie: 'Cloud session credits',
};
const ORDER = Object.keys(LABELS);

/**
 * Every window of a usage response: each `utilization` key (unknown keys under their raw
 * name), then model-scoped weeks taken from `limits`.
 * @param {object} u the raw usage response
 */
export function windowsOf(u) {
  const windows = [];
  for (const [key, v] of Object.entries(u)) {
    if (!v || typeof v !== 'object' || Array.isArray(v) || !('utilization' in v) || v.utilization == null) continue;
    windows.push({
      key,
      label: LABELS[key] || key,
      pct: Math.round(v.utilization * 10) / 10,
      resetsAt: v.resets_at,
      usedDollars: v.used_dollars ?? null,
      limitDollars: v.limit_dollars ?? null,
      locked: v.locked_reason || null,
    });
  }
  windows.sort((a, b) => (ORDER.indexOf(a.key) + 1 || 99) - (ORDER.indexOf(b.key) + 1 || 99));
  // Model-scoped weekly limits (for example Fable) only appear in `limits`.
  for (const l of Array.isArray(u.limits) ? u.limits : []) {
    const model = l.scope && l.scope.model && l.scope.model.display_name;
    if (!model) continue;
    const key = `${l.group || l.kind}_${model}`.toLowerCase();
    windows.push({
      key,
      label: `${l.group === 'weekly' ? '7-day' : l.group || l.kind} · ${model}`,
      pct: Math.round(l.percent || 0),
      resetsAt: l.resets_at,
      inactive: l.is_active === false,
    });
  }
  return windows;
}

/**
 * windowsOf shaped as the shell's StripWindow[] (shared/ipc.js): inactive scoped weeks are
 * left out of the strip.
 * @returns {{key: string, label: string|null, used_pct: number|null, resets_at: string|null}[]}
 */
export function stripWindows(u) {
  return windowsOf(u)
    .filter((w) => !w.inactive)
    .map((w) => ({ key: w.key, label: w.label, used_pct: w.pct ?? null, resets_at: w.resetsAt ?? null }));
}

/**
 * Pick the org the strip shows: the active one when it reports windows, else the one
 * reporting the most (as the prototype did).
 * @param {{org: {uuid: string}, usage: object}[]} orgs
 * @param {string|null} activeOrg
 * @returns {{org: object, usage: object, windows: object[]}|null}
 */
export function pickOrg(orgs, activeOrg) {
  const scored = orgs
    .map((o) => ({ ...o, windows: o.usage && !o.usage.__status ? stripWindows(o.usage) : null }))
    .filter((x) => x.windows);
  return scored.find((x) => x.org.uuid === activeOrg && x.windows.length) || scored.sort((a, b) => b.windows.length - a.windows.length)[0] || null;
}

// ---- URLs (claude.ai only; the navigation policy itself is shared/urls.js, WP-A)

/** True for https://claude.ai[/…], any *.claude.ai host, or `origin` (the test fake's origin). */
export function isClaudeUrl(url, origin = 'https://claude.ai') {
  let u;
  try {
    u = new URL(url);
  } catch {
    return false;
  }
  if (u.origin === origin) return true;
  if (u.protocol !== 'https:') return false;
  return u.hostname === 'claude.ai' || u.hostname.endsWith('.claude.ai');
}

/** A claude.ai URL whose path is not /login or /logout (C5 "readable" URL rule). */
export function isReadableUrl(url, origin = 'https://claude.ai') {
  if (!isClaudeUrl(url, origin)) return false;
  const p = new URL(url).pathname.replace(/\/+$/, '');
  return !NOT_READABLE_PATHS.some((bad) => p === bad || p.startsWith(`${bad}/`));
}
