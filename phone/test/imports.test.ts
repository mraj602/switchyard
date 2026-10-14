// Import lint (phone.md P1): src/lib and src/contract are pure TypeScript. Following every
// relative import from them, no file may import react, react-native, expo, @expo/*,
// @react-native/* or any other package. eslint checks the direct imports; this test walks
// the whole graph so a relative hop through another folder cannot sneak one in.
import * as fs from 'node:fs';
import * as path from 'node:path';

const SRC = path.resolve(__dirname, '../src');
const ROOTS = ['lib', 'contract'].map((d) => path.join(SRC, d));
const IMPORT = /(?:import|export)\s[^'"]*?from\s*['"]([^'"]+)['"]|import\s*\(\s*['"]([^'"]+)['"]\s*\)|require\(\s*['"]([^'"]+)['"]\s*\)|^\s*import\s*['"]([^'"]+)['"]/gm;

function filesUnder(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) return filesUnder(p);
    return /\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name) ? [p] : [];
  });
}

function resolve(from: string, spec: string): string {
  const base = path.resolve(path.dirname(from), spec);
  for (const c of [base, `${base}.ts`, `${base}.tsx`, path.join(base, 'index.ts'), path.join(base, 'index.tsx')]) {
    if (fs.existsSync(c) && fs.statSync(c).isFile()) return c;
  }
  throw new Error(`${path.relative(SRC, from)}: cannot resolve ${spec}`);
}

export function importsOf(file: string): string[] {
  const text = fs.readFileSync(file, 'utf8');
  return [...text.matchAll(IMPORT)].map((m) => m[1] ?? m[2] ?? m[3] ?? m[4] ?? '');
}

/** Every forbidden import reachable from the given files: "file: spec". */
export function forbidden(entry: string[]): string[] {
  const seen = new Set<string>();
  const bad: string[] = [];
  const queue = [...entry];
  while (queue.length) {
    const f = queue.pop()!;
    if (seen.has(f)) continue;
    seen.add(f);
    for (const spec of importsOf(f)) {
      if (spec.startsWith('.')) {
        const target = resolve(f, spec);
        if (!ROOTS.some((r) => target.startsWith(r + path.sep))) bad.push(`${path.relative(SRC, f)}: ${spec} (leaves src/lib and src/contract)`);
        queue.push(target);
      } else {
        bad.push(`${path.relative(SRC, f)}: ${spec}`);
      }
    }
  }
  return bad;
}

describe('pure import graph (phone.md P1)', () => {
  it('src/lib and src/contract import no package', () => {
    expect(forbidden(ROOTS.flatMap(filesUnder))).toEqual([]);
  });

  it('the checker catches a forbidden import', () => {
    const tmp = fs.mkdtempSync(path.join(SRC, 'lib', '.probe-'));
    try {
      fs.writeFileSync(path.join(tmp, 'a.ts'), "import { b } from './b';\nexport const a = b;\n");
      fs.writeFileSync(path.join(tmp, 'b.ts'), "import 'react-native';\nexport const b = 1;\n");
      expect(forbidden([path.join(tmp, 'a.ts')])).toEqual([`${path.relative(SRC, path.join(tmp, 'b.ts'))}: react-native`]);
    } finally {
      fs.rmSync(tmp, { recursive: true, force: true });
    }
  });
});
