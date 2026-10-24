import { claims, fakeFetch, manualClock, response } from '../../../test/hubfake';
import type { AppInfo } from '../../contract/platform';
import { claim, ClaimError, claimMessage, hostOf, isTailscaleHost, pairInputMessage, parsePairInput } from './index';
import type { PairInputError } from './index';

const S = 'Q2xhdWRlIGlzIG5vdCBoZXJl'.slice(0, 22); // 22 base64url characters
const app: AppInfo = { version: '0.8.0', build: '12', model: 'Pixel 9', platform: 'android', dev: false };

const prod = { dev: false };
const dev = { dev: true };

describe('parsePairInput', () => {
  it('accepts the QR of pairing.md 2.2', () => {
    expect(parsePairInput(`https://hub.example.ts.net/#/pair?s=${S}&c=482913`, prod)).toEqual({
      ok: true,
      input: { origin: 'https://hub.example.ts.net', secret: S, code: '482913' },
    });
    expect(parsePairInput(`https://Hub.Example.ts.net:8443/#/pair?s=${S}`, prod)).toEqual({
      ok: true,
      input: { origin: 'https://hub.example.ts.net:8443', secret: S },
    });
  });

  it('drops an explicit default port, so later same-origin checks match', () => {
    expect(parsePairInput(`https://hub.example.ts.net:443/#/pair?s=${S}`, prod)).toEqual({ ok: true, input: { origin: 'https://hub.example.ts.net', secret: S } });
    expect(parsePairInput(`watchyard://pair?u=${encodeURIComponent('https://hub.example.ts.net:443')}&s=${S}`, prod)).toEqual({
      ok: true,
      input: { origin: 'https://hub.example.ts.net', secret: S },
    });
    expect(parsePairInput('hub.example.ts.net:0443 482913', prod)).toEqual({ ok: true, input: { origin: 'https://hub.example.ts.net', code: '482913' } });
    expect(parsePairInput('http://192.168.1.5:80/#/pair?c=482913', dev)).toEqual({ ok: true, input: { origin: 'http://192.168.1.5', code: '482913' } });
    expect(parsePairInput('http://192.168.1.5:443/#/pair?c=482913', dev)).toEqual({ ok: true, input: { origin: 'http://192.168.1.5:443', code: '482913' } });
    expect(parsePairInput('http://100.101.102.103:8787/#/pair?c=482913', dev)).toEqual({ ok: true, input: { origin: 'http://100.101.102.103:8787', code: '482913' } });
  });

  it('accepts the code alone in the QR fragment', () => {
    expect(parsePairInput('https://hub.example.ts.net/#/pair?c=482-913', prod)).toEqual({
      ok: true,
      input: { origin: 'https://hub.example.ts.net', code: '482913' },
    });
  });

  it('accepts the watchyard:// link', () => {
    const u = encodeURIComponent('https://hub.example.ts.net:8443');
    expect(parsePairInput(`watchyard://pair?u=${u}&s=${S}&c=482913`, prod)).toEqual({
      ok: true,
      input: { origin: 'https://hub.example.ts.net:8443', secret: S, code: '482913' },
    });
  });

  it.each([
    ['hub.example.ts.net 482-913'],
    ['hub.example.ts.net 482913'],
    ['hub.example.ts.net 482 913'],
    ['https://hub.example.ts.net/ 482-913'],
    ['  hub.example.ts.net   482-913  '],
  ])('accepts typed %j', (text) => {
    expect(parsePairInput(text, prod)).toEqual({ ok: true, input: { origin: 'https://hub.example.ts.net', code: '482913' } });
  });

  it('accepts a typed address with the code in its own field', () => {
    expect(parsePairInput('482 913', { dev: false, address: 'hub.example.ts.net:8443' })).toEqual({
      ok: true,
      input: { origin: 'https://hub.example.ts.net:8443', code: '482913' },
    });
  });

  it('keeps no path, query, fragment or user info', () => {
    const r = parsePairInput('https://hub.example.ts.net/deep/path?x=1 482913', prod);
    expect(r).toEqual({ ok: true, input: { origin: 'https://hub.example.ts.net', code: '482913' } });
  });

  const refused: [string, string, { dev: boolean; address?: string }, PairInputError][] = [
    ['empty', '   ', prod, 'empty'],
    ['http in a release build', `http://hub.example.ts.net/#/pair?s=${S}`, prod, 'not_https'],
    ['http public host even in dev', `http://hub.example.ts.net/#/pair?s=${S}`, dev, 'not_https'],
    ['http 8.8.8.8 in dev', 'http://8.8.8.8 482913', dev, 'not_https'],
    ['http 172.32.0.1 (not RFC 1918) in dev', 'http://172.32.0.1 482913', dev, 'not_https'],
    ['http 100.128.0.1 (outside Tailscale 100.64/10) in dev', 'http://100.128.0.1 482913', dev, 'not_https'],
    ['http Tailscale address in a release build', 'http://100.101.102.103:8787 482913', prod, 'not_https'],
    ['javascript: url', 'javascript:alert(1)', prod, 'not_https'],
    ['javascript: url with //', 'javascript://hub.example.ts.net/%0aalert(1)', prod, 'not_https'],
    ['ftp', 'ftp://hub.example.ts.net 482913', prod, 'not_https'],
    ['user info', `https://user:pw@hub.example.ts.net/#/pair?s=${S}`, prod, 'bad_origin'],
    ['backslash', `https://hub.example.ts.net\\@evil.com/#/pair?s=${S}`, prod, 'bad_origin'],
    ['backslash typed', 'hub\\evil 482913', prod, 'bad_origin'],
    ['bad host chars', 'hub_example.ts.net 482913', prod, 'bad_origin'],
    ['port 0', 'hub.example.ts.net:0 482913', prod, 'bad_origin'],
    ['port too big', 'hub.example.ts.net:70000 482913', prod, 'bad_origin'],
    ['ipv6 in a release build', 'https://[::1]/ 482913', prod, 'bad_origin'],
    ['host over 253 characters', `https://${'a'.repeat(254)}.com/#/pair?c=482913`, prod, 'too_long'],
    ['input over 2048 characters', `hub.example.ts.net ${'1'.repeat(2100)}`, prod, 'too_long'],
    ['url without secret or code', 'https://hub.example.ts.net/', prod, 'no_secret_or_code'],
    ['url with another route', 'https://hub.example.ts.net/#/sessions', prod, 'no_secret_or_code'],
    ['typed address alone', 'hub.example.ts.net', prod, 'no_secret_or_code'],
    ['address field with empty code', '', { dev: false, address: 'hub.example.ts.net' }, 'no_secret_or_code'],
    ['short secret', 'https://hub.example.ts.net/#/pair?s=abc', prod, 'bad_secret'],
    ['secret with a bad character', `https://hub.example.ts.net/#/pair?s=${S.slice(0, 21)}!`, prod, 'bad_secret'],
    ['five-digit code', 'hub.example.ts.net 48291', prod, 'bad_code'],
    ['code with letters', 'hub.example.ts.net 48a-913', prod, 'bad_code'],
    ['seven-digit code', 'hub.example.ts.net 4829133', prod, 'bad_code'],
    ['watchyard link without u', `watchyard://pair?s=${S}`, prod, 'bad_origin'],
    ['watchyard link with a javascript u', `watchyard://pair?u=${encodeURIComponent('javascript:alert(1)')}&s=${S}`, prod, 'not_https'],
    ['watchyard link with another host', 'watchyard://sessions?u=https://x', prod, 'bad_origin'],
    ['bad percent-encoding', 'watchyard://pair?u=%E0%A4%A&s=x', prod, 'bad_origin'],
  ];
  it.each(refused)('refuses %s', (_name, text, opts, reason) => {
    expect(parsePairInput(text, opts)).toEqual({ ok: false, reason });
  });

  it('accepts http for loopback and RFC 1918 hubs only in dev builds', () => {
    for (const host of ['localhost:18788', '127.0.0.1:18788', '10.0.0.5', '172.16.4.1:8080', '192.168.1.20:8080']) {
      expect(parsePairInput(`http://${host} 482913`, dev)).toEqual({
        ok: true,
        input: { origin: `http://${host}`, code: '482913' },
      });
      expect(parsePairInput(`http://${host} 482913`, prod)).toEqual({ ok: false, reason: 'not_https' });
    }
  });

  it('has words for every refusal', () => {
    for (const r of ['empty', 'not_https', 'bad_origin', 'no_secret_or_code', 'bad_secret', 'bad_code', 'too_long'] as const) {
      expect(pairInputMessage(r).length).toBeGreaterThan(5);
    }
  });
});

