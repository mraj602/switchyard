// Pairing (PhonePair, pairing.md 5): claim a QR secret at once (Android and computers), or on
// iPhone show the install steps and the digits first and claim with the typed code in the
// installed app. Also a typed-code form and in-app QR scanning where the browser can.

import { h } from '../components/dom.js';
import { svg, toast } from '../components/ui.js';
import { claimPair } from '../api.js';
import { deviceName, isIOS, isMobile, normalizeCode, codeDigits, validCode, appPairLink } from '../platform.js';
import { pwa, isStandalone, canInstall, promptInstall } from '../pwa.js';
import { turnOn } from '../push.js';

const st = { phase: 'idle', error: '', name: '', secret: '', hubFp: '', sealKey: '', code: '', typed: false, scanning: false, other: '', busy: false, notif: '', startedAt: 0 };
let stream = null;
let scanTimer = null;
let ctxRef = null;
const again = () => window.dispatchEvent(new Event('sy-rerender'));

const ua = () => navigator.userAgent;
const ios = () => isIOS(ua(), navigator.maxTouchPoints, navigator.platform);
const kindGuess = () => (isMobile(ua(), navigator.maxTouchPoints, navigator.platform) ? 'phone' : 'browser');

export function load(ctx) {
  ctxRef = ctx;
  stopScan();
  const q = ctx.route.query;
  Object.assign(st, { phase: 'idle', error: '', secret: q.get('s') || '', hubFp: q.get('h') || '', sealKey: q.get('k') || '', code: normalizeCode(q.get('c') || ''), typed: false, busy: false, notif: '', startedAt: Date.now(), other: '' });
  if (st.secret) {
    // iPhone in Safari: the Home Screen app has its own storage, so claiming here would pair the wrong app.
    if (ios() && !isStandalone()) { st.phase = 'ios-steps'; return; }
    claim({ secret: st.secret });
  } else if (st.code) {
    st.typed = true;
  }
}

async function claim(creds) {
  st.phase = 'claiming';
  st.error = '';
  again();
  const name = deviceName(ua(), navigator.userAgentData || null);
  const first = kindGuess();
  const tryKind = (kind) => claimPair({ ...creds, name, expect_kind: kind, client: `pwa/${pwa.build || 'dev'}` });
  try {
    let reply;
    try { reply = await tryKind(first); } catch (e) {
      // The code's kind is the maker's choice: retry once with the other web kind (the code stays live on 409).
      if (e.status !== 409) throw e;
      reply = await tryKind(first === 'phone' ? 'browser' : 'phone');
    }
    st.phase = 'done';
    st.name = reply.name || name;
    history.replaceState(null, '', '#/pair'); // drop s and c from the address bar
  } catch (e) {
    st.phase = 'error';
    st.error = e.status === 401 ? 'That code is not valid any more. Codes work once, for 10 minutes: make a new one on the hub.'
      : e.status === 429 ? 'Too many tries. Wait a few minutes and try again.' : e.message;
  }
  again();
}

// ---- camera scanning (Chrome on Android) -------------------------------------------------------

const canScan = () => 'BarcodeDetector' in window && !!(navigator.mediaDevices && navigator.mediaDevices.getUserMedia);

function stopScan() {
  clearInterval(scanTimer);
  scanTimer = null;
  if (stream) stream.getTracks().forEach((t) => t.stop());
  stream = null;
  st.scanning = false;
}

async function startScan(video) {
  try {
    const detector = new window.BarcodeDetector({ formats: ['qr_code'] });
    stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } });
    video.srcObject = stream;
    await video.play();
    st.scanning = true;
    scanTimer = setInterval(async () => {
      try {
        const found = await detector.detect(video);
        for (const f of found) {
          let u;
          try { u = new URL(f.rawValue); } catch (_) { continue; }
          if (!u.hash.startsWith('#/pair?')) continue;
          if (u.origin !== location.origin) { stopScan(); st.other = u.origin; again(); return; }
          const p = new URLSearchParams(u.hash.split('?')[1]);
          stopScan();
          st.secret = p.get('s') || '';
          if (st.secret) claim({ secret: st.secret });
          return;
        }
      } catch (_) { /* keep looking */ }
    }, 300);
  } catch (e) {
    stopScan();
    toast('Could not use the camera. Type the code instead.');
  }
  again();
}

// ---- screen ---------------------------------------------------------------------------------

const logo = () => svg('svg', { viewBox: '0 0 22 22', width: 26, height: 26, 'aria-hidden': 'true' },
  svg('path', { d: 'M3 18 L11 4 L19 18', fill: 'none', stroke: '#ECEEEF', 'stroke-width': 2 }),
  svg('path', { d: 'M7 18 L11 11 L15 18', fill: 'none', stroke: '#E8B04B', 'stroke-width': 2 }));

const note = () => h('p', { class: 'pair-note' }, 'The phone gets its own read-only key. It can see your sessions and usage, and it can never control a session or see your Claude logins. Remove it any time from Devices.');

// "Have the Switchyard app? Open in app": the link carries the one-time secret, so it is only put in the page.
function appLink() {
  const href = appPairLink(location.origin, st.secret, st.code, st.hubFp, st.sealKey);
  if (!href) return null;
  return h('p', { class: 'small', 'data-key': 'open-in-app' }, 'Have the Switchyard app? ',
    h('a', { href, class: 'strong', 'data-key': 'open-in-app-link' }, 'Open in app'));
}

