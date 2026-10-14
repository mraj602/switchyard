// The desktop token store (C6): hub-token.bin = safeStorage.encryptString(JSON.stringify(
// {url, device, token})), written atomically with mode 0600. The token is bound to the hub URL
// it was made for: a file for another URL is deleted and pairing starts again, and so is a
// file that decrypts to something that is not a token record. A file that cannot be decrypted
// is kept: the keyring may only be locked or not up yet at login, so it is tried again at the
// next load (a new pairing replaces it). Without an available keyring nothing is written (the token then lives in memory for this run only). On Linux's
// `basic_text` backend the file is encrypted with a fixed key only, so the mode is the
// protection and Settings shows a warning (hub state `storage_backend`).

import { FILE_MODE } from '../../shared/constants.js';

/** One spelling per hub URL: scheme and host lower-cased (URL does it), no trailing slash, no hash. */
export function normalizeHubUrl(u) {
  try {
    const x = new URL(u);
    if (x.protocol !== 'http:' && x.protocol !== 'https:') return null;
    if (x.username || x.password) return null;
    return `${x.origin}${x.pathname.replace(/\/+$/, '')}`;
  } catch {
    return null;
  }
}

/**
 * @param {{safeStorage: object, fs: object, path: object, file: string, log?: object, platform?: string}} deps
 *   fs: node:fs/promises
 */
export function createTokenStore(deps) {
  const { safeStorage, fs, path, file } = deps;
  const log = deps.log ?? console;
  const platform = deps.platform ?? process.platform;

  let warnedDecrypt = false;

  const unlink = async () => {
    try {
      await fs.unlink(file);
    } catch (err) {
      if (err?.code !== 'ENOENT') log.warn?.(`hub token: cannot remove file: ${err?.code ?? err}`);
    }
  };

  return {
    /** The storage backend name on Linux ('basic_text' = no keyring), else null. */
    backend() {
      if (platform !== 'linux') return null;
      try {
        return safeStorage.getSelectedStorageBackend?.() ?? null;
      } catch {
        return null;
      }
    },
    /** True when the token can be stored encrypted at all. */
    available() {
      try {
        // On Linux's basic_text backend Electron reports no encryption until plain-text
        // encryption is allowed; C6 wants the file written there (mode 0600, Settings warns).
        if (platform === 'linux' && this.backend() === 'basic_text') safeStorage.setUsePlainTextEncryption?.(true);
        return safeStorage.isEncryptionAvailable();
      } catch {
        return false;
      }
    },
    /**
     * @param {string|null} hubUrl  the hub URL now; a token for another URL is dropped
     * @returns {Promise<{url: string, device: string, token: string}|null>}
     */
    async load(hubUrl) {
      let buf;
      try {
        buf = await fs.readFile(file);
      } catch (err) {
        if (err?.code !== 'ENOENT') log.warn?.(`hub token: cannot read file: ${err?.code ?? err}`);
        return null;
      }
      let text;
      try {
        this.available(); // on basic_text: allow plain-text encryption before decrypting
        text = safeStorage.decryptString(buf);
      } catch {
        // Maybe the keyring is locked or not available yet: keep the file for the next try.
        if (!warnedDecrypt) log.warn?.('hub token: file cannot be decrypted now (keyring unavailable?); keeping it to try again');
        warnedDecrypt = true;
        return null;
      }
      warnedDecrypt = false;
      let rec = null;
      try {
        rec = JSON.parse(text);
      } catch {
        rec = null; // decrypted, but not JSON: a bad record
      }
      const ok = rec && typeof rec.url === 'string' && typeof rec.device === 'string' && typeof rec.token === 'string' && rec.token !== '';
      if (!ok) {
        await unlink();
        return null;
      }
      if (!hubUrl || normalizeHubUrl(rec.url) !== normalizeHubUrl(hubUrl)) {
        await unlink();
        return null;
      }
      return { url: rec.url, device: rec.device, token: rec.token };
    },
    /** @returns {Promise<boolean>} true when written (false: no secure storage, memory only) */
    async save({ url, device, token }) {
      if (!this.available()) return false;
      const enc = safeStorage.encryptString(JSON.stringify({ url, device, token }));
      const tmp = `${file}.tmp-${process.pid}`;
      const fh = await fs.open(tmp, 'w', FILE_MODE);
      try {
        await fh.writeFile(enc);
        await fh.sync();
      } finally {
        await fh.close();
      }
      try {
        await fs.chmod?.(tmp, FILE_MODE);
        await fs.rename(tmp, file);
      } catch (err) {
        await fs.unlink(tmp).catch(() => {});
        throw err;
      }
      return true;
    },
    clear: unlink,
  };
}
