// Packaging checks that need no Electron binary: electron-builder.yml, the afterPack
// fuses and scripts/fetch-wy.mjs helpers. (The packed asar contents and the real fuse read
// are checked against the built package by the e2e job.)
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { resolveTarget, ldflags, sumFor } from '../../scripts/fetch-wy.mjs';

const require = createRequire(import.meta.url);
const appDir = fileURLToPath(new URL('../../', import.meta.url));
const yml = readFileSync(appDir + 'electron-builder.yml', 'utf8');
const pkg = JSON.parse(readFileSync(appDir + 'package.json', 'utf8'));
const afterPack = require('../../build/after-pack.cjs');

test('electron-builder.yml parses (electron-builder reads it with js-yaml)', () => {
  const cfg = require('js-yaml').load(yml);
  assert.match(cfg.linux.synopsis, /^Unofficial /);
});

test('electron-builder.yml: no publishing, no signing, no updater', () => {
  assert.match(yml, /^publish: null$/m);
  // Public builds say what they are: unofficial, and no one else's publisher info.
  assert.match(yml, /synopsis: "Unofficial /);
  assert.match(yml, /Not affiliated with, endorsed by or sponsored by Anthropic/);
  assert.equal(pkg.license, 'AGPL-3.0-only');
  assert.ok(pkg.author);
  const emails = (yml + JSON.stringify(pkg)).match(/[\w.+-]+@[\w.-]+\.[a-z]{2,}/gi) || [];
  assert.deepEqual(emails.filter((e) => !e.endsWith('.invalid')), [], 'no real email address in the package metadata');
  assert.match(yml, /^appId: io\.github\.mraj602\.watchyard\.desktop$/m);
  assert.match(yml, /^productName: Watchyard$/m);
  assert.match(yml, /^asar: true$/m);
  assert.match(yml, /^afterPack: build\/after-pack\.cjs$/m);
  assert.match(yml, /^ {2}identity: null$/m);
  assert.match(yml, /^ {2}hardenedRuntime: false$/m);
  assert.match(yml, /^ {2}notarize: false$/m);
  assert.match(yml, /^ {2}executableName: watchyard-desktop$/m);
  assert.match(yml, /target: \[AppImage, deb, rpm\]/);
  assert.match(yml, /^ {2}target: dmg$/m);
  assert.match(yml, /^ {2}target: nsis$/m);
  assert.ok(existsSync(appDir + 'build/after-pack.cjs'));
  assert.equal(pkg.dependencies, undefined);
  assert.ok(!('electron-updater' in (pkg.devDependencies || {})));
});

test('Linux: the app_id (desktopName) is the .desktop file name, so the shell shows the icon', () => {
  // Electron sets CHROME_DESKTOP (the Wayland app_id and libnotify's desktop-entry hint) from
  // desktopName; without it the id is "watchyard" and matches no installed entry.
  assert.equal(pkg.desktopName, 'watchyard-desktop.desktop');
  assert.match(yml, /^ {2}syncDesktopName: true$/m);
  assert.ok(existsSync(appDir + 'assets/icon.png'));
});

test('electron-builder.yml: tests, dev page and test hooks are excluded; wy is bundled', () => {
  for (const x of ['!**/*.test.js', '!src/renderer/dev/**', '!src/main/test-hooks.js']) {
    assert.ok(yml.includes(`"${x}"`), `files excludes ${x}`);
  }
  assert.match(yml, /- src\/\*\*\/\*/);
  assert.match(yml, /from: resources\/wy\/\$\{os\}-\$\{arch\}\//);
  assert.match(yml, /to: wy$/m);
});

test('after-pack flips exactly the C2 fuses', () => {
  const FuseV1Options = {
    RunAsNode: 0, EnableCookieEncryption: 1, EnableNodeOptionsEnvironmentVariable: 2,
    EnableNodeCliInspectArguments: 3, EnableEmbeddedAsarIntegrityValidation: 4, OnlyLoadAppFromAsar: 5,
    LoadBrowserProcessSpecificV8Snapshot: 6, GrantFileProtocolExtraPrivileges: 7,
  };
  const cfg = afterPack.buildConfig(FuseV1Options, { V1: '1' });
  assert.equal(cfg.version, '1');
  assert.equal(cfg[0], false); // RunAsNode
  assert.equal(cfg[1], true); // EnableCookieEncryption: sign-in cookies are encrypted on disk
  assert.equal(cfg[2], false);
  assert.equal(cfg[3], false);
  assert.equal(cfg[4], true); // asar integrity
  assert.equal(cfg[5], true); // only load from asar
  assert.equal(cfg[7], false);
  assert.equal(6 in cfg, false);
  assert.throws(() => afterPack.buildConfig({}, { V1: '1' }), /unknown fuse/);
});

test('after-pack finds the executable on each platform', () => {
  const packager = { appInfo: { productFilename: 'Watchyard' }, executableName: 'watchyard-desktop' };
  const p = (electronPlatformName) => afterPack.executablePath({ electronPlatformName, appOutDir: '/o', packager });
  // appOutDir is a host path (electron-builder runs on the build host), so the expectation
  // is joined with the host separator too.
  assert.equal(p('linux'), path.join('/o', 'watchyard-desktop'));
  assert.equal(p('darwin'), path.join('/o', 'Watchyard.app', 'Contents', 'MacOS', 'Watchyard'));
  assert.equal(p('win32'), path.join('/o', 'Watchyard.exe'));
});

test('fetch-wy: target names, ldflags, checksums', () => {
  assert.deepEqual(resolveTarget('linux', 'x64'), { builderOs: 'linux', goos: 'linux', builderArch: 'x64', goarch: 'amd64', bin: 'wy' });
  assert.equal(resolveTarget('darwin', 'arm64').builderOs, 'mac');
  assert.equal(resolveTarget('win', 'amd64').bin, 'wy.exe');
  assert.throws(() => resolveTarget('plan9', 'x64'), /unknown os/);
  assert.throws(() => resolveTarget('linux', 'mips'), /unknown arch/);
  assert.deepEqual(resolveTarget('auto', 'auto', { platform: 'win32', arch: 'arm64' }), { builderOs: 'win', goos: 'windows', builderArch: 'arm64', goarch: 'arm64', bin: 'wy.exe' });
  assert.equal(resolveTarget('auto', 'x64', { platform: 'darwin', arch: 'arm64' }).goos, 'darwin');
  assert.throws(() => resolveTarget('auto', 'auto', { platform: 'aix', arch: 'x64' }), /unknown os/);
  assert.equal(
    ldflags('0.6.0', 'abc1234'),
    '-s -w -X github.com/mraj602/watchyard/internal/version.Version=0.6.0 -X github.com/mraj602/watchyard/internal/version.Commit=abc1234',
  );
  const h = 'a'.repeat(64);
  assert.equal(sumFor(`${h}  wy_0.6.0_linux_amd64.tar.gz\n`, 'wy_0.6.0_linux_amd64.tar.gz'), h);
  assert.equal(sumFor(`${h}  other\n`, 'wy_x'), null);
});
