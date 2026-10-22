// Pairing (PhonePair, pairing.md 5): claim a QR secret at once (Android and computers), or on
// iPhone show the install steps and the digits first and claim with the typed code in the
// installed app. Also a typed-code form and in-app QR scanning where the browser can.

import { h } from '../components/dom.js';
import { toast, brandRow } from '../components/ui.js';
import { claimPair, patchDevice } from '../api.js';
import { deviceName, isIOS, isMobile, normalizeCode, codeDigits, validCode, appPairLink } from '../platform.js';
import { pwa, isStandalone, canInstall, promptInstall } from '../pwa.js';
import { turnOn } from '../push.js';

const st = { phase: 'idle', error: '', name: '', devName: '', nameNote: '', secret: '', hubFp: '', sealKey: '', code: '', typed: false, scanning: false, other: '', busy: false, notif: '', startedAt: 0 };
let stream = null;
let scanTimer = null;
let ctxRef = null;
const again = () => window.dispatchEvent(new Event('sy-rerender'));

const ua = () => navigator.userAgent;
const ios = () => isIOS(ua(), navigator.maxTouchPoints, navigator.platform);
const mobile = () => isMobile(ua(), navigator.maxTouchPoints, navigator.platform);
const kindGuess = () => (mobile() ? 'phone' : 'browser');
// The name the hub gets: what the person typed, prefilled with a guess from the browser.
// Two Android phones in Chrome both guess "Phone X · Chrome", so the field is there to change it.
const guessName = () => deviceName(ua(), navigator.userAgentData || null);

// paired: this page claimed a code and set the session cookie, but "Open the app" has not
// started the app yet. The router signs in instead of sending the person back here.
export const paired = () => st.phase === 'done';

export function load(ctx) {
  ctxRef = ctx;
  stopScan();
  const q = ctx.route.query;
  Object.assign(st, { phase: 'idle', error: '', secret: q.get('s') || '', hubFp: q.get('h') || '', sealKey: q.get('k') || '', code: normalizeCode(q.get('c') || ''), typed: false, busy: false, notif: '', startedAt: Date.now(), other: '', devName: guessName(), nameNote: '' });
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
  const name = (st.devName || '').trim().slice(0, 60) || guessName();
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
    st.devName = st.name;
    history.replaceState(null, '', '#/pair'); // drop s and c from the address bar
  } catch (e) {
    st.phase = 'error';
    st.error = e.status === 401 ? (creds.code
      ? 'That code didn’t work. Check the 6 digits, or make a new code (codes last 10 minutes and work once).'
      : 'This pairing link didn’t work. It may have been used already or expired: make a new code (codes last 10 minutes and work once).')
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

// What a paired device may do (P4): read, and manage its own notifications; never type.
const note = () => h('p', { class: 'pair-note' }, `This ${mobile() ? 'phone' : 'browser'} gets its own key. It can see sessions and usage and manage its own notifications (Seen, Snooze, mute, quiet hours); it can’t type into a session or see your Claude logins. Remove it any time from Devices.`);

// "Have the Switchyard app? Open in app": the link carries the one-time secret, so it is only put in the page.
function appLink() {
  const href = appPairLink(location.origin, st.secret, st.code, st.hubFp, st.sealKey);
  if (!href) return null;
  return h('p', { class: 'small', 'data-key': 'open-in-app' }, 'Using the Switchyard Android app? ',
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
    nameForm(),
    mobile() ? h('p', { class: 'muted small', 'data-key': 'app-hint' }, 'On Android, the Switchyard app gets notifications over its own connection to your hub. Pair the app from an owner device: More, Devices, Add phone. In this browser, you can turn on notifications here.') : null,
    canInstall() ? h('button', { type: 'button', class: 'btn btn-primary', 'data-key': 'install-app', onclick: promptInstall }, 'Install app') : null,
    h('button', {
      type: 'button', class: 'btn', 'data-key': 'turn-on-notifications', disabled: st.notif === WAITING,
      onclick: async (e) => {
        e.currentTarget.disabled = true;
        st.notif = WAITING;
        again();
        try {
          const r = await turnOn();
          st.notif = r.state === 'on' ? 'Notifications are on.' : r.state === 'install-first' ? 'Install the app first, then turn notifications on in Settings.'
            : r.state === 'blocked' ? 'Notifications are blocked in system settings.' : 'Notifications are off. You can turn them on in Settings.';
        } catch (ex) { st.notif = ex.name === 'AbortError' ? 'The browser could not reach its notification service.' : ex.message; }
        again();
      },
    }, st.notif === WAITING ? 'Waiting for permission…' : 'Turn on notifications in this browser'),
    st.notif ? h('p', { role: 'status', class: 'small', 'data-key': 'notif-result' }, st.notif) : null,
    h('button', { type: 'button', class: 'btn btn-primary', 'data-key': 'pair-continue', onclick: () => { ctxRef.onLoggedIn().then(() => { location.hash = '#/'; }); } }, 'Open the app'),
  ];
  return h('div', { class: 'col' }, kids);
}

const WAITING = 'Waiting for permission… Answer the browser’s prompt.';

// The device's name, editable once paired (PATCH /v1/devices/self): the QR link pairs at
// once with the browser's guess, so this is where a second "Phone X · Chrome" gets its own name.
function nameForm() {
  return h('form', {
    class: 'col pair-form', 'data-key': 'paired-name-form',
    onsubmit: async (e) => {
      e.preventDefault();
      const name = (st.devName || '').trim();
      if (!name || name === st.name) return;
      try {
        const d = await patchDevice('self', { name });
        st.name = d.name || name;
        st.devName = st.name;
        st.nameNote = 'Name saved.';
      } catch (ex) { st.nameNote = ex.message; }
      again();
    },
  },
    h('label', { for: 'paired-name' }, 'Name of this device'),
    h('div', { class: 'name-row' },
      h('input', { id: 'paired-name', type: 'text', maxlength: 60, 'data-key': 'paired-name', value: st.devName, oninput: (e) => { st.devName = e.target.value; st.nameNote = ''; } }),
      h('button', { type: 'submit', class: 'btn', 'data-key': 'save-name' }, 'Save')),
    h('p', { class: 'muted small', role: 'status', 'data-key': 'name-note' }, st.nameNote || 'Shown in Devices and in alerts on your other devices.'));
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
    h('label', { for: 'pair-code' }, 'Code'),
    input,
    h('label', { for: 'pair-name' }, 'Name of this device'),
    h('input', { id: 'pair-name', name: 'name', type: 'text', maxlength: 60, 'data-key': 'pair-name', value: st.devName, oninput: (e) => { st.devName = e.target.value; } }),
    h('p', { class: 'muted small' }, 'Pairs with the hub at ', h('span', { class: 'mono', 'data-key': 'pair-address' }, location.origin), '.'),
    h('button', { type: 'submit', class: 'btn btn-primary', 'data-key': 'pair-submit', disabled: st.phase === 'claiming' }, 'Pair'));
}

