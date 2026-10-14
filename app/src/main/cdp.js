// The DevTools-protocol guard (C2): a packaged app refuses to start with a remote debugging
// switch, since anyone who can start it with one could drive the signed-in claude.ai tabs and
// the hub board. The only exception is a build that baked `switchyard.allowCdp: true` into
// its package.json (the asar copy): the e2e build sets it with electron-builder
// `-c.extraMetadata.switchyard.allowCdp=true` (scripts/dist-e2e.sh); release builds never do.
// Unpackaged runs (npm start, the smoke) are not affected.
//
// Pure: main/index.js passes Electron's app, process.argv and the read package.json.

export const CDP_SWITCHES = Object.freeze(['remote-debugging-port', 'remote-debugging-pipe', 'remote-debugging-address']);

/** True when argv carries `--<name>` or `--<name>=…` for one of the switches (Chromium also takes a single dash). */
function inArgv(argv, name) {
  return (argv ?? []).some((a) => {
    if (typeof a !== 'string') return false;
    const m = /^--?([^=]+)(?:=.*)?$/s.exec(a);
    return m !== null && m[1].toLowerCase() === name;
  });
}

/** The build-time allowance: package.json `switchyard.allowCdp` set to true. */
export function cdpAllowed(pkg) {
  const v = pkg?.switchyard?.allowCdp;
  return v === true || v === 'true';
}

/**
 * @param {{isPackaged: boolean, hasSwitch?: (name: string) => boolean, argv?: string[], pkg?: object|null}} o
 *   hasSwitch: app.commandLine.hasSwitch; argv: process.argv; pkg: the app's own package.json
 * @returns {string|null} the refused switch, or null when the start may go on
 */
export function refusedCdpSwitch({ isPackaged, hasSwitch, argv, pkg }) {
  if (!isPackaged || cdpAllowed(pkg)) return null;
  for (const name of CDP_SWITCHES) {
    let present = inArgv(argv, name);
    try {
      present ||= hasSwitch?.(name) === true;
    } catch {
      // no command line to ask: argv decides
    }
    if (present) return name;
  }
  return null;
}
