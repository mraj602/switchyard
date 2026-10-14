#!/usr/bin/env node
// Prebuild sanity check (phases/8.md test plan, wave 0 facts 1-3). Copies phone/ to a temp
// directory (node_modules symlinked), runs `expo prebuild --platform <p> --no-install --clean`
// there, and asserts the generated native config. The real tree is never touched, nothing
// is signed or uploaded, and the copy is deleted afterwards (set SY_KEEP_PREBUILD=1 to keep it).
// Usage: node scripts/prebuild-check.mjs android|ios
import { execFileSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';

const phone = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const platform = process.argv[2];
if (platform !== 'android' && platform !== 'ios') {
  console.error('usage: node scripts/prebuild-check.mjs android|ios');
  process.exit(2);
}

const SKIP = new Set(['node_modules', 'android', 'ios', '.expo', '.git', 'coverage', 'dist']);
const work = fs.mkdtempSync(path.join(os.tmpdir(), 'sy-prebuild-'));
const app = path.join(work, 'phone');
// Top-level only: the local modules keep their own android/ sources (modules/sy-link/android),
// minus their build outputs.
const MODULE_SKIP = new Set(['build', '.cxx', '.gradle']);
fs.cpSync(phone, app, {
  recursive: true,
  filter: (src) => src === phone || (path.dirname(src) === phone ? !SKIP.has(path.basename(src)) : !MODULE_SKIP.has(path.basename(src)) && path.basename(src) !== 'node_modules'),
});
fs.symlinkSync(path.join(phone, 'node_modules'), path.join(app, 'node_modules'), 'dir');

const failures = [];
const check = (ok, what) => {
  (ok ? console.log : console.error)(`${ok ? 'ok  ' : 'FAIL'} ${what}`);
  if (!ok) failures.push(what);
};
const read = (...p) => fs.readFileSync(path.join(app, ...p), 'utf8');
const findFiles = (dir, re, out = []) => {
  if (!fs.existsSync(dir)) return out;
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory() && e.name !== 'Pods' && e.name !== 'build') findFiles(p, re, out);
    else if (re.test(e.name)) out.push(p);
  }
  return out;
};