// One <video> for the life of the page, so a redraw does not drop the camera stream.
const videoEl = h('video', { class: 'camera-video', playsinline: true, muted: true, 'aria-label': 'Camera view' });

// Only where this browser can read a QR code itself (P2); otherwise the phone's camera app
// opens the QR's link, and this page is just the typed code.
function camera() {
  if (!canScan() || !mobile()) return null;
  return h('div', { class: 'camera' },
    h('div', { class: `camera-frame${st.scanning ? ' is-live' : ''}` }, videoEl),
    h('button', { type: 'button', class: 'btn btn-amber', 'data-key': 'scan', disabled: st.scanning, onclick: () => startScan(videoEl) }, st.scanning ? 'Scanning…' : 'Scan the QR code'));
}

export function render() {
  let body;
  if (st.phase === 'ios-steps') body = installSteps();
  else if (st.phase === 'done') body = done();
  else if (st.phase === 'claiming') body = h('div', { class: 'col' }, h('h1', {}, 'Pairing…'), h('p', { class: 'muted' }, 'Checking the code with your hub.'));
  else {
    const cam = camera();
    body = h('div', { class: 'col' },
      h('h1', {}, mobile() ? 'Pair this phone' : 'Pair this browser'),
      mobile()
        ? h('p', { class: 'pair-lead' }, 'On an owner device, open More, Devices, Add phone. Scan the QR code with this phone’s camera, or type the 6-digit code below.')
        : h('p', { class: 'pair-lead' }, 'This browser can read the board once paired. Get a 6-digit code on an owner device: More, Devices, Add phone.'),
      st.other ? h('p', { class: 'form-error', role: 'alert' }, 'That code is for another hub: ', h('a', { href: `${st.other}/#/pair` }, `open ${st.other}`), ' and install the app from there.') : null,
      st.error ? h('p', { class: 'form-error', role: 'alert', 'data-key': 'pair-error' }, st.error) : null,
      !cam || st.typed ? typedForm() : null,
      cam,
      cam && !st.typed ? h('button', { type: 'button', class: 'linkish', 'data-key': 'type-code', onclick: () => { st.typed = true; again(); } }, 'Type the code instead') : null,
      h('div', { class: 'grow' }),
      note(),
      h('p', { class: 'pair-alt' }, mobile()
        ? h('a', { href: '#/login', 'data-key': 'to-login' }, 'Sign in with an access token instead')
        : h('a', { href: '#/login', 'data-key': 'to-login' }, 'Sign in as the owner instead')));
  }
  return {
    title: 'Pair · Switchyard',
    node: h('div', { class: 'pair-screen' }, h('div', { class: 'pair-inner' }, brandRow(), body)),
  };
}

