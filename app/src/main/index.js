// Main process entry (package.json "main"). The only module that imports Electron; it hands
// it to boot(), which creates every module and wires the start-up and shutdown order
// (boot.js header).
//
// Before anything else (before `ready`): a packaged app started with a remote debugging switch
// exits with code 1, unless its build allowed it (cdp.js, C2).
import fs from 'node:fs';
import path from 'node:path';
import electron from 'electron';
import { boot } from './boot.js';
import { refusedCdpSwitch } from './cdp.js';

function ownPackageJson(app) {
  try {
    return JSON.parse(fs.readFileSync(path.join(app.getAppPath(), 'package.json'), 'utf8'));
  } catch {
    return null; // unreadable: no allowance
  }
}

const refused = electron.app.isPackaged
  ? refusedCdpSwitch({
    isPackaged: true,
    hasSwitch: (n) => electron.app.commandLine.hasSwitch(n),
    argv: process.argv,
    pkg: ownPackageJson(electron.app),
  })
  : null;

if (refused) {
  console.error(`switchyard-desktop: --${refused} is not allowed in this build`);
  electron.app.exit(1);
  process.exit(1);
} else {
  boot(electron).catch((err) => {
    console.error('switchyard-desktop: start failed:', err);
    electron.app.exit(1);
  });
}
