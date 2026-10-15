import { hashToRoute, incomingToRoute, nativeIntentToHref, routeToHref } from '../src/lib/links';
import type { AppRoute } from '../src/contract/routes';

const NOW: AppRoute = { name: 'now' };

describe('hashToRoute (push payload url)', () => {
  const ok: [string, AppRoute][] = [
    ['/#/', NOW],
    ['/#/sessions/s_1?alert=al_9', { name: 'session', id: 's_1', alert: 'al_9' }],
    ['/#/sessions/s_1', { name: 'session', id: 's_1' }],
    ['/#/sessions/s_1?alert=nope', { name: 'session', id: 's_1' }],
    ['/#/sessions?filter=needs_you&by=machine', { name: 'sessions', filter: 'needs_you', by: 'machine' }],
    ['/#/sessions?filter=bogus', { name: 'sessions' }],
    ['/#/accounts?seat=seat-1', { name: 'accounts', seat: 'seat-1' }],
    ['/#/accounts', { name: 'accounts' }],
    ['/#/machines?machine=m.1', { name: 'machines', machine: 'm.1' }],
    ['/#/alerts/al_01JA3B', { name: 'alert', id: 'al_01JA3B' }],
    ['/#/more', { name: 'more' }],
    ['/#/more/alerts', { name: 'alerts-settings' }],
    ['/#/more/settings', { name: 'settings' }],
    ['/#/more/peers', { name: 'peers' }],
    ['/#/more/analytics', { name: 'analytics' }],
    ['/#/sessions/ab:cd?alert=al_x', { name: 'session', id: 'ab:cd', alert: 'al_x' }],
  ];
  it.each(ok)('%s', (url, want) => {
    expect(hashToRoute(url)).toEqual(want);
  });

  const refused = [
    '',
    '//evil.example/#/',
    '//evil',
    '/\\evil',
    '/#/sessions/a\\b',
    '/\\/evil/#/',
    'javascript:alert(1)',
    'https://evil.example/#/',
    '/no-hash',
    '/#sessions',
    '/#/sessions/a/b',
    '/#/sessions/..',
    '/#/sessions/a..b',
    '/#/sessions/%2F',
    '/#/sessions/a%2Fb',
    '/#/alerts/notanalert',
    '/#/alerts/al_x/extra',
    '/#/unknown',
    '/#/more/devices',
    '/#/pair',
    '/#//sessions',
    `/#/sessions/${'a'.repeat(81)}`,
  ];
  it.each(refused)('refuses %j', (url) => {
    expect(hashToRoute(url)).toBeNull();
  });

  it('ignores a hostile seat id but keeps the route', () => {
    expect(hashToRoute('/#/accounts?seat=../x')).toEqual({ name: 'accounts' });
    expect(hashToRoute('/#/accounts?seat=a/b')).toEqual({ name: 'accounts' });
  });
});

describe('incomingToRoute (switchyard:// allow-list)', () => {
  const secret = 'abcdefghijklmnopqrstuv';
  const table: [string, AppRoute][] = [
    ['switchyard://now', NOW],
    ['switchyard://', NOW],
    ['SWITCHYARD://Now', NOW],
    ['switchyard://sessions/s1', { name: 'session', id: 's1' }],
    ['switchyard://sessions/s1?alert=al_1', { name: 'session', id: 's1', alert: 'al_1' }],
    ['switchyard://sessions', { name: 'sessions' }],
    ['switchyard://accounts?seat=abc', { name: 'accounts', seat: 'abc' }],
    ['switchyard://accounts/', { name: 'accounts' }],
    ['switchyard://machines?machine=mac1', { name: 'machines', machine: 'mac1' }],
    ['switchyard://alerts/al_77', { name: 'alert', id: 'al_77' }],
    [
      `switchyard://pair?u=${encodeURIComponent('https://hub.ts.net')}&s=${secret}&c=482913`,
      { name: 'pair', origin: 'https://hub.ts.net', secret, code: '482913' },
    ],
    ['switchyard://pair', { name: 'pair' }],
    ['switchyard://settings/notifications', { name: 'notify-settings' }],
    ['switchyard://settings/notifications?x=1', { name: 'notify-settings' }],
    // not on the list: everything opens Now
    ['switchyard://settings', NOW],
    ['switchyard://settings/other', NOW],
    ['switchyard://more', NOW],
    ['switchyard://evil', NOW],
    ['switchyard:///sessions/s1', NOW],
    ['switchyard://sessions/a/b', NOW],
    ['switchyard://sessions/..', NOW],
    ['switchyard://sessions/a%2Fb', NOW],
    ['switchyard://alerts/s1', NOW],
    ['switchyard://alerts/al_1/../x', NOW],
    ['switchyard://sessions/s\\1', NOW],
    ['switchyard://sessions/s 1', NOW],
    ['https://hub.example/#/sessions/s1', NOW],
    ['javascript:alert(1)', NOW],
    ['//evil', NOW],
    ['/\\evil', NOW],
    ['', NOW],
  ];
  it.each(table)('%s', (url, want) => {
    expect(incomingToRoute(url)).toEqual(want);
  });

  it('keeps only a plain http(s) origin from a pair link, and well-formed secret and code', () => {
    const q = (u: string, s = secret, c = '123456') =>
      `switchyard://pair?u=${encodeURIComponent(u)}&s=${s}&c=${c}`;
    // http reaches the pair screen; parsePairInput refuses it there unless the build is a development one
    expect(incomingToRoute(q('http://10.0.2.2:18787'))).toEqual({ name: 'pair', origin: 'http://10.0.2.2:18787', secret, code: '123456' });
    expect(incomingToRoute(q('ftp://hub.ts.net'))).toEqual({ name: 'pair', secret, code: '123456' });
    expect(incomingToRoute(q('https://u:p@evil.example'))).toEqual({ name: 'pair', secret, code: '123456' });
    expect(incomingToRoute(q('https://hub.ts.net/path'))).toEqual({ name: 'pair', secret, code: '123456' });
    expect(incomingToRoute(q('https://hub.ts.net:8443/'))).toEqual({ name: 'pair', origin: 'https://hub.ts.net:8443', secret, code: '123456' });
    expect(incomingToRoute(q('https://h.ts.net', 'short', '12'))).toEqual({ name: 'pair', origin: 'https://h.ts.net' });
  });
});

