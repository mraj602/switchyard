// Pair screen (phone.md P2): scan, typed input, errors, confirm replace. Offline: the claim
// fetch is a fake and expo-router is mocked.
import { act, fireEvent, render, screen } from '@testing-library/react-native';
import { HubTestProvider } from '../src/state';
import type { HubState } from '../src/state';
import PairScreen from '../app/pair';
import { redirectSystemPath } from '../app/+native-intent';
import { claims, fakeFetch, manualClock, response } from './hubfake';
import type { FakeFetch } from './hubfake';
import { cameraState } from './fakes';

const mockReplace = jest.fn();
let mockParams: Record<string, string | undefined> = {};
jest.mock('expo-router', () => ({
  router: { replace: (...a: unknown[]) => mockReplace(...a) },
  useLocalSearchParams: () => mockParams,
}));

const SECRET = 'Q2xhdWRlIGlzIG5vdCBoZXJl'.slice(0, 22);
const claimOk = { device: 'dev_phone', kind: 'phone', name: 'Pixel · app', scopes: ['read', 'push'], token: 'sy_p_NEWTOKEN', hub: {} };

function renderPair(f: FakeFetch, value: Partial<HubState> = {}) {
  const pairWith = jest.fn(async () => {});
  const unpair = jest.fn(async () => {});
  const claimDeps = { fetch: f, clock: manualClock(), app: { version: '0.8.0', build: '3', model: 'Pixel', platform: 'android' as const, dev: false } };
  const ui = (
    <HubTestProvider value={{ status: 'unpaired', hub: null, pairWith, unpair, claimDeps, dev: false, ...value }}>
      <PairScreen />
    </HubTestProvider>
  );
  return { ui, pairWith, unpair };
}

async function scan(data: string) {
  const cam = screen.getByTestId('camera');
  await act(async () => {
    (cam.props as { onBarcodeScanned: (r: { data: string }) => void }).onBarcodeScanned({ data });
  });
}

beforeEach(() => {
  mockReplace.mockClear();
  mockParams = {};
  cameraState.granted = true;
});

