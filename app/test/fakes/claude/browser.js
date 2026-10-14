// A stand-in for a signed-in claude.ai tab, for unit tests on plain Node: it evaluates the
// reader's script (main/usage/reader.js) in a `vm` context whose `fetch` is Node's fetch aimed
// at the fake server with the tab's cookies, like `credentials: 'include'` in the real page.
// Use it as `fakeElectron({executeJavaScript: browser.exec})`.

import vm from 'node:vm';

/**
 * @param {{origin: string, cookies?: Record<string, string>}} opts
 */
export function fakeBrowser(opts) {
  const cookies = { ...(opts.cookies ?? {}) };
  const calls = [];
  const exec = async (_wc, code, world) => {
    calls.push({ world: world ?? 'main' });
    const cookie = Object.entries(cookies)
      .map(([k, v]) => `${k}=${encodeURIComponent(v)}`)
      .join('; ');
    const ctx = vm.createContext({
      fetch: (u, init = {}) => fetch(new URL(u, opts.origin), { ...init, headers: { ...(init.headers ?? {}), cookie } }),
      Date,
      JSON,
      Array,
      Object,
      Promise,
    });
    // Structured-clone the result, as Electron does, so it is a main-realm object.
    return JSON.parse(JSON.stringify(await vm.runInContext(code, ctx)));
  };
  return { exec, cookies, calls };
}

/** Sign in on the fake: POST /login, return the cookies it set (sessionKey, lastActiveOrg). */
export async function signInFake(origin) {
  const r = await fetch(`${origin}/login`, { method: 'POST', redirect: 'manual', body: 'email=a%40example.com', headers: { 'content-type': 'application/x-www-form-urlencoded' } });
  const out = {};
  for (const c of r.headers.getSetCookie()) {
    const [kv] = c.split(';');
    const i = kv.indexOf('=');
    out[kv.slice(0, i)] = decodeURIComponent(kv.slice(i + 1));
  }
  return out;
}