try {
  const env = { ...process.env, CI: '1', EXPO_NO_TELEMETRY: '1', EXPO_NO_GIT_STATUS: '1', SY_APP_VARIANT: 'production' };
  delete env.CLAUDE_CONFIG_DIR;
  execFileSync('npx', ['expo', 'prebuild', '--platform', platform, '--no-install', '--clean'], { cwd: app, env, stdio: 'inherit' });

  if (platform === 'android') {
    const manifest = read('android', 'app', 'src', 'main', 'AndroidManifest.xml');
    check(manifest.includes('android.permission.POST_NOTIFICATIONS'), 'POST_NOTIFICATIONS permission');
    check(manifest.includes('android.permission.CAMERA'), 'CAMERA permission');
    for (const p of ['RECORD_AUDIO', 'SYSTEM_ALERT_WINDOW']) {
      const re = new RegExp(`<uses-permission[^>]*android\\.permission\\.${p}"(?![^>]*tools:node="remove")[^>]*>`);
      check(!re.test(manifest), `no ${p} (or removed with tools:node="remove")`);
    }
    check(/android:scheme="switchyard"/.test(manifest), 'switchyard intent filter');
    const receivers = [...manifest.matchAll(/<receiver[\s\S]*?<\/receiver>/g)].map((m) => m[0]).filter((r) => r.includes('android.appwidget.action.APPWIDGET_UPDATE'));
    check(receivers.length === 2, `two AppWidgetProvider receivers (found ${receivers.length})`);
    for (const name of ['SyNeedsYou', 'SyAccounts']) check(receivers.some((r) => r.includes(name)), `widget receiver ${name}`);
    check(/<application[^>]*android:allowBackup="false"/.test(manifest), 'allowBackup="false"');
    check(!/<application[^>]*android:usesCleartextTraffic="true"/.test(manifest), 'no cleartext in release');
    const xml = findFiles(path.join(app, 'android', 'app', 'src', 'main', 'res'), /\.xml$/);
    const infos = xml.filter((f) => fs.readFileSync(f, 'utf8').includes('<appwidget-provider'));
    check(infos.length === 2, `two appwidget-provider infos (found ${infos.length})`);
    check(!fs.existsSync(path.join(app, 'android', 'android')), 'no doubled android/android path');
    // The self-hosted profile (P13, push.md 14.2). The sy-link module's own manifest is merged
    // by Gradle, so its entries are read there; the app's must carry no FCM entry.
    const link = fs.readFileSync(path.join(app, 'modules', 'sy-link', 'android', 'src', 'main', 'AndroidManifest.xml'), 'utf8');
    const both = manifest + link;
    check(/<service[^>]*SyLinkService[^>]*android:foregroundServiceType="specialUse"/.test(link), 'sy-link service with foregroundServiceType specialUse');
    check(/android\.app\.PROPERTY_SPECIAL_USE_FGS_SUBTYPE/.test(link), 'specialUse subtype property');
    check(both.includes('android.permission.FOREGROUND_SERVICE_SPECIAL_USE'), 'FOREGROUND_SERVICE_SPECIAL_USE permission');
    check(both.includes('android.permission.RECEIVE_BOOT_COMPLETED'), 'RECEIVE_BOOT_COMPLETED permission');
    check(both.includes('android.permission.REQUEST_INSTALL_PACKAGES'), 'REQUEST_INSTALL_PACKAGES permission');
    check(!manifest.includes('com.google.firebase'), 'no com.google.firebase entry in the app manifest');
    check(new RegExp('ExpoFirebaseMessagingService"[^>]*tools:node="remove"').test(manifest), 'FCM service removed from the merged manifest');
    const gradle = ['build.gradle', path.join('app', 'build.gradle')].map((f) => read('android', f)).join('\n');
    check(!/google-services|com\.google\.gms/.test(gradle), 'no google-services Gradle plugin');
    check(!fs.existsSync(path.join(app, 'android', 'app', 'google-services.json')), 'no google-services.json');
    const props = read('android', 'gradle.properties');
    const minSdk = /android\.minSdkVersion=(\d+)/.exec(props);
    check(!!minSdk && Number(minSdk[1]) >= 31, `minSdkVersion 31 (found ${minSdk ? minSdk[1] : 'none'})`);
  } else {
    const plists = findFiles(path.join(app, 'ios'), /^Info\.plist$/);
    const mainPlist = plists.find((f) => !/Widget|Extension|ExpoWidgetsTarget/i.test(path.relative(path.join(app, 'ios'), f)));
    const plist = mainPlist ? fs.readFileSync(mainPlist, 'utf8') : '';
    check(Boolean(mainPlist), 'app Info.plist');
    check(/<key>UIBackgroundModes<\/key>\s*<array>[\s\S]*?<string>remote-notification<\/string>/.test(plist), 'UIBackgroundModes remote-notification');
    check(/<key>NSSupportsLiveActivities<\/key>\s*<true\/>/.test(plist), 'NSSupportsLiveActivities');
    check(plist.includes('<key>NSCameraUsageDescription</key>'), 'NSCameraUsageDescription');
    check(/<key>CFBundleURLSchemes<\/key>\s*<array>[\s\S]*?<string>switchyard<\/string>/.test(plist), 'switchyard URL scheme');
    const ents = findFiles(path.join(app, 'ios'), /\.entitlements$/);
    const withGroup = ents.filter((f) => /com\.apple\.security\.application-groups[\s\S]*?group\./.test(fs.readFileSync(f, 'utf8')));
    check(withGroup.length >= 2, `App Group entitlement on the app and the widget extension (${withGroup.map((f) => path.relative(app, f)).join(', ') || 'none'})`);
    check(ents.some((f) => fs.readFileSync(f, 'utf8').includes('aps-environment')), 'aps-environment entitlement');
    const pbx = findFiles(path.join(app, 'ios'), /^project\.pbxproj$/).map((f) => fs.readFileSync(f, 'utf8')).join('\n');
    check(/com\.apple\.product-type\.app-extension/.test(pbx), 'widget extension target in project.pbxproj');
    check(/IPHONEOS_DEPLOYMENT_TARGET = 16\.4;/.test(pbx), 'deployment target 16.4');
    const podfileProps = path.join(app, 'ios', 'Podfile.properties.json');
    if (fs.existsSync(podfileProps)) check(!/"ios\.deploymentTarget"\s*:\s*"(?!16\.4)/.test(fs.readFileSync(podfileProps, 'utf8')), 'Podfile deployment target 16.4');
  }
} catch (e) {
  failures.push(`prebuild failed: ${e instanceof Error ? e.message : String(e)}`);
  console.error(failures.at(-1));
} finally {
  if (process.env.SY_KEEP_PREBUILD === '1') console.log(`kept ${app}`);
  else fs.rmSync(work, { recursive: true, force: true });
}

if (failures.length) {
  console.error(`prebuild-check ${platform}: ${failures.length} failure(s)`);
  process.exit(1);
}
console.log(`prebuild-check ${platform}: ok`);
