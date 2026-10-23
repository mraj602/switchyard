#!/usr/bin/env node
// No proprietary Google code in the Android app (docs/spec/phone.md P13, docs/guide/android.md
// "F-Droid and IzzyOnDroid"). Fails when Firebase, Play services, ML Kit or another closed
// Google/Play library is in:
//
//   static            the Android dependencies every installed package and local module declares
//                     (build.gradle; test, androidTest and debug-only lines aside). No Gradle
//                     run: prebuild-check.mjs android runs this mode.
//   gradle [dir]      the release runtime classpath, from `./gradlew :app:dependencies
//                     --configuration releaseRuntimeClasspath` in a prebuilt android/ (default
//                     phone/android).
//   apk <file.apk>    the classes defined in the APK's dex files (classes*.dex, read directly).
//
// Usage: node scripts/check-no-google.mjs static | gradle [androidDir] | apk <file.apk>
import { execFileSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';

const phone = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

/**
 * Closed Google / Play artifacts: Maven groups and the Java packages they ship. The first three
 * are the ones the app used to pull in (firebase-messaging through expo-notifications; Play
 * services and ML Kit through expo-camera's scanner); the rest are the other usual suspects that
 * F-Droid's scanner refuses (Play Install Referrer came with expo-application).
 */
export const FORBIDDEN = [
  { group: 'com.google.firebase', pkg: 'com/google/firebase/' },
  { group: 'com.google.android.gms', pkg: 'com/google/android/gms/' },
  { group: 'com.google.mlkit', pkg: 'com/google/mlkit/' },
  { group: 'com.google.android.play', pkg: 'com/google/android/play/' },
  { group: 'com.android.installreferrer', pkg: 'com/android/installreferrer/' },
  { group: 'com.android.billingclient', pkg: 'com/android/billingclient/' },
  { group: 'com.google.android.datatransport', pkg: 'com/google/android/datatransport/' },
];

const GROUP_RE = new RegExp(`(?:^|[\\s'"(])(${FORBIDDEN.map((f) => f.group.replace(/\./g, '\\.')).join('|')}):`);

// ------------------------------------------------------------------ static

/** Configurations that never reach the release APK. */
const NOT_SHIPPED = /^\s*(test|androidTest|debug)\w*\s*[ (]|^\s*add\(\s*["']?(test|androidTest|debug)/;

/** build.gradle lines (comments dropped) that declare a forbidden artifact for a shipped configuration. */
export function forbiddenGradleLines(text) {
  const out = [];
  text.split('\n').forEach((raw, i) => {
    const line = raw.replace(/\/\/.*$/, '');
    if (!GROUP_RE.test(line) || NOT_SHIPPED.test(line)) return;
    out.push({ line: i + 1, text: raw.trim() });
  });
  return out;
}

function gradleFiles() {
  const files = [];
  const nm = path.join(phone, 'node_modules');
  const pkgDirs = [];
  for (const e of fs.existsSync(nm) ? fs.readdirSync(nm) : []) {
    if (e.startsWith('.')) continue;
    const p = path.join(nm, e);
    if (e.startsWith('@')) for (const s of fs.readdirSync(p)) pkgDirs.push(path.join(p, s));
    else pkgDirs.push(p);
  }
  const mods = path.join(phone, 'modules');
  for (const m of fs.existsSync(mods) ? fs.readdirSync(mods) : []) pkgDirs.push(path.join(mods, m));
  for (const d of pkgDirs) {
    for (const f of ['android/build.gradle', 'android/build.gradle.kts']) {
      const p = path.join(d, f);
      if (fs.existsSync(p)) files.push(p);
    }
  }
  return files;
}

function checkStatic() {
  const files = gradleFiles();
  if (files.length < 5) return fail(`static: found only ${files.length} Android build files; run npm ci first`);
  const hits = [];
  for (const f of files) {
    for (const h of forbiddenGradleLines(fs.readFileSync(f, 'utf8'))) hits.push(`${path.relative(phone, f)}:${h.line}: ${h.text}`);
  }
  return report('static', `${files.length} Android build files`, hits);
}

// ------------------------------------------------------------------ gradle

function checkGradle(dir) {
  const android = path.resolve(dir ?? path.join(phone, 'android'));
  const gradlew = path.join(android, 'gradlew');
  if (!fs.existsSync(gradlew)) return fail(`gradle: no ${gradlew}; run expo prebuild --platform android first`);
  let out;
  try {
    out = execFileSync(gradlew, ['--no-daemon', '-q', ':app:dependencies', '--configuration', 'releaseRuntimeClasspath'], {
      cwd: android,
      encoding: 'utf8',
      maxBuffer: 64 * 1024 * 1024,
      stdio: ['ignore', 'pipe', 'inherit'],
    });
  } catch (e) {
    return fail(`gradle: ./gradlew :app:dependencies failed (${e instanceof Error ? e.message.split('\n')[0] : String(e)})`);
  }
  if (!out.includes('releaseRuntimeClasspath')) return fail('gradle: no releaseRuntimeClasspath in the output');
  const lines = out.split('\n').filter((l) => /^[\s|\\+-]*[\\+]--- /.test(l));
  if (lines.length < 50) return fail(`gradle: only ${lines.length} dependency lines; is this the app's classpath?`);
  const hits = [...new Set(lines.filter((l) => GROUP_RE.test(` ${l.replace(/^[\s|\\+-]*/, '')}`)).map((l) => l.replace(/^[\s|\\+-]*/, '').trim()))];
  return report('gradle', `${lines.length} lines of releaseRuntimeClasspath`, hits);
}

// ------------------------------------------------------------------ apk

/** The class descriptors a dex file defines (its class_defs), e.g. "Lcom/example/Foo;". */
export function dexClasses(buf) {
  const magic = buf.subarray(0, 4).toString('latin1');
  if (magic !== 'dex\n') throw new Error('not a dex file');
  const u32 = (o) => buf.readUInt32LE(o);
  const stringIdsOff = u32(0x3c);
  const typeIdsOff = u32(0x44);
  const classDefsSize = u32(0x60);
  const classDefsOff = u32(0x64);
  const str = (idx) => {
    let o = u32(stringIdsOff + idx * 4);
    while (buf[o] & 0x80) o++; // the uleb128 length
    o++;
    const end = buf.indexOf(0, o);
    return buf.subarray(o, end).toString('latin1');
  };
  const out = [];
  for (let i = 0; i < classDefsSize; i++) {
    const typeIdx = u32(classDefsOff + i * 32);
    out.push(str(u32(typeIdsOff + typeIdx * 4)));
  }
  return out;
}

function checkApk(file) {
  if (!file || !fs.existsSync(file)) return fail(`apk: no such file ${file ?? ''}`);
  const list = execFileSync('unzip', ['-Z1', file], { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 }).split('\n');
  const dexes = list.filter((n) => /^classes\d*\.dex$/.test(n));
  if (dexes.length === 0) return fail('apk: no classes*.dex in the APK');
  let total = 0;
  const counts = new Map();
  for (const d of dexes) {
    const buf = execFileSync('unzip', ['-p', file, d], { maxBuffer: 512 * 1024 * 1024 });
    const classes = dexClasses(buf);
    total += classes.length;
    for (const c of classes) {
      const f = FORBIDDEN.find((x) => c.startsWith(`L${x.pkg}`));
      if (f) counts.set(f.pkg, (counts.get(f.pkg) ?? 0) + 1);
    }
  }
  if (total < 1000) return fail(`apk: only ${total} classes in ${dexes.length} dex file(s); is this the app?`);
  const hits = [...counts].map(([pkg, n]) => `${n} classes in ${pkg.replace(/\//g, '.').replace(/\.$/, '')}`);
  return report('apk', `${total} classes in ${dexes.length} dex file(s) of ${path.basename(file)}`, hits);
}

// ------------------------------------------------------------------ main

function fail(msg) {
  console.error(`check-no-google: ${msg}`);
  return 1;
}

function report(mode, what, hits) {
  if (hits.length) {
    console.error(`check-no-google ${mode}: proprietary Google code found (${what}):`);
    for (const h of hits) console.error(`  ${h}`);
    return 1;
  }
  console.log(`check-no-google ${mode}: ok, none of ${FORBIDDEN.map((f) => f.group).join(', ')} (${what})`);
  return 0;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [mode, arg] = process.argv.slice(2);
  const code =
    mode === 'static' ? checkStatic() : mode === 'gradle' ? checkGradle(arg) : mode === 'apk' ? checkApk(arg) : fail('usage: check-no-google.mjs static | gradle [androidDir] | apk <file.apk>');
  process.exit(code);
}
