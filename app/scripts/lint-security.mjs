#!/usr/bin/env node
// `npm run lint`, part 2: the security lint of docs/spec/desktop.md C2 over src/ (test files
// excluded). Owned by WP-A. Rules (docs/phases/6.md WP-A), checked on code with comments
// removed:
//
//   nodeIntegration / nodeIntegrationInWorker / nodeIntegrationInSubFrames: true,
//   contextIsolation: false, sandbox: false, webSecurity: false, allowRunningInsecureContent
//   set to anything but false, webviewTag: true, enableRemoteModule / @electron/remote,
//   eval(, new Function(, .innerHTML = / .outerHTML =, insertAdjacentHTML, document.write(,
//   executeJavaScript…( outside main/usage/reader.js, openExternal outside main/security.js
//   (its openExternalSafe is the one caller), child_process outside main/sy/, ipcMain.on /
//   sendSync (C3: ipcMain.handle only), certificate overrides ('certificate-error',
//   setCertificateVerifyProc, ignore-certificate-errors), crashReporter (C2: never started).
//
// HTML under src/renderer/: no inline <script>, no <style>, no style= or on…= attributes,
// no javascript: URLs.
//
// The module exports lintText(rel, text) and lintTree(srcDir) for its unit test
// (src/main/security.test.js); run as a script it lints app/src and exits 1 on any finding.

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

