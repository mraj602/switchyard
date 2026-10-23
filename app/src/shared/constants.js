// Shared constants of the desktop app (docs/spec/desktop.md). Pure data: no Node, no
// Electron, so main, the renderer (served from app://shell/shared/) and tests all import it.

// ---- C1 folders and identity

/** Basename of the app's own user-data folder under app.getPath('appData'). */
export const USER_DATA_DIRNAME = 'switchyard-desktop';
/** The prototype's folder basename: refused as user data in any case (C1). */
export const PROTOTYPE_DIRNAME = 'switchyard';
/** Files the app owns in its user-data folder. */
export const APP_STATE_FILE = 'app-state.json';
export const HUB_TOKEN_FILE = 'hub-token.bin';
/** app-state.json schema version (`v`). */
export const APP_STATE_V = 1;
/** Mode of every file the app writes (app-state.json, hub-token.bin). */
export const FILE_MODE = 0o600;

/** Tab id: `t_` + 16 lowercase hex, or a prototype 8-hex account id kept on import. */
export const TAB_ID_PATTERN = '^(t_[0-9a-f]{16}|[0-9a-f]{8})$';
export const TAB_ID_RE = new RegExp(TAB_ID_PATTERN);
/** Partition of an account tab: PARTITION_PREFIX + tab id. */
export const TAB_PARTITION_PREFIX = 'persist:tab-';
/** Partition of the hub view (C7). */
export const HUB_PARTITION = 'persist:sy-hub';
/** Account tab name: 1 to 24 characters. */
export const TAB_NAME_MAX = 24;

/** `client` in records: CLIENT_NAME + '/' + app version. */
export const CLIENT_NAME = 'switchyard-desktop';

/** Linux autostart and launcher entry name; the prototype's `switchyard.desktop` is never touched. */
export const DESKTOP_ENTRY = 'switchyard-desktop.desktop';
export const PROTOTYPE_DESKTOP_ENTRY = 'switchyard.desktop';
/** The prototype's hook port: never bound, never connected to. */
export const PROTOTYPE_HOOK_PORT = 47821;

/** Command-line flags (C1). */
export const FLAG_BACKGROUND = '--background';
export const FLAG_AUTOSTART = '--autostart'; // --autostart=on|off

// ---- C2 security

/** The shell's origin and URL prefix. */
export const SHELL_SCHEME = 'app';
export const SHELL_HOST = 'shell';
export const SHELL_ORIGIN = 'app://shell';
export const SHELL_URL = 'app://shell/index.html';

/**
 * How app://shell/<path> maps to files (C2): `shared/<p>` → src/shared/<p>, anything else
 * → src/renderer/<p>. So the renderer imports shared code as `./shared/format.js`.
 */
export const SHELL_SHARED_PREFIX = 'shared/';
/** File extensions app://shell/ serves; anything else is 403. */
export const SHELL_EXTENSIONS = Object.freeze(['.html', '.js', '.css', '.png', '.svg', '.woff2']);

/**
 * The shell's Content-Security-Policy in its <meta> tag (byte-equal). `frame-ancestors` is
 * ignored in a meta tag (Chromium logs a console error for it), so only the header has it.
 */
export const SHELL_CSP_META =
  "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; " +
  "connect-src 'none'; media-src 'none'; object-src 'none'; frame-src 'none'; worker-src 'none'; " +
  "manifest-src 'none'; base-uri 'none'; form-action 'none'";
/** The shell's Content-Security-Policy response header (app:// handler): the meta policy plus frame-ancestors. */
export const SHELL_CSP = `${SHELL_CSP_META}; frame-ancestors 'none'`;

/** Other headers on every app://shell/ response. */
export const SHELL_HEADERS = Object.freeze({
  'X-Content-Type-Options': 'nosniff',
  'Cross-Origin-Opener-Policy': 'same-origin',
});

/** webPreferences every BrowserWindow and WebContentsView gets (C2). */
export const SAFE_WEB_PREFERENCES = Object.freeze({
  contextIsolation: true,
  sandbox: true,
  nodeIntegration: false,
  nodeIntegrationInWorker: false,
  webSecurity: true,
  allowRunningInsecureContent: false,
  webviewTag: false,
  spellcheck: false,
});

/** View kinds (navigation, open and permission policies differ per kind). */
export const VIEW_SHELL = 'shell';
export const VIEW_TAB = 'tab';
export const VIEW_HUB = 'hub';

/** Permissions allowed per session kind (C2); everything else is denied. */
export const PERMISSIONS = Object.freeze({
  tab: Object.freeze(['clipboard-sanitized-write', 'fullscreen', 'notifications', 'media']), // media: audio only
  hub: Object.freeze(['clipboard-sanitized-write']),
  shell: Object.freeze([]),
});

/** claude.ai and the sign-in hops kept inside an account tab (C2, `shared/urls.js`). */
export const CLAUDE_ORIGIN = 'https://claude.ai';
export const CLAUDE_HOSTS = Object.freeze(['claude.ai']); // and subdomains
export const SIGN_IN_HOSTS = Object.freeze(['anthropic.com', 'claude.com', 'challenges.cloudflare.com']); // and subdomains
/**
 * Hosts (and their subdomains) a subframe of an account tab may load, https only (C2). Every
 * other subframe navigation is blocked and its host logged at debug level, so the list can be
 * extended when claude.ai embeds something new.
 */