describe('Pair screen', () => {
  it('shows the camera and the typed switch', async () => {
    const { ui } = renderPair(fakeFetch(() => response(500)));
    await render(ui);
    expect(screen.getByText('Pair with your hub')).toBeTruthy();
    expect(screen.getByTestId('camera')).toBeTruthy();
    expect(screen.getByText('Type a code and address instead')).toBeTruthy();
  });

  it('claims the first good QR, keeps the token through pairWith and opens Now', async () => {
    const f = fakeFetch(() => response(200, claimOk));
    const { ui, pairWith } = renderPair(f);
    await render(ui);
    await scan(`https://hub.example.ts.net/#/pair?s=${SECRET}`);
    expect(claims(f)).toHaveLength(1);
    expect(claims(f)[0]!.url).toBe('https://hub.example.ts.net/v1/pair/claim');
    expect(JSON.parse(claims(f)[0]!.init.body!)).toMatchObject({ secret: SECRET, expect_kind: 'phone', cookie: false, client: 'app/0.8.0+3' });
    expect(pairWith).toHaveBeenCalledWith(expect.objectContaining({ origin: 'https://hub.example.ts.net', token: 'sy_p_NEWTOKEN', device: 'dev_phone' }));
    expect(mockReplace).toHaveBeenCalledWith('/');
  });

  it('ignores a QR that is not a pairing code and says why, then still scans', async () => {
    const f = fakeFetch(() => response(200, claimOk));
    const { ui } = renderPair(f);
    await render(ui);
    await scan('https://example.com/menu');
    expect(claims(f)).toHaveLength(0);
    expect(screen.getByText('Add the 6-digit code from Add phone')).toBeTruthy();
    await scan('WIFI:S:home;; P:secret');
    expect(screen.getByText("That doesn't look like a hub address")).toBeTruthy();
    await scan(`https://hub.example.ts.net/#/pair?s=${SECRET}`);
    expect(claims(f)).toHaveLength(1);
  });

  it('does not claim twice for the same QR while a claim is running', async () => {
    let release: (r: ReturnType<typeof response>) => void = () => {};
    const f = fakeFetch((url) => (url.endsWith('/health') ? response(200, { ok: true }) : new Promise((res) => (release = res))));
    const { ui } = renderPair(f);
    await render(ui);
    const qr = `https://hub.example.ts.net/#/pair?s=${SECRET}`;
    await scan(qr);
    await scan(qr);
    expect(claims(f)).toHaveLength(1);
    await act(async () => release(response(200, claimOk)));
  });

  const errors: [string, () => ReturnType<typeof response> | Promise<never>, string][] = [
    ['401', () => response(401, { error: 'x', code: 'unauthorized' }), 'Invalid or expired code'],
    ['409', () => response(409, { error: 'code is for a machine', code: 'conflict' }), 'That code is for a machine, not a phone'],
    ['429', () => response(429, {}), 'Too many tries, wait 10 minutes'],
    ['network', () => Promise.reject(new Error('down')), "Can't reach hub.example.ts.net. Is Tailscale on?"],
  ];
  it.each(errors)('shows the %s message and keeps nothing', async (_n, handler, message) => {
    const { ui, pairWith } = renderPair(fakeFetch(handler));
    await render(ui);
    await scan(`https://hub.example.ts.net/#/pair?s=${SECRET}`);
    expect(screen.getByText(message)).toBeTruthy();
    expect(pairWith).not.toHaveBeenCalled();
    expect(mockReplace).not.toHaveBeenCalled();
  });

  it('pairs from typed address and code', async () => {
    const f = fakeFetch(() => response(200, claimOk));
    const { ui, pairWith } = renderPair(f);
    await render(ui);
    await fireEvent.press(screen.getByText('Type a code and address instead'));
    expect(screen.queryByTestId('camera')).toBeNull();
    await fireEvent.changeText(screen.getByLabelText('Hub address'), 'hub.example.ts.net');
    await fireEvent.changeText(screen.getByLabelText('Code'), '482 913');
    await fireEvent.press(screen.getByText('Pair'));
    expect(JSON.parse(claims(f)[0]!.init.body!)).toMatchObject({ code: '482913' });
    expect(claims(f)[0]!.url).toBe('https://hub.example.ts.net/v1/pair/claim');
    expect(pairWith).toHaveBeenCalled();
  });

  it('refuses typed input without a request', async () => {
    const f = fakeFetch(() => response(200, claimOk));
    const { ui } = renderPair(f);
    await render(ui);
    await fireEvent.press(screen.getByText('Type a code and address instead'));
    await fireEvent.changeText(screen.getByLabelText('Hub address'), 'http://hub.example.ts.net');
    await fireEvent.changeText(screen.getByLabelText('Code'), '482913');
    await fireEvent.press(screen.getByText('Pair'));
    expect(screen.getByText('The hub address must start with https://')).toBeTruthy();
    await fireEvent.changeText(screen.getByLabelText('Hub address'), 'hub.example.ts.net');
    await fireEvent.changeText(screen.getByLabelText('Code'), '12');
    await fireEvent.press(screen.getByText('Pair'));
    expect(screen.getByText('The code is 6 digits, like 482-913')).toBeTruthy();
    expect(claims(f)).toHaveLength(0);
  });

  it('asks for the camera when it is not allowed', async () => {
    cameraState.granted = false;
    const { ui } = renderPair(fakeFetch(() => response(500)));
    await render(ui);
    expect(screen.queryByTestId('camera')).toBeNull();
    expect(screen.getByText('Allow camera')).toBeTruthy();
  });

  it('shows why the app was unpaired', async () => {
    const { ui } = renderPair(fakeFetch(() => response(500)), { notice: 'This phone was unpaired on the hub' });
    await render(ui);
    expect(screen.getByText('This phone was unpaired on the hub')).toBeTruthy();
  });

  it('a switchyard:// link shows a pre-filled "Pair with <host>" card and claims only on the tap (P7)', async () => {
    mockParams = { origin: 'https://hub.example.ts.net', secret: SECRET };
    const f = fakeFetch(() => response(200, claimOk));
    const { ui, pairWith } = renderPair(f);
    await render(ui);
    expect(screen.getByTestId('link-confirm')).toBeTruthy();
    expect(claims(f)).toHaveLength(0);
    expect(pairWith).not.toHaveBeenCalled();
    await fireEvent.press(screen.getByText('Pair with hub.example.ts.net'));
    expect(claims(f)).toHaveLength(1);
    expect(pairWith).toHaveBeenCalledTimes(1);
  });

  it('Cancel on a link card claims nothing and brings back the scanner', async () => {
    mockParams = { origin: 'https://hub.example.ts.net', secret: SECRET };
    const f = fakeFetch(() => response(200, claimOk));
    const { ui } = renderPair(f);
    await render(ui);
    await fireEvent.press(screen.getByText('Cancel'));
    expect(screen.queryByTestId('link-confirm')).toBeNull();
    expect(screen.getByTestId('camera')).toBeTruthy();
    expect(claims(f)).toHaveLength(0);
  });

  it('end to end: an "Open in app" link through +native-intent and the router opens a pre-filled card; one claim after the tap', async () => {
    const link = `switchyard://pair?u=${encodeURIComponent('https://hub.example.ts.net')}&s=${SECRET}&c=482913`;
    const href = redirectSystemPath({ path: link, initial: true });
    // What expo-router hands the screen: the href's query as search params.
    const qi = href.indexOf('?');
    expect(href.slice(0, qi)).toBe('/pair');
    mockParams = Object.fromEntries(
      href
        .slice(qi + 1)
        .split('&')
        .map((kv) => kv.split('=').map(decodeURIComponent) as [string, string]),
    );
    const f = fakeFetch(() => response(200, claimOk));
    const { ui, pairWith } = renderPair(f);
    await render(ui);
    expect(screen.getByText('Pair with hub.example.ts.net?')).toBeTruthy();
    expect(claims(f)).toHaveLength(0);
    await fireEvent.press(screen.getByText('Pair with hub.example.ts.net'));
    expect(claims(f)).toHaveLength(1);
    expect(claims(f)[0]!.url).toBe('https://hub.example.ts.net/v1/pair/claim');
    expect(JSON.parse(claims(f)[0]!.init.body!)).toMatchObject({ secret: SECRET });
    expect(pairWith).toHaveBeenCalledWith(expect.objectContaining({ origin: 'https://hub.example.ts.net' }));
  });

  it('a link while paired asks "Unpair from <host>?" after the tap, and still claims nothing before it', async () => {
    mockParams = { origin: 'https://new.ts.net', secret: SECRET };
    const f = fakeFetch(() => response(200, claimOk));
    const { ui, unpair } = renderPair(f, { status: 'paired', hub: { origin: 'https://old.ts.net', device: 'd', name: 'n', paired_at: 't' } });
    await render(ui);
    await fireEvent.press(screen.getByText('Pair with new.ts.net'));
    expect(screen.getByText('Unpair from old.ts.net?')).toBeTruthy();
    expect(claims(f)).toHaveLength(0);
    expect(unpair).not.toHaveBeenCalled();
  });

  it('a link with a bad origin shows the refusal and claims nothing', async () => {
    mockParams = { origin: 'javascript:alert(1)', secret: SECRET };
    const f = fakeFetch(() => response(200, claimOk));
    const { ui } = renderPair(f);
    await render(ui);
    expect(claims(f)).toHaveLength(0);
    expect(screen.getByText('The hub address must start with https://')).toBeTruthy();
  });

  describe('when already paired', () => {
    const paired: Partial<HubState> = {
      status: 'paired',
      hub: { origin: 'https://old.ts.net', device: 'd', name: 'n', paired_at: 't' },
    };

    it('confirms "Unpair from <host>?" before replacing, and Cancel keeps it', async () => {
      const f = fakeFetch(() => response(200, claimOk));
      const { ui, unpair, pairWith } = renderPair(f, paired);
      await render(ui);
      await scan(`https://new.ts.net/#/pair?s=${SECRET}`);
      expect(screen.getByText('Unpair from old.ts.net?')).toBeTruthy();
      expect(claims(f)).toHaveLength(0);
      await fireEvent.press(screen.getByText('Cancel'));
      expect(screen.queryByTestId('confirm')).toBeNull();
      expect(unpair).not.toHaveBeenCalled();
      expect(pairWith).not.toHaveBeenCalled();
    });

    it('Unpair and pair clears the old hub, then claims the new one', async () => {
      const f = fakeFetch(() => response(200, claimOk));
      const { ui, unpair, pairWith } = renderPair(f, paired);
      await render(ui);
      await scan(`https://new.ts.net/#/pair?s=${SECRET}`);
      await fireEvent.press(screen.getByText('Unpair and pair'));
      expect(unpair).toHaveBeenCalledTimes(1);
      expect(claims(f)[0]!.url).toBe('https://new.ts.net/v1/pair/claim');
      expect(pairWith).toHaveBeenCalledWith(expect.objectContaining({ origin: 'https://new.ts.net' }));
    });
  });
});
