// The release gate of scripts/release-apk.sh (docs/spec/phone.md P16.1).
// eslint-disable-next-line @typescript-eslint/no-require-imports
const { spawnSync } = require('node:child_process') as typeof import('node:child_process');
// eslint-disable-next-line @typescript-eslint/no-require-imports
const path = require('node:path') as typeof import('node:path');

const SCRIPT = path.join(__dirname, '..', 'scripts', 'check-apk-manifest.mjs');
const ID = 'io.github.mraj602.watchyard';

function dump(over: { pkg?: string; debuggable?: string; cleartext?: string; backup?: string } = {}) {
  const A = 'http://schemas.android.com/apk/res/android';
  const lines = ['N: android=' + A + ' (line=2)', '  E: manifest (line=2)', `    A: package="${over.pkg ?? ID}" (Raw: "${over.pkg ?? ID}")`, '    E: application (line=7)'];
  if (over.debuggable) lines.push(`      A: ${A}:debuggable(0x0101000f)=${over.debuggable}`);
  if (over.cleartext) lines.push(`      A: ${A}:usesCleartextTraffic(0x010104ec)=${over.cleartext}`);
  lines.push(`      A: ${A}:allowBackup(0x01010280)=${over.backup ?? 'false'}`);
  return lines.join('\n');
}

function run(text: string, id = ID) {
  const r = spawnSync(process.execPath, [SCRIPT, id], { input: text, encoding: 'utf8' });
  return { code: r.status, err: r.stderr };
}

describe('check-apk-manifest', () => {
  it('passes a release manifest', () => {
    expect(run(dump()).code).toBe(0);
    expect(run(dump({ cleartext: 'false', debuggable: 'false' })).code).toBe(0);
    expect(run(dump({ cleartext: '(type 0x12)0x0', debuggable: '(type 0x12)0x0' })).code).toBe(0);
  });

  it('fails the wrong package id', () => {
    const r = run(dump({ pkg: `${ID}.dev` }));
    expect(r.code).toBe(1);
    expect(r.err).toContain('package id');
    expect(run(dump(), 'org.example.app').code).toBe(1);
  });

  it('fails cleartext traffic, in either spelling', () => {
    expect(run(dump({ cleartext: 'true' })).code).toBe(1);
    expect(run(dump({ cleartext: '(type 0x12)0xffffffff' })).err).toContain('cleartext');
  });

  it('fails a debuggable build, in either spelling', () => {
    expect(run(dump({ debuggable: 'true' })).code).toBe(1);
    expect(run(dump({ debuggable: '(type 0x12)0xffffffff' })).err).toContain('debuggable');
  });

  it('fails when backup is allowed or not stated, and on an empty dump', () => {
    expect(run(dump({ backup: 'true' })).code).toBe(1);
    expect(run('').code).toBe(1);
  });
});
