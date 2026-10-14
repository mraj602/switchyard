// The app:// protocol (C2): app://shell/<path> serves src/renderer/<path>, and
// app://shell/shared/<path> serves src/shared/<path>; traversal → 403, extension allow-list,
// the CSP and other headers of shared/constants.js.
//
// Owned by WP-A. Contract: docs/spec/desktop.md C2.
//
// The path is checked on the raw URL text, before any normalisation, so `app://shell/../x`
// and `app://shell/%2e%2e/x` are refused (403) rather than quietly folded into the root.
// Each segment is percent-decoded once and must not be empty, '.', '..', hidden (leading
// dot), or hold a slash, backslash, colon or control character. After the file is found its
// real path (symlinks resolved) must still be inside the real root.

import { SHELL_SCHEME, SHELL_HOST, SHELL_CSP, SHELL_HEADERS, SHELL_EXTENSIONS, SHELL_SHARED_PREFIX } from '../shared/constants.js';

const MIME = Object.freeze({
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.png': 'image/png',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
});

const SHARED = SHELL_SHARED_PREFIX.replace(/\/$/, '');
const RAW_RE = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([^/?#]*)([^?#]*)/;
const BAD_SEGMENT_RE = /[/\\:\x00-\x1f\x7f]/;

/** Headers on every app://shell/ response (also errors). */
export function shellHeaders(contentType) {
  const h = { 'Content-Security-Policy': SHELL_CSP, ...SHELL_HEADERS, 'Cache-Control': 'no-store' };
  if (contentType) h['Content-Type'] = contentType;
  return h;
}

/**
 * @param {{electron: object, fs: typeof import('node:fs/promises'), path: typeof import('node:path'),
 *          srcDir: string, log?: object}} deps   srcDir: absolute path of app/src
 * @returns {{
 *   registerSchemes(): void,
 *   handle(): void,
 *   resolve(url: string): {status: 200|403|404, file?: string, type?: string},
 *   respond(request: Request): Promise<Response>,
 * }}
 */
export function create(deps) {
  const { electron, fs, path, srcDir } = deps;
  const log = deps.log ?? console;
  const roots = { renderer: path.join(srcDir, 'renderer'), shared: path.join(srcDir, 'shared') };
  const realRoots = new Map();

  function registerSchemes() {
    electron.protocol.registerSchemesAsPrivileged([{ scheme: SHELL_SCHEME, privileges: { standard: true, secure: true } }]);
  }

  /** Pure mapping of a URL to a file (no disk access). */
  function resolve(url) {
    const m = typeof url === 'string' && url.length <= 2048 ? RAW_RE.exec(url) : null;
    if (!m || m[1].toLowerCase() !== SHELL_SCHEME || m[2].toLowerCase() !== SHELL_HOST) return { status: 404 };
    const raw = m[3] === '' || m[3] === '/' ? '/index.html' : m[3];
    const segments = [];
    for (const enc of raw.slice(1).split('/')) {
      let seg;
      try {
        seg = decodeURIComponent(enc);
      } catch {
        return { status: 403 };
      }
      if (seg === '' || seg === '.' || seg === '..' || seg.startsWith('.') || BAD_SEGMENT_RE.test(seg)) return { status: 403 };
      segments.push(seg);
    }
    const shared = segments[0] === SHARED;
    const root = shared ? roots.shared : roots.renderer;
    const rest = shared ? segments.slice(1) : segments;
    if (rest.length === 0) return { status: 403 };
    const name = rest.at(-1);
    const ext = path.extname(name).toLowerCase();
    if (!SHELL_EXTENSIONS.includes(ext) || /\.test\.[a-z]+$/i.test(name)) return { status: 403 };
    const file = path.join(root, ...rest);
    const rel = path.relative(root, file);
    if (!rel || rel.startsWith('..') || path.isAbsolute(rel)) return { status: 403 };
    return { status: 200, file, root, type: MIME[ext] };
  }

  const fail = (status) => new Response(status === 404 ? 'not found' : 'forbidden', { status, headers: shellHeaders('text/plain; charset=utf-8') });

  async function realRoot(root) {
    if (!realRoots.has(root)) realRoots.set(root, await fs.realpath(root));
    return realRoots.get(root);
  }

  /** The protocol handler: one request → one Response. */
  async function respond(request) {
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      return new Response('method not allowed', { status: 405, headers: shellHeaders('text/plain; charset=utf-8') });
    }
    const r = resolve(request.url);
    if (r.status !== 200) return fail(r.status);
    try {
      const real = await fs.realpath(r.file);
      const rootReal = await realRoot(r.root);
      const rel = path.relative(rootReal, real);
      if (!rel || rel.startsWith('..') || path.isAbsolute(rel)) return fail(403);
      const st = await fs.stat(real);
      if (!st.isFile()) return fail(404);
      const body = request.method === 'HEAD' ? null : await fs.readFile(real);
      return new Response(body, { status: 200, headers: shellHeaders(r.type) });
    } catch (err) {
      if (err?.code === 'ENOENT' || err?.code === 'ENOTDIR') return fail(404);
      log.warn?.(`protocol: ${request.url.slice(0, 200)}: ${err?.message ?? err}`);
      return fail(404);
    }
  }

  function handle() {
    electron.protocol.handle(SHELL_SCHEME, respond);
  }

  return Object.freeze({ registerSchemes, handle, resolve, respond });
}