function installSteps() {
  const digits = st.code || '––– –––';
  return h('div', { class: 'col' },
    h('h1', {}, 'Install, then pair'),
    h('ol', { class: 'steps' },
      h('li', {}, 'Tap Share, then Add to Home Screen.'),
      h('li', {}, 'Open Switchyard from your Home Screen.'),
      h('li', {}, 'Enter this code:')),
    h('p', { class: 'pair-digits big mono', 'data-key': 'pair-digits' }, digits),
    h('p', { class: 'muted small' }, 'The code works once, for 10 minutes after it was made.'),
    appLink(),
    note());
}

function done() {
  const kids = [
    h('h1', {}, 'Paired'),
    h('p', { class: 'strong', 'data-key': 'paired-as' }, `Paired as ${st.name}`),
    canInstall() ? h('button', { type: 'button', class: 'btn btn-primary', 'data-key': 'install-app', onclick: promptInstall }, 'Install app') : null,
    h('button', {
      type: 'button', class: 'btn', 'data-key': 'turn-on-notifications',
      onclick: async (e) => {
        e.currentTarget.disabled = true;
        try {
          const r = await turnOn();
          st.notif = r.state === 'on' ? 'Notifications are on.' : r.state === 'install-first' ? 'Install the app first, then turn notifications on in Settings.'
            : r.state === 'blocked' ? 'Notifications are blocked in system settings.' : 'Notifications are off. You can turn them on in Settings.';
        } catch (ex) { st.notif = ex.name === 'AbortError' ? 'The browser could not reach its push service.' : ex.message; }
        again();
      },
    }, 'Turn on notifications'),
    st.notif ? h('p', { role: 'status', class: 'small', 'data-key': 'notif-result' }, st.notif) : null,
    h('button', { type: 'button', class: 'btn btn-primary', 'data-key': 'pair-continue', onclick: () => { ctxRef.onLoggedIn().then(() => { location.hash = '#/'; }); } }, 'Open the app'),
  ];
  return h('div', { class: 'col' }, kids);
}

function typedForm() {
  const input = h('input', {
    id: 'pair-code', name: 'code', type: 'text', inputmode: 'numeric', autocomplete: 'one-time-code', placeholder: '482-913',
    maxlength: 7, spellcheck: 'false', 'data-key': 'pair-code', value: st.code,
    oninput: (e) => { st.code = normalizeCode(e.target.value); e.target.value = st.code; },
  });
  return h('form', {
    class: 'col pair-form',
    onsubmit: (e) => {
      e.preventDefault();
      if (!validCode(st.code)) { st.error = 'Enter the 6 digits shown on your computer.'; again(); return; }
      claim({ code: codeDigits(st.code) });
    },
  },
    h('label', { for: 'pair-address' }, 'Hub address'),
    h('input', { id: 'pair-address', type: 'text', readonly: true, value: location.origin, 'aria-describedby': 'pair-addr-help' }),
    h('p', { id: 'pair-addr-help', class: 'muted small' }, 'This app can only pair with the hub it was opened from.'),
    h('label', { for: 'pair-code' }, 'Code'),
    input,
    h('button', { type: 'submit', class: 'btn btn-primary', 'data-key': 'pair-submit', disabled: st.phase === 'claiming' }, 'Pair'));
}

// One <video> for the life of the page, so a redraw does not drop the camera stream.
const videoEl = h('video', { class: 'camera-video', playsinline: true, muted: true, 'aria-label': 'Camera view' });

function camera() {
  const supported = canScan();
  return h('div', { class: 'camera' },
    h('div', { class: `camera-frame${st.scanning ? ' is-live' : ''}` }, videoEl),
    supported
      ? h('button', { type: 'button', class: 'btn btn-amber', 'data-key': 'scan', disabled: st.scanning, onclick: () => startScan(videoEl) }, st.scanning ? 'Scanning…' : 'Scan the QR code')
      : h('span', { class: 'camera-label' }, 'Use your camera app to scan the code'));
}

export function render() {
  let body;
  if (st.phase === 'ios-steps') body = installSteps();
  else if (st.phase === 'done') body = done();
  else if (st.phase === 'claiming') body = h('div', { class: 'col' }, h('h1', {}, 'Pairing…'), h('p', { class: 'muted' }, 'Checking the code with your hub.'));
  else {
    body = h('div', { class: 'col' },
      h('h1', {}, 'Pair with your hub'),
      h('p', { class: 'pair-lead' }, 'On your computer, open Add phone (desktop app, hub page, or ', h('code', {}, 'sy phone pair'), ') and point the camera at the code.'),
      camera(),
      st.other ? h('p', { class: 'form-error', role: 'alert' }, 'That code is for another hub: ', h('a', { href: `${st.other}/#/pair` }, `open ${st.other}`), ' and install the app from there.') : null,
      st.error ? h('p', { class: 'form-error', role: 'alert', 'data-key': 'pair-error' }, st.error) : null,
      st.typed || !canScan() ? typedForm() : h('button', { type: 'button', class: 'linkish amber', 'data-key': 'type-code', onclick: () => { st.typed = true; again(); } }, 'Type a code and address instead'),
      h('div', { class: 'grow' }),
      note(),
      h('p', { class: 'pair-alt' }, h('a', { href: '#/login', 'data-key': 'to-login' }, 'Sign in with an access token instead')));
  }
  return {
    title: 'Pair · Switchyard',
    node: h('div', { class: 'pair-screen' }, h('div', { class: 'pair-inner' }, h('div', { class: 'brand-row' }, logo(), h('span', { class: 'brand-name' }, 'Switchyard')), body)),
  };
}

