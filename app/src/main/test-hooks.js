// Test-only overrides (docs/spec/desktop.md C2). boot.js imports this file only when
// !app.isPackaged, and electron-builder.yml leaves it out of the asar, so a packaged app
// cannot be steered by these variables. Each function reads one variable from the env it is
// given and returns a checked value, or null when it is unset or not acceptable.

import { TEST_ENV } from '../shared/constants.js';
import { testOrigin } from '../shared/urls.js';

const isAbsolute = (p) => typeof p === 'string' && (p.startsWith('/') || /^[A-Za-z]:[\\/]/.test(p) || p.startsWith('\\\\'));

/** SY_APP_USER_DATA: an absolute folder for user data (paths.js checks it further). */
export function userData(env) {
  const v = env?.[TEST_ENV.USER_DATA];
  if (!v) return null;
  if (!isAbsolute(v)) throw new Error(`${TEST_ENV.USER_DATA} must be an absolute path`);
  return v;
}

/** SY_APP_TEST_CLAUDE_ORIGIN: a loopback http(s) origin that stands in for https://claude.ai. */
export function claudeOrigin(env) {
  const v = env?.[TEST_ENV.CLAUDE_ORIGIN];
  if (!v) return null;
  const o = testOrigin(v);
  if (!o) throw new Error(`${TEST_ENV.CLAUDE_ORIGIN} must be an http(s) URL on a loopback host`);
  return o;
}

/** SY_APP_TEST_CADENCE_MS: replaces the 300 s, 60 s and 10 s cadences (an integer, 100 ms to 1 h). */
export function cadenceMs(env) {
  const v = env?.[TEST_ENV.CADENCE_MS];
  if (!v) return null;
  const n = Number(v);
  if (!Number.isSafeInteger(n) || n < 100 || n > 3_600_000) throw new Error(`${TEST_ENV.CADENCE_MS} must be an integer between 100 and 3600000`);
  return n;
}

/** SY_APP_TEST_EXPOSE=1: boot.js exposes its modules to the smoke test (main process only). */
export function expose(env) {
  return env?.[TEST_ENV.EXPOSE] === '1';
}

/** SY_BIN: an absolute path to the sy binary to run instead of the resolved one. */
export function syBin(env) {
  const v = env?.[TEST_ENV.SY_BIN];
  if (!v) return null;
  if (!isAbsolute(v)) throw new Error(`${TEST_ENV.SY_BIN} must be an absolute path`);
  return v;
}