export const TAB_FRAME_HOSTS = Object.freeze([
  ...CLAUDE_HOSTS,
  ...SIGN_IN_HOSTS, // anthropic.com, claude.com, Cloudflare's challenge and Turnstile frames
  'claudeusercontent.com', // artifacts and previews, sandboxed on their own domain
  'js.stripe.com', // billing: Stripe Elements (card fields, payment request button)
  'hooks.stripe.com', // billing: Stripe's 3-D Secure hand-off frame
  'm.stripe.network', // billing: Stripe's fraud-signal frame that Stripe.js inserts
  'accounts.google.com', // the "Sign in with Google" button and One Tap frame
]);
/** Subframe URLs that load nothing from the network and are always allowed (C2). */
export const INERT_FRAME_URLS = Object.freeze(['about:blank', 'about:srcdoc']);
/** Paths of claude.ai where a tab is not readable (C5). */
export const NOT_READABLE_PATHS = Object.freeze(['/login', '/logout']);
/** A board link that hands off to an account tab (C7). */
export const CLAUDE_SESSION_LINK_PATTERN = '^https://claude\\.ai/code/session_[A-Za-z0-9]+$';

// ---- C5 web usage reading

/** Version of the reading warning; raise it to show the warning again (C5). */
export const WARNING_VERSION = 1;
/** Isolated world the reader script runs in. */
export const READER_WORLD_ID = 1001;
/** Reader result cap and uuid shape. */
export const READER_MAX_BYTES = 512 * 1024;
export const UUID_PATTERN = '^[0-9a-f-]{36}$';
/** Cookie that names the tab's active org. */
export const ACTIVE_ORG_COOKIE = 'lastActiveOrg';

/** Reading values of a tab (app-state.json tabs[].reading). */
export const READING_ON = 'on';
export const READING_OFF = 'off';
export const READING_NEVER_ASKED = 'never_asked';

/** Cadence, in milliseconds (C5). SY_APP_TEST_CADENCE_MS replaces PERIOD, FLOOR and MANUAL_FLOOR. */
export const CADENCE = Object.freeze({
  PERIOD_MS: 300_000,
  JITTER_MS: 15_000, // ±
  STAGGER_MS: 20_000, // first periodic read offset by index × STAGGER
  OPEN_DELAY_MS: 1_500, // after did-finish-load on claude.ai
  NAV_DEBOUNCE_MS: 3_000, // in-page navigation
  NAV_MIN_AGE_MS: 60_000, // in-page navigation reads only if the last read is older
  FLOOR_MS: 60_000, // never more than one read per 60 s, except manual
  MANUAL_FLOOR_MS: 10_000, // usage:refresh still needs 10 s since the last read
});
/** Error backoff (C5): 5, 10, 20, 40, then 60 min cap, plus jitter. */
export const BACKOFF_MS = Object.freeze([300_000, 600_000, 1_200_000, 2_400_000, 3_600_000]);
/** Statuses that stop reading until the person presses Resume. */
export const STOP_STATUSES = Object.freeze([401, 403, 429]);

/** Debounce of `sy flush --quiet` after appends, coalesced across tabs (C5). */
export const FLUSH_DEBOUNCE_MS = 2_000;

// ---- C6 hub connection

export const HUB_TIMEOUT_MS = 10_000;
export const HUB_REPLY_MAX_BYTES = 2 * 1024 * 1024;
export const STREAM_RETRY_MS = Object.freeze([1_000, 2_000, 4_000, 8_000, 16_000, 32_000, 60_000]);
export const STREAM_PING_TIMEOUT_MS = 60_000;
export const NEEDS_YOU_DEBOUNCE_MS = 1_000;

/** The project's release page (C8 update notice; opened only through openExternalSafe). */
export const RELEASES_URL = 'https://github.com/mraj602/switchyard/releases';

// ---- C3 state push

export const STATE_PUSH_DEBOUNCE_MS = 100;

// ---- C9 notifications

export const NOTIFY_QUESTION_MAX = 140;
export const TRAY_SESSIONS_MAX = 5;

// ---- C11 running sy

export const SY_TIMEOUT_MS = 15_000;
export const SY_LONG_TIMEOUT_MS = 60_000; // flush, desktop token, desktop owner-link
export const SY_STDOUT_MAX = 1024 * 1024;
export const SY_STDERR_MAX = 64 * 1024;

/** `sy desktop` exit codes (C4). */
export const SY_EXIT = Object.freeze({ OK: 0, USAGE: 2, REFUSED: 3, NOT_HERE: 4, UNREACHABLE: 5 });

// ---- test-only environment (read only when !app.isPackaged, through main/test-hooks.js)

export const TEST_ENV = Object.freeze({
  USER_DATA: 'SY_APP_USER_DATA',
  CLAUDE_ORIGIN: 'SY_APP_TEST_CLAUDE_ORIGIN',
  CADENCE_MS: 'SY_APP_TEST_CADENCE_MS',
  SY_BIN: 'SY_BIN',
  // '1': boot.js puts its modules on globalThis.__switchyard so the Electron smoke test can
  // inspect them from the main process (Playwright app.evaluate). Unpackaged only.
  EXPOSE: 'SY_APP_TEST_EXPOSE',
});
