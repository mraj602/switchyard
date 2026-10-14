import { claims, fakeFetch, manualClock, response } from '../../../test/hubfake';
import type { AppInfo } from '../../contract/platform';
import { claim, ClaimError, claimMessage, hostOf, pairInputMessage, parsePairInput } from './index';
import type { PairInputError } from './index';

const S = 'Q2xhdWRlIGlzIG5vdCBoZXJl'.slice(0, 22); // 22 base64url characters
const app: AppInfo = { version: '0.8.0', build: '12', model: 'Pixel 9', platform: 'android', projectId: null, dev: false };

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

  it('drops an explicit default port, so later same-origin checks match (finding 5)', () => {
    expect(parsePairInput(`https://hub.example.ts.net:443/#/pair?s=${S}`, prod)).toEqual({ ok: true, input: { origin: 'https://hub.example.ts.net', secret: S } });
    expect(parsePairInput(`switchyard://pair?u=${encodeURIComponent('https://hub.example.ts.net:443')}&s=${S}`, prod)).toEqual({
      ok: true,
      input: { origin: 'https://hub.example.ts.net', secret: S },
    });
    expect(parsePairInput('hub.example.ts.net:0443 482913', prod)).toEqual({ ok: true, input: { origin: 'https://hub.example.ts.net', code: '482913' } });
    expect(parsePairInput('http://192.168.1.5:80/#/pair?c=482913', dev)).toEqual({ ok: true, input: { origin: 'http://192.168.1.5', code: '482913' } });
    expect(parsePairInput('http://192.168.1.5:443/#/pair?c=482913', dev)).toEqual({ ok: true, input: { origin: 'http://192.168.1.5:443', code: '482913' } });
  });

  it('accepts the code alone in the QR fragment', () => {
    expect(parsePairInput('https://hub.example.ts.net/#/pair?c=482-913', prod)).toEqual({
      ok: true,
      input: { origin: 'https://hub.example.ts.net', code: '482913' },
    });
  });

  it('accepts the switchyard:// link', () => {
    const u = encodeURIComponent('https://hub.example.ts.net:8443');
    expect(parsePairInput(`switchyard://pair?u=${u}&s=${S}&c=482913`, prod)).toEqual({
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
    ['switchyard link without u', `switchyard://pair?s=${S}`, prod, 'bad_origin'],
    ['switchyard link with a javascript u', `switchyard://pair?u=${encodeURIComponent('javascript:alert(1)')}&s=${S}`, prod, 'not_https'],
    ['switchyard link with another host', 'switchyard://sessions?u=https://x', prod, 'bad_origin'],
    ['bad percent-encoding', 'switchyard://pair?u=%E0%A4%A&s=x', prod, 'bad_origin'],
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
    ['500', () => response(500, 'boom'), 'other', 'Pairing failed. Try again'],
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

  it('times out after 10 s as unreachable', async () => {
    const clock = manualClock();
    const f = fakeFetch((_u, init) => new Promise((_r, rej) => init.signal?.addEventListener('abort', () => rej(new Error('x')))));
    const p = claim({ fetch: f, clock, app }, { origin: 'https://hub.test.ts.net', code: '482913' }).catch((e) => e);
    await clock.advance(10_000);
    expect((await p).reason).toBe('unreachable');
  });

  it('probes GET /health without credentials and without following redirects before sending the secret (finding 6)', async () => {
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

  it('keeps the origin without the default port (finding 5)', async () => {
    const f = fakeFetch(() => response(200, ok));
    const hub = await claim({ fetch: f, clock: manualClock(), app }, { origin: 'https://hub.test.ts.net:443', code: '482913' });
    expect(hub.origin).toBe('https://hub.test.ts.net');
    expect(claims(f)[0]!.url).toBe('https://hub.test.ts.net/v1/pair/claim');
  });

  it('messages and host helper', () => {
    expect(claimMessage('unreachable', 'h')).toContain('Is Tailscale on?');
    expect(hostOf('https://a.b:1')).toBe('a.b:1');
  });
});