describe('claim', () => {
  const ok = { device: 'dev_phone', kind: 'phone', name: 'Pixel 9 · app', scopes: ['read', 'push'], token: 'sy_p_TOKENVALUE', hub: { url: 'https://hub.test.ts.net' } };

  it('sends the P2 body and keeps origin, token, device and name', async () => {
    const clock = manualClock();
    const f = fakeFetch(() => response(200, ok));
    const hub = await claim({ fetch: f, clock, app }, { origin: 'https://hub.test.ts.net', secret: S });
    expect(claims(f)).toHaveLength(1);
    const c = claims(f)[0]!;
    expect(c.url).toBe('https://hub.test.ts.net/v1/pair/claim');
    expect(c.init.method).toBe('POST');
    expect(c.init.headers?.Authorization).toBeUndefined();
    expect(JSON.parse(c.init.body!)).toEqual({
      secret: S,
      name: 'Pixel 9 · app',
      expect_kind: 'phone',
      cookie: false,
      client: 'app/0.8.0+12',
    });
    expect(hub).toEqual({
      origin: 'https://hub.test.ts.net',
      token: 'sy_p_TOKENVALUE',
      device: 'dev_phone',
      name: 'Pixel 9 · app',
      paired_at: new Date(clock.now()).toISOString(),
    });
  });

  it('sends the code when there is no secret', async () => {
    const f = fakeFetch(() => response(200, ok));
    await claim({ fetch: f, clock: manualClock(), app }, { origin: 'https://hub.test.ts.net', code: '482913' });
    const body = JSON.parse(claims(f)[0]!.init.body!);
    expect(body.code).toBe('482913');
    expect('secret' in body).toBe(false);
  });

  const failures: [string, () => ReturnType<typeof response> | Promise<never>, string, string][] = [
    ['401', () => response(401, { error: 'invalid or expired code', code: 'unauthorized' }), 'invalid', 'Invalid or expired code'],
    ['409 for a machine', () => response(409, { error: 'this code is for a machine', code: 'conflict' }), 'wrong_kind', 'That code is for a machine, not a phone'],
    ['409 without a kind', () => response(409, { error: 'conflict', code: 'conflict' }), 'wrong_kind', 'That code is for a different kind of device, not a phone'],
    ['429', () => response(429, { error: 'slow down', code: 'rate_limited' }), 'rate_limited', 'Too many tries, wait 10 minutes'],
    ['network', () => Promise.reject(new Error('Network request failed: https://hub.test.ts.net')), 'unreachable', "Can't reach hub.test.ts.net. Is Tailscale on?"],
    ['500', () => response(500, 'boom'), 'http', 'hub.test.ts.net answered with an error (HTTP 500).'],
    ['503', () => response(503, { error: 'frozen', code: 'unavailable' }), 'http', 'hub.test.ts.net is not taking phones right now (HTTP 503).'],
    ['404', () => response(404, 'nope'), 'other', 'Pairing failed. Try again'],
    ['certificate', () => Promise.reject(new Error('java.security.cert.CertPathValidatorException: Trust anchor for certification path not found')), 'tls', 'The secure connection to hub.test.ts.net failed: its certificate is not trusted. Check the address.'],
    ['unknown host', () => Promise.reject(new Error('Unable to resolve host "hub.test.ts.net"')), 'dns', 'hub.test.ts.net was not found. Check the address. Is Tailscale on?'],
    ['refused', () => Promise.reject(new Error('Failed to connect to hub.test.ts.net/100.64.0.1:443: ECONNREFUSED')), 'refused', 'hub.test.ts.net refused the connection. Is the hub running? Is Tailscale on?'],
    ['200 without a token', () => response(200, { device: 'd' }), 'other', 'Pairing failed. Try again'],
    ['200 with junk', () => response(200, 'not json'), 'other', 'Pairing failed. Try again'],
  ];
  it.each(failures)('maps %s', async (_n, handler, reason, message) => {
    const f = fakeFetch(handler);
    const err = await claim({ fetch: f, clock: manualClock(), app }, { origin: 'https://hub.test.ts.net', code: '482913' }).catch((e) => e);
    expect(err).toBeInstanceOf(ClaimError);
    expect(err.reason).toBe(reason);
    expect(err.message).toBe(message);
    expect(err.message).not.toContain('482913');
  });

  it('times out after 10 s as a timeout', async () => {
    const clock = manualClock();
    const f = fakeFetch((_u, init) => new Promise((_r, rej) => init.signal?.addEventListener('abort', () => rej(new Error('x')))));
    const p = claim({ fetch: f, clock, app }, { origin: 'https://hub.test.ts.net', code: '482913' }).catch((e) => e);
    await clock.advance(10_000);
    expect((await p).reason).toBe('timeout');
  });

  it('waits for a slow /health (a cold phone, a busy hub) instead of calling the hub unreachable', async () => {
    const clock = manualClock();
    const f = fakeFetch((u, init) =>
      u.endsWith('/health')
        ? new Promise((r, rej) => {
            clock.setTimeout(() => r(response(200, {})), 3_000);
            init.signal?.addEventListener('abort', () => rej(new Error('aborted')));
          })
        : response(200, ok),
    );
    const p = claim({ fetch: f, clock, app }, { origin: 'https://hub.test.ts.net', code: '482913' });
    await clock.advance(3_000);
    await expect(p).resolves.toBeTruthy();
    expect(f.calls.map((c) => c.url)).toEqual(['https://hub.test.ts.net/health', 'https://hub.test.ts.net/v1/pair/claim']);
  });

  it('probes GET /health without credentials and without following redirects before sending the secret', async () => {
    const f = fakeFetch(() => response(200, ok));
    await claim({ fetch: f, clock: manualClock(), app }, { origin: 'https://hub.test.ts.net', secret: S });
    expect(f.calls.map((c) => c.url)).toEqual(['https://hub.test.ts.net/health', 'https://hub.test.ts.net/v1/pair/claim']);
    const probe = f.calls[0]!.init;
    expect(probe.method).toBe('GET');
    expect(probe.body).toBeUndefined();
    expect(probe.headers?.Authorization).toBeUndefined();
    expect(probe.redirect).toBe('error');
    expect(f.calls[1]!.init.redirect).toBe('error');
  });

  it('sends no secret to an origin whose /health redirects', async () => {
    for (const extra of [{ status: 302 }, { status: 200, url: 'https://evil.example/health', redirected: true }]) {
      const f = fakeFetch((url) => (url.endsWith('/health') ? response(extra.status, '', extra) : response(200, ok)));
      const err = await claim({ fetch: f, clock: manualClock(), app }, { origin: 'https://hub.test.ts.net', secret: S }).catch((e) => e);
      expect(err).toBeInstanceOf(ClaimError);
      expect(err.reason).toBe('other');
      expect(claims(f)).toHaveLength(0);
    }
  });

  it('accepts a /health that answers with an error status (a 503 hub is still that hub)', async () => {
    const f = fakeFetch((url) => (url.endsWith('/health') ? response(503, { ok: false }) : response(200, ok)));
    await expect(claim({ fetch: f, clock: manualClock(), app }, { origin: 'https://hub.test.ts.net', code: '482913' })).resolves.toMatchObject({ token: 'sy_p_TOKENVALUE' });
  });

  it('keeps no token from a claim answer that was redirected or is a 3xx', async () => {
    for (const extra of [{ status: 307 }, { status: 200, url: 'https://evil.example/v1/pair/claim', redirected: true }]) {
      const f = fakeFetch((url) => (url.endsWith('/health') ? response(200, {}) : response(extra.status, ok, extra)));
      const err = await claim({ fetch: f, clock: manualClock(), app }, { origin: 'https://hub.test.ts.net', code: '482913' }).catch((e) => e);
      expect(err).toBeInstanceOf(ClaimError);
      expect(err.reason).toBe('other');
    }
  });

  it('keeps the origin without the default port', async () => {
    const f = fakeFetch(() => response(200, ok));
    const hub = await claim({ fetch: f, clock: manualClock(), app }, { origin: 'https://hub.test.ts.net:443', code: '482913' });
    expect(hub.origin).toBe('https://hub.test.ts.net');
    expect(claims(f)[0]!.url).toBe('https://hub.test.ts.net/v1/pair/claim');
  });

  it('messages and host helper', () => {
    expect(claimMessage('unreachable', 'h.ts.net')).toContain('Is Tailscale on?');
    expect(claimMessage('unreachable', '100.101.102.103:443')).toContain('Is Tailscale on?');
    expect(claimMessage('unreachable', 'hub.example.com')).toBe("Can't reach hub.example.com.");
    expect(claimMessage('refused', 'hub.example.com')).not.toContain('Tailscale');
    expect(claimMessage('dns', 'hub.example.com')).toBe('hub.example.com was not found. Check the address.');
    expect(claimMessage('timeout', 'hub.example.com')).toBe('hub.example.com did not answer in time.');
    expect(isTailscaleHost('myts.net.example.com')).toBe(false);
    expect(hostOf('https://a.b:1')).toBe('a.b:1');
  });
});

