// The environment `wy` runs with (C11): the app's own, minus anything that steers Node or
// Electron or is private to the app (`ELECTRON_*`, `NODE_*` which includes NODE_OPTIONS,
// `WY_APP_*`). HOME, PATH, `WY_*` (WY_BIN, WY_DATA …), the XDG and the Windows profile
// variables stay, as does everything else not on the drop list (SSH_AUTH_SOCK and the session
// bus matter for the admin channel and the hub service).

const DROP = [/^ELECTRON_/, /^NODE_/, /^WY_APP_/];

/**
 * @param {Record<string, string|undefined>} env
 * @param {string} platform
 * @returns {Record<string, string>}
 */
export function cleanEnv(env, platform = process.platform) {
  const out = {};
  for (const [k, v] of Object.entries(env ?? {})) {
    if (typeof v !== 'string') continue;
    const key = platform === 'win32' ? k.toUpperCase() : k;
    if (DROP.some((re) => re.test(key))) continue;
    out[k] = v;
  }
  return out;
}