/** {id, re, why, allow?(rel)} — rel is the path under src/, with forward slashes. */
export const JS_RULES = Object.freeze([
  { id: 'node-integration', re: /\bnodeIntegration(InWorker|InSubFrames)?\s*:\s*true\b/, why: 'nodeIntegration must stay false' },
  { id: 'context-isolation', re: /\bcontextIsolation\s*:\s*false\b/, why: 'contextIsolation must stay true' },
  { id: 'sandbox', re: /\bsandbox\s*:\s*false\b/, why: 'sandbox must stay true' },
  { id: 'web-security', re: /\bwebSecurity\s*:\s*false\b/, why: 'webSecurity must stay true' },
  { id: 'insecure-content', re: /\ballowRunningInsecureContent\s*:(?!\s*false\b)/, why: 'allowRunningInsecureContent must stay false' },
  { id: 'webview-tag', re: /\bwebviewTag\s*:\s*true\b/, why: 'webviewTag must stay false' },
  { id: 'remote-module', re: /\benableRemoteModule\b|@electron\/remote/, why: 'the remote module is not used' },
  { id: 'eval', re: /(^|[^.\w$])eval\s*\(/, why: 'no eval' },
  { id: 'new-function', re: /\bnew\s+Function\s*\(/, why: 'no new Function' },
  { id: 'inner-html', re: /\.(inner|outer)HTML\s*\+?=(?!=)/, why: 'no innerHTML/outerHTML assignment' },
  { id: 'insert-adjacent-html', re: /\binsertAdjacentHTML\b/, why: 'no insertAdjacentHTML' },
  { id: 'document-write', re: /\bdocument\.write(ln)?\s*\(/, why: 'no document.write' },
  {
    id: 'execute-javascript', re: /\bexecuteJavaScript\w*\s*\(/, why: 'executeJavaScript only in main/usage/reader.js',
    allow: (rel) => rel === 'main/usage/reader.js',
  },
  {
    id: 'open-external', re: /\bopenExternal\b/, why: 'shell.openExternal only through openExternalSafe (main/security.js)',
    allow: (rel) => rel === 'main/security.js',
  },
  {
    id: 'child-process', re: /['"](node:)?child_process['"]/, why: 'child_process only under main/sy/',
    allow: (rel) => rel.startsWith('main/sy/'),
  },
  { id: 'ipc-on', re: /\bipcMain\s*\.\s*(on|once|addListener)\s*\(|\bsendSync\b/, why: 'IPC is ipcMain.handle only (C3)' },
  {
    id: 'certificate-override', re: /['"]certificate-error['"]|\bsetCertificateVerifyProc\b|ignore-certificate-errors/,
    why: 'certificate errors are never overridden',
  },
  { id: 'crash-reporter', re: /\bcrashReporter\b/, why: 'the crash reporter is not started' },
]);

export const HTML_RULES = Object.freeze([
  { id: 'inline-script', re: /<script\b(?![^>]*\bsrc\s*=)[^>]*>/i, why: 'no inline <script> (CSP script-src self)' },
  { id: 'inline-style', re: /<style\b/i, why: 'no <style> (CSP style-src self)' },
  { id: 'style-attribute', re: /<[^>]+\sstyle\s*=/i, why: 'no style= attributes' },
  { id: 'event-attribute', re: /<[^>]+\son[a-z]+\s*=/i, why: 'no on…= handlers' },
  { id: 'javascript-url', re: /javascript\s*:/i, why: 'no javascript: URLs' },
]);

/**
 * Removes // and /* *\/ comments, keeping strings, template literals and regex literals, and
 * keeping line breaks so line numbers stay right.
 * @param {string} src
 */
export function stripComments(src) {
  let out = '';
  let i = 0;
  let prev = ''; // last significant character outside comments
  const n = src.length;
  const regexAllowedAfter = new Set(['', '(', ',', '=', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '%', '<', '>', '~', '^']);
  while (i < n) {
    const c = src[i];
    const d = src[i + 1];
    if (c === '/' && d === '/') {
      while (i < n && src[i] !== '\n') i++;
      continue;
    }
    if (c === '/' && d === '*') {
      i += 2;
      while (i < n && !(src[i] === '*' && src[i + 1] === '/')) {
        if (src[i] === '\n') out += '\n';
        i++;
      }
      i += 2;
      continue;
    }
    if (c === '"' || c === "'" || c === '`') {
      const q = c;
      let j = i + 1;
      while (j < n && src[j] !== q) {
        if (src[j] === '\\') j++;
        else if (q !== '`' && src[j] === '\n') break;
        j++;
      }
      out += src.slice(i, j + 1);
      i = j + 1;
      prev = q;
      continue;
    }
    if (c === '/' && regexAllowedAfter.has(prev)) {
      let j = i + 1;
      let inClass = false;
      while (j < n && src[j] !== '\n') {
        if (src[j] === '\\') j++;
        else if (src[j] === '[') inClass = true;
        else if (src[j] === ']') inClass = false;
        else if (src[j] === '/' && !inClass) break;
        j++;
      }
      out += src.slice(i, j + 1);
      i = j + 1;
      prev = '/';
      continue;
    }
    out += c;
    if (!/\s/.test(c)) prev = /[\w$)\].]/.test(c) ? 'x' : c;
    i++;
  }
  return out;
}

/**
 * Findings of one file.
 * @param {string} rel   path under src/, forward slashes
 * @param {string} text
 * @returns {{file: string, line: number, rule: string, why: string, text: string}[]}
 */
export function lintText(rel, text) {
  const findings = [];
  const isHtml = rel.endsWith('.html');
  if (isHtml && !rel.startsWith('renderer/')) return findings;
  const rules = isHtml ? HTML_RULES : JS_RULES;
  const code = isHtml ? text.replace(/<!--[\s\S]*?-->/g, (m) => m.replace(/[^\n]/g, ' ')) : stripComments(text);
  const lines = code.split('\n');
  const original = text.split('\n');
  lines.forEach((line, idx) => {
    for (const r of rules) {
      if (r.allow?.(rel)) continue;
      if (r.re.test(line)) findings.push({ file: rel, line: idx + 1, rule: r.id, why: r.why, text: (original[idx] ?? '').trim().slice(0, 160) });
    }
  });
  return findings;
}

const isTest = (name) => /\.test\.(c|m)?js$/.test(name);

/** Lints every .js/.mjs/.cjs/.html file under srcDir (tests excluded). */
export function lintTree(srcDir) {
  const findings = [];
  let files = 0;
  const walk = (dir) => {
    for (const name of readdirSync(dir)) {
      if (name === 'node_modules' || name.startsWith('.')) continue;
      const p = join(dir, name);
      if (statSync(p).isDirectory()) walk(p);
      else if (/\.((c|m)?js|html)$/.test(name) && !isTest(name)) {
        files++;
        const rel = relative(srcDir, p).split('\\').join('/');
        findings.push(...lintText(rel, readFileSync(p, 'utf8')));
      }
    }
  };
  walk(srcDir);
  return { files, findings };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const srcDir = fileURLToPath(new URL('../src', import.meta.url));
  const { files, findings } = lintTree(srcDir);
  for (const f of findings) process.stderr.write(`src/${f.file}:${f.line}: ${f.rule}: ${f.why}\n    ${f.text}\n`);
  console.log(`lint-security: ${files} files, ${findings.length} finding${findings.length === 1 ? '' : 's'}`);
  process.exit(findings.length ? 1 : 0);
}