describe('claim with link keys (keys.md 7, P2 "Keys in the claim")', () => {
  const H = 'abcdefghijklmnopqrstuvwx';
  const K = 'S2V5U2VhbEtleVNlYWxLZQ';
  const keys = { sign: { alg: 'p256' as const, pub: 'BSIGN' }, box: { alg: 'p256' as const, pub: 'BBOX' }, auth: 'AUTH' };
  const cert = { type: 'device_cert', body: 'Ym9keQ', sigs: [{ by: 'hub', fp: H, sig: 'c2ln' }] };
  const reply = (over: Record<string, unknown> = {}) => ({
    device: 'dev_phone',
    kind: 'phone',
    name: 'Pixel 9 · app',
    scopes: ['read', 'push'],
    token: 'sy_p_TOKENVALUE',
    hub: {},
    hub_key: { alg: 'ed25519', pub: 'SFVCUFVC', fp: H },
    owner_key: null,
    cert,
    link: { urls: ['wss://hub.test.ts.net/v1/link'], ping_s: 180 },
    reg: 'qr',
    ...over,
  });
  const fakeLink = (configureCode: string | null = null) => {
    const log: { fn: string; args: unknown[] }[] = [];
    return {
      log,
      prepareKeys: async () => {
        log.push({ fn: 'prepareKeys', args: [] });
        return keys;
      },
      sealKeys: async (k: string, secret: string) => {
        log.push({ fn: 'sealKeys', args: [k, secret] });
        return 'SEALED';
      },
      configure: async (cfg: unknown) => {
        log.push({ fn: 'configure', args: [cfg] });
        if (configureCode) throw Object.assign(new Error('refused'), { code: configureCode });
      },
    };
  };
  const route = (r: unknown) => (url: string) => (url.endsWith('/v1/pair/claim') ? response(200, r) : response(200, {}));
  const old = { device: 'dev_phone', kind: 'phone', name: 'n', scopes: [], token: 'sy_p_T', hub: {} };

  it('parses h and k from the QR and refuses damaged ones', () => {
    expect(parsePairInput(`https://hub.test.ts.net/#/pair?s=${S}&c=482913&h=${H.toUpperCase()}&k=${K}`, prod)).toEqual({
      ok: true,
      input: { origin: 'https://hub.test.ts.net', secret: S, code: '482913', hubFp: H, sealKey: K },
    });
    expect(parsePairInput(`https://hub.test.ts.net/#/pair?s=${S}&h=short`, prod)).toEqual({ ok: false, reason: 'bad_secret' });
    expect(parsePairInput(`https://hub.test.ts.net/#/pair?s=${S}&k=bad`, prod)).toEqual({ ok: false, reason: 'bad_secret' });
    expect(parsePairInput(`https://hub.test.ts.net/#/pair?c=482913&k=${K}`, prod)).toEqual({ ok: false, reason: 'bad_secret' });
  });

  it('seals the keys with k for a QR secret, then configures the link with the pins', async () => {
    const link = fakeLink();
    const f = fakeFetch(route(reply()));
    const hub = await claim({ fetch: f, clock: manualClock(), app, link }, { origin: 'https://hub.test.ts.net', secret: S, hubFp: H, sealKey: K });
    const body = JSON.parse(claims(f)[0]!.init.body!);
    expect(body.keys_sealed).toBe('SEALED');
    expect(body.keys).toBeUndefined();
    expect(link.log.map((c) => c.fn)).toEqual(['prepareKeys', 'sealKeys', 'configure']);
    expect(link.log[1]!.args).toEqual([K, S]);
    expect(link.log[2]!.args[0]).toEqual({
      origin: 'https://hub.test.ts.net',
      device: 'dev_phone',
      urls: ['wss://hub.test.ts.net/v1/link'],
      pingS: 180,
      hubKey: { pub: 'SFVCUFVC', fp: H },
      reg: 'qr',
      cert,
      qrFp: H,
    });
    expect(hub.token).toBe('sy_p_TOKENVALUE');
  });

  it('sends the keys in the clear with a typed code (reg tofu)', async () => {
    const link = fakeLink();
    const f = fakeFetch(route(reply({ reg: 'tofu' })));
    await claim({ fetch: f, clock: manualClock(), app, link }, { origin: 'https://hub.test.ts.net', code: '482913' });
    const body = JSON.parse(claims(f)[0]!.init.body!);
    expect(body.keys).toEqual(keys);
    expect(body.keys_sealed).toBeUndefined();
    const cfg = link.log[1]!.args[0] as { reg: string; qrFp: unknown };
    expect(cfg.reg).toBe('tofu');
    expect(cfg.qrFp).toBeNull();
  });

  it('sends no keys with a QR secret but no k (a hub before F1)', async () => {
    const link = fakeLink();
    const f = fakeFetch(route(old));
    await claim({ fetch: f, clock: manualClock(), app, link }, { origin: 'https://hub.test.ts.net', secret: S });
    const body = JSON.parse(claims(f)[0]!.init.body!);
    expect('keys' in body || 'keys_sealed' in body).toBe(false);
    expect(link.log).toEqual([]);
  });

  const deletes = (f: ReturnType<typeof fakeFetch>) => f.calls.filter((c) => c.url.endsWith('/v1/devices/self') && c.init.method === 'DELETE');
  const TAMPERED = 'Pairing was tampered with; try again on your home network';
  const HUB_KEY = "This hub's key does not match the QR code";

  it.each([
    ['a hub key that is not the QR h', reply({ hub_key: { alg: 'ed25519', pub: 'X', fp: 'zzzzzzzzzzzzzzzzzzzzzzzz' } }), null, 'hub_key', HUB_KEY],
    ['reg tofu after a sealed claim', reply({ reg: 'tofu' }), null, 'tampered', TAMPERED],
    ['a reply with the keys stripped', reply({ hub_key: undefined, cert: undefined, reg: undefined }), null, 'tampered', TAMPERED],
    ['no certificate', reply({ cert: undefined }), null, 'tampered', TAMPERED],
    ['a certificate configure() refuses', reply(), 'ERR_SY_TAMPERED', 'tampered', TAMPERED],
    ['a hub key configure() refuses', reply(), 'ERR_SY_HUB_KEY', 'hub_key', HUB_KEY],
  ])('refuses %s and deletes the new device with its token', async (_n, r, code, reason, message) => {
    const link = fakeLink(code);
    const f = fakeFetch(route(r));
    const err = await claim({ fetch: f, clock: manualClock(), app, link }, { origin: 'https://hub.test.ts.net', secret: S, hubFp: H, sealKey: K }).catch((e) => e);
    expect(err).toBeInstanceOf(ClaimError);
    expect(err.reason).toBe(reason);
    expect(err.message).toBe(message);
    expect(err.message).not.toContain('sy_p_TOKENVALUE');
    const d = deletes(f);
    expect(d).toHaveLength(1);
    expect(d[0]!.init.headers?.Authorization).toBe('Bearer sy_p_TOKENVALUE');
    expect(d[0]!.init.redirect).toBe('error');
  });

  it('pairs without a link when configure fails for another reason', async () => {
    const link = fakeLink('ERR_SY_CONFIG');
    const f = fakeFetch(route(reply({ reg: 'tofu' })));
    await expect(claim({ fetch: f, clock: manualClock(), app, link }, { origin: 'https://hub.test.ts.net', code: '482913' })).resolves.toMatchObject({ device: 'dev_phone' });
    expect(deletes(f)).toHaveLength(0);
  });

  it('pairs without a link when a hub before F1 ignores typed-code keys', async () => {
    const link = fakeLink();
    const f = fakeFetch(route(old));
    await claim({ fetch: f, clock: manualClock(), app, link }, { origin: 'https://hub.test.ts.net', code: '482913' });
    expect(link.log.map((c) => c.fn)).toEqual(['prepareKeys']);
    expect(deletes(f)).toHaveLength(0);
  });

  it('fails before the claim when the keys cannot be made', async () => {
    const link = { ...fakeLink(), prepareKeys: () => Promise.reject(new Error('keystore')) };
    const f = fakeFetch(route(reply()));
    const err = await claim({ fetch: f, clock: manualClock(), app, link }, { origin: 'https://hub.test.ts.net', code: '482913' }).catch((e) => e);
    expect(err.reason).toBe('keys');
    expect(claims(f)).toHaveLength(0);
  });
});
