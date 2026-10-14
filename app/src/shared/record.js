// Builds `web_usage` records (docs/spec/record.md 4.3) from a checked reader result
// (main/usage/reader.js). `id` and `v` are left to `sy outbox append`. Pure: no clock, no I/O.
// Owned by WP-B.

import { CLIENT_NAME } from './constants.js';

/** The six keys kept from an /api/organizations entry. */
export const ORG_KEYS = Object.freeze(['uuid', 'name', 'capabilities', 'rate_limit_tier', 'billing_type', 'raven_type']);
/** The four keys kept from /api/account. */
export const ACCOUNT_KEYS = Object.freeze(['uuid', 'email_address', 'full_name', 'display_name']);

/** A copy of `obj` with only `keys` that are present. */
export function pickKeys(obj, keys) {
  const out = {};
  if (!obj || typeof obj !== 'object') return out;
  for (const k of keys) if (k in obj) out[k] = obj[k];
  return out;
}

/** `client` of every record. */
export const clientOf = (version) => `${CLIENT_NAME}/${version}`;

/**
 * One record per org.
 * @param {{orgs: {org: object, usage: object}[], account: object, activeOrg: string|null, fetchMs?: number|null,
 *          tabId: string, machine: string, client: string, ts: string}} p
 */
export function buildRecords(p) {
  const account = pickKeys(p.account, ACCOUNT_KEYS);
  return p.orgs.map(({ org, usage }) => {
    const o = pickKeys(org, ORG_KEYS);
    const seat = { account: account.uuid, org: o.uuid, src: 'claude_ai' };
    if (account.email_address) seat.email = account.email_address;
    const payload = { tab_id: p.tabId, org: o, account, active: o.uuid === p.activeOrg, usage };
    if (Number.isInteger(p.fetchMs) && p.fetchMs >= 0) payload.fetch_ms = p.fetchMs;
    return { kind: 'web_usage', ts: p.ts, machine: p.machine, seat, client: p.client, payload };
  });
}

/**
 * One error record per known seat (a stopped read: 401, 403, 429). A seat is
 * `{account, org, email?, org_obj?, account_obj?, active?}`; none means no records.
 * @param {{seats: object[], status: number, message: string, tabId: string, machine: string, client: string, ts: string}} p
 */
export function buildErrorRecords(p) {
  return p.seats.map((s) => {
    const seat = { account: s.account, org: s.org, src: 'claude_ai' };
    if (s.email) seat.email = s.email;
    const org = { ...pickKeys(s.org_obj, ORG_KEYS), uuid: s.org };
    const account = { ...pickKeys(s.account_obj, ACCOUNT_KEYS), uuid: s.account };
    const payload = { tab_id: p.tabId, org, account, error: { status: p.status, message: p.message } };
    if (typeof s.active === 'boolean') payload.active = s.active;
    return { kind: 'web_usage', ts: p.ts, machine: p.machine, seat, client: p.client, payload };
  });
}

/** The seat facts of a successful read, kept to write error records later. */
export function seatsOf(orgs, account, activeOrg) {
  const a = pickKeys(account, ACCOUNT_KEYS);
  return orgs.map(({ org }) => ({
    account: a.uuid,
    org: org.uuid,
    email: a.email_address,
    org_obj: pickKeys(org, ORG_KEYS),
    account_obj: a,
    active: org.uuid === activeOrg,
  }));
}
