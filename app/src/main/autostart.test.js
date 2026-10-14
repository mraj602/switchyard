import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import fsp from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

import { create } from './autostart.js';
import { fakeElectron } from '../../test/fakes/electron.js';

const homes = [];
after(() => Promise.all(homes.map((h) => fsp.rm(h, { recursive: true, force: true }))));

async function setup(over = {}) {
  const home = await fsp.mkdtemp(path.join(os.tmpdir(), 'sy-autostart-'));
  homes.push(home);
  const fake = fakeElectron({ platform: over.platform ?? 'linux', isPackaged: over.isPackaged ?? true });
  const a = create({
    electron: fake.electron, fs: fsp, path, platform: over.platform ?? 'linux', home,
    execPath: '/opt/Switchyard/switchyard-desktop', ...over,
  });
  return { home, fake, a };
}

const GOLDEN_BACKGROUND = `[Desktop Entry]
Type=Application
Name=Switchyard Desktop
Comment=Usage, limits and alerts for several Claude accounts (unofficial)
Exec="/opt/Switchyard/switchyard-desktop" --background
Icon=switchyard-desktop
Terminal=false
Categories=Utility;Development;
StartupWMClass=switchyard-desktop
X-GNOME-Autostart-enabled=true
X-GNOME-Autostart-Delay=10
`;

const GOLDEN_PLAIN = `[Desktop Entry]
Type=Application
Name=Switchyard Desktop
Comment=Usage, limits and alerts for several Claude accounts (unofficial)
Exec="/opt/Switchyard/switchyard-desktop"
Icon=switchyard-desktop
Terminal=false
Categories=Utility;Development;
StartupWMClass=switchyard-desktop
`;

test('golden: the desktop entry, background and not', async () => {
  const { a } = await setup();
  assert.equal(a.desktopEntry(true), GOLDEN_BACKGROUND);
  assert.equal(a.desktopEntry(false), GOLDEN_PLAIN);
});

test('quoting: spaces, quotes, dollar, backtick, backslash and percent are escaped', async () => {
  const { a } = await setup({ execPath: '/opt/My "App"/$x`y\\z%d/sw' });
  assert.match(a.desktopEntry(false), /^Exec="\/opt\/My \\"App\\"\/\\\$x\\`y\\\\z%%d\/sw"$/m);
});

test('an AppImage uses $APPIMAGE, and a development run names the app path and the user-data override', async () => {
  const img = await setup({ appImage: '/home/u/Switchyard.AppImage' });
  assert.match(img.a.desktopEntry(true), /^Exec="\/home\/u\/Switchyard\.AppImage" --background$/m);
  const dev = await setup({ isPackaged: false, execPath: '/n/electron', appPath: '/src/app', env: { SY_APP_USER_DATA: '/tmp/x' } });
  // The override is path.resolve()d (drive letter and backslashes on Windows), then quoted
  // as in desktopEntry: backslashes and quotes are escaped.
  const userData = path.resolve('/tmp/x').replace(/(["\\`$])/g, '\\$1');
  assert.equal(
    dev.a.desktopEntry(true).split('\n').find((l) => l.startsWith('Exec=')),
    `Exec=env SY_APP_USER_DATA="${userData}" "/n/electron" "/src/app" --background`,
  );
  const packaged = await setup({ env: { SY_APP_USER_DATA: '/tmp/x' } });
  assert.doesNotMatch(packaged.a.desktopEntry(true), /SY_APP_USER_DATA/);
});

test('linux: set(true) writes both entries, get() reads, set(false) removes only the autostart one', async () => {
  const { home, a } = await setup();
  assert.equal(await a.get(), false);
  await a.set(true);
  assert.equal(await a.get(), true);
  const f = a.files();
  assert.equal(f.autostart, path.join(home, '.config/autostart/switchyard-desktop.desktop'));
  assert.equal(f.launcher, path.join(home, '.local/share/applications/switchyard-desktop.desktop'));
  assert.equal(await fsp.readFile(f.autostart, 'utf8'), GOLDEN_BACKGROUND);
  assert.equal(await fsp.readFile(f.launcher, 'utf8'), GOLDEN_PLAIN);
  await a.set(false);
  assert.equal(await a.get(), false);
  await assert.doesNotReject(fsp.access(f.launcher));
  await a.set(false); // idempotent
});

test('no path written contains switchyard.desktop (the prototype entry is never touched)', async () => {
  const { home, a } = await setup();
  await fsp.mkdir(path.join(home, '.config/autostart'), { recursive: true });
  const proto = path.join(home, '.config/autostart/switchyard.desktop');
  await fsp.writeFile(proto, 'prototype');
  await a.set(true);
  await a.set(false);
  assert.equal(await fsp.readFile(proto, 'utf8'), 'prototype');
  const all = [];
  for (const d of ['.config/autostart', '.local/share/applications']) all.push(...(await fsp.readdir(path.join(home, d))));
  assert.ok(all.every((n) => n === 'switchyard.desktop' || n === 'switchyard-desktop.desktop'));
  for (const p of Object.values(a.files())) assert.ok(!p.endsWith('/switchyard.desktop'));
});

test('darwin and win32: a login item with --background, nothing written to disk', async () => {
  for (const platform of ['darwin', 'win32']) {
    const { home, fake, a } = await setup({ platform });
    assert.equal(await a.get(), false);
    await a.set(true);
    const [item] = fake.callsOf('app.setLoginItemSettings')[0];
    assert.equal(item.openAtLogin, true);
    assert.deepEqual(item.args, ['--background']);
    assert.equal(item.openAsHidden, platform === 'darwin' ? true : undefined);
    assert.equal(await a.get(), true);
    await a.set(false);
    assert.equal(await a.get(), false);
    assert.deepEqual(await fsp.readdir(home), []);
  }
});
