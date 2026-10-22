#!/usr/bin/env node
// Secrets check (docs/spec/phone.md P13): no signing or push credential may live in the
// repository. Fails when a credential-shaped file exists under phone/ (outside node_modules
// and the prebuild output, which .gitignore keeps out) or is tracked anywhere by git, or when
// a tracked phone/ text file contains a private key block.
// Usage: node scripts/check-no-secrets.mjs
import { execFileSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';

const phone = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

const NAMES = [
  /\.p8$/i,
  /\.p12$/i,
  /\.mobileprovision$/i,
  /\.provisionprofile$/i,
  /\.keystore$/i,
  /\.jks$/i,
  /\.pem$/i,
  /(^|\/)google-services\.json$/i,
  /(^|\/)GoogleService-Info\.plist$/i,
  /(^|\/)credentials\.json$/i,
  /service[-_]?account.*\.json$/i,
  /(^|\/)\.env(\.(?!example$|sample$)[^/]+)?$/i,
];
const SKIP_DIRS = new Set(['node_modules', '.git', '.expo', 'android', 'ios', 'coverage', 'dist']);
const CONTENT = [
  /-----BEGIN (RSA |EC |OPENSSH |ENCRYPTED )?PRIVATE KEY-----/,
  /"type"\s*:\s*"service_account"/,
];

export function isCredentialName(p) {
  return NAMES.some((re) => re.test(p.replaceAll('\\', '/')));
}

function walk(dir, rel, out) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const r = rel ? `${rel}/${e.name}` : e.name;
    if (e.isDirectory()) {
      if (!SKIP_DIRS.has(e.name)) walk(path.join(dir, e.name), r, out);
    } else if (isCredentialName(r)) {
      out.push(`phone/${r}: credential file in the tree`);
    }
  }
}

function tracked() {
  try {
    const top = execFileSync('git', ['rev-parse', '--show-toplevel'], { cwd: phone, encoding: 'utf8' }).trim();
    const files = execFileSync('git', ['ls-files', '-z'], { cwd: top, encoding: 'utf8' }).split('\0').filter(Boolean);
    return { top, files };
  } catch {
    return null; // not a git checkout (a tarball): the tree walk still runs
  }
}

const problems = [];
walk(phone, '', problems);
const git = tracked();
if (git) {
  for (const f of git.files) {
    if (isCredentialName(f)) problems.push(`${f}: credential file tracked by git`);
    if (!f.startsWith('phone/') || f.includes('/node_modules/')) continue;
    const abs = path.join(git.top, f);
    let text;
    try {
      if (fs.statSync(abs).size > 1_000_000) continue;
      text = fs.readFileSync(abs, 'utf8');
    } catch {
      continue;
    }
    if (CONTENT.some((re) => re.test(text))) problems.push(`${f}: contains a private key or service-account JSON`);
  }
}

if (problems.length) {
  console.error('secrets check failed (docs/spec/phone.md P13):');
  for (const p of problems) console.error(`  ${p}`);
  process.exit(1);
}
console.log(`secrets check ok${git ? '' : ' (no git: tree only)'}`);
