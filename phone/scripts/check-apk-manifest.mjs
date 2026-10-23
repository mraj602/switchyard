#!/usr/bin/env node
// Release gate (docs/spec/phone.md P16.1): reads `aapt2 dump xmltree --file AndroidManifest.xml
// <apk>` on stdin and fails when the APK is not a release build of the expected application:
// a different package id (a .dev build), cleartext traffic allowed, debuggable, or backup allowed.
// Usage: aapt2 dump xmltree --file AndroidManifest.xml app.apk | node check-apk-manifest.mjs <package id>
import { readFileSync } from 'node:fs';

/** The reasons the manifest dump is not a release build of `expected` (empty when it is). */
export function manifestProblems(xmltree, expected) {
  const problems = [];
  const pkg = /^\s*A: package="([^"]*)"/m.exec(xmltree)?.[1];
  if (!pkg) problems.push('the manifest has no package id');
  else if (pkg !== expected) problems.push(`the package id is ${pkg}, expected ${expected}`);
  // aapt2 prints a boolean as true/false or as (type 0x12)0xffffffff / 0x0.
  const on = (name) => {
    const m = new RegExp(`:${name}\\(0x[0-9a-fA-F]+\\)=(true|false|\\(type 0x12\\)0x[0-9a-fA-F]+)`).exec(xmltree);
    if (!m) return null;
    return m[1] === 'true' || (m[1].startsWith('(type') && !/0x0+$/.test(m[1]));
  };
  if (on('debuggable') !== false && on('debuggable') !== null) problems.push('the APK is debuggable');
  if (on('usesCleartextTraffic') !== false && on('usesCleartextTraffic') !== null) problems.push('the APK allows cleartext traffic');
  if (on('allowBackup') !== false) problems.push('the APK does not set allowBackup="false"');
  return problems;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const expected = process.argv[2];
  if (!expected) {
    console.error('usage: check-apk-manifest.mjs <package id> < xmltree');
    process.exit(2);
  }
  const problems = manifestProblems(readFileSync(0, 'utf8'), expected);
  for (const p of problems) console.error(`check-apk-manifest: ${p}`);
  process.exit(problems.length ? 1 : 0);
}
