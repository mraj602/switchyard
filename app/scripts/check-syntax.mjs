#!/usr/bin/env node
// `npm run lint`, part 1: `node --check` on every .js, .mjs and .cjs file of the app
// (src/, scripts/, test/, build/), so a syntax error fails before any test runs.
import { readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const dirs = ['src', 'scripts', 'test', 'build'];
const files = [];
const walk = (dir) => {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name.startsWith('.')) continue;
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p);
    else if (/\.(c|m)?js$/.test(name)) files.push(p);
  }
};
for (const d of dirs) {
  try {
    if (statSync(join(root, d)).isDirectory()) walk(join(root, d));
  } catch {
    // folder not there (yet)
  }
}
let bad = 0;
for (const f of files) {
  const r = spawnSync(process.execPath, ['--check', f], { encoding: 'utf8' });
  if (r.status !== 0) {
    bad++;
    process.stderr.write(`${relative(root, f)}:\n${r.stderr}\n`);
  }
}
console.log(`check-syntax: ${files.length} files, ${bad} with errors`);
process.exit(bad ? 1 : 0);