describe('pair link keys (keys.md 7)', () => {
  const secret = 'abcdefghijklmnopqrstuv';
  const h = 'a2b3c4d5e6f7g2h3i4j5k6l7';
  const k = 'AbCdEfGhIjKlMnOpQrStUv';
  it('carries the QR hub fingerprint and seal key to the Pair screen', () => {
    const url = `switchyard://pair?u=${encodeURIComponent('http://10.0.2.2:18787')}&s=${secret}&c=482913&h=${h}&k=${k}`;
    expect(incomingToRoute(url)).toEqual({ name: 'pair', origin: 'http://10.0.2.2:18787', secret, code: '482913', hubFp: h, sealKey: k });
    expect(nativeIntentToHref(url)).toBe(
      `/pair?origin=${encodeURIComponent('http://10.0.2.2:18787')}&secret=${secret}&code=482913&hub=${h}&key=${k}`,
    );
  });
  it('drops a malformed h or k', () => {
    const url = `switchyard://pair?u=${encodeURIComponent('https://h.ts.net')}&s=${secret}&h=SHORT&k=x`;
    expect(incomingToRoute(url)).toEqual({ name: 'pair', origin: 'https://h.ts.net', secret });
  });
});

describe('routeToHref', () => {
  it.each<[AppRoute, string]>([
    [NOW, '/'],
    [{ name: 'session', id: 'abc', alert: 'al_1' }, '/sessions/abc?alert=al_1'],
    [{ name: 'session', id: 'abc' }, '/sessions/abc'],
    [{ name: 'sessions', filter: 'working' }, '/sessions?filter=working'],
    [{ name: 'accounts', seat: 's1' }, '/accounts?seat=s1'],
    [{ name: 'machines' }, '/more/machines'],
    [{ name: 'alert', id: 'al_1' }, '/alerts/al_1'],
    [{ name: 'settings' }, '/more/settings'],
    [{ name: 'peers' }, '/more/peers'],
    [{ name: 'analytics' }, '/more/analytics'],
    [{ name: 'alerts-settings' }, '/more/alerts'],
    [{ name: 'more' }, '/more'],
    [{ name: 'pair', origin: 'https://h.ts.net', code: '123456' }, '/pair?origin=https%3A%2F%2Fh.ts.net&code=123456'],
  ])('%j -> %s', (route, href) => {
    expect(routeToHref(route)).toBe(href);
  });
});

describe('nativeIntentToHref (+native-intent)', () => {
  it.each<[string, string]>([
    ['switchyard://sessions/s1', '/sessions/s1'],
    ['/', '/'],
    ['/sessions/s1?alert=al_2', '/sessions/s1?alert=al_2'],
    ['/accounts?seat=a', '/accounts?seat=a'],
    ['//evil', '/'],
    ['/\\evil', '/'],
    ['javascript:alert(1)', '/'],
    ['exp://192.168.1.2:8081', '/'],
    ['https://evil.example/sessions/s1', '/'],
    ['/settings', '/'],
    ['/more/settings', '/'],
    ['', '/'],
    ['evil', '/'],
  ])('%j -> %s', (path, href) => {
    expect(nativeIntentToHref(path)).toBe(href);
  });

  it('a pair link opens the Pair screen pre-filled, nothing more', () => {
    const href = nativeIntentToHref('switchyard://pair?u=https%3A%2F%2Fh.ts.net&c=123456');
    // The query names are the ones app/pair.tsx reads (origin, secret, code), not the link's.
    expect(href).toBe('/pair?origin=https%3A%2F%2Fh.ts.net&code=123456');
  });
});
