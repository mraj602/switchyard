// scripts/check-no-google.mjs (phone.md P13): the Gradle line filter and the dex reader. The
// script is an ES module, so each case runs in a child `node` that imports it.
import { execFileSync } from 'node:child_process';
import * as path from 'node:path';

const script = path.join(__dirname, '..', 'scripts', 'check-no-google.mjs');

function run<T>(body: string): T {
  const code = `import * as m from ${JSON.stringify(script)};\nconst out = await (async () => { ${body} })();\nprocess.stdout.write(JSON.stringify(out));`;
  return JSON.parse(execFileSync(process.execPath, ['--input-type=module', '-e', code], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })) as T;
}

/** A minimal dex file defining the given classes (header, string_ids, type_ids, class_defs, strings). */
const DEX = `
const dex = (names) => {
  const n = names.length;
  const sIds = 0x70, tIds = sIds + 4 * n, cDefs = tIds + 4 * n, data = cDefs + 32 * n;
  const strs = names.map((s) => Buffer.concat([Buffer.from([s.length]), Buffer.from(s, 'latin1'), Buffer.from([0])]));
  const buf = Buffer.alloc(data + strs.reduce((a, b) => a + b.length, 0));
  buf.write('dex\\n035\\0', 0, 'latin1');
  buf.writeUInt32LE(0x70, 0x24);
  buf.writeUInt32LE(n, 0x38); buf.writeUInt32LE(sIds, 0x3c);
  buf.writeUInt32LE(n, 0x40); buf.writeUInt32LE(tIds, 0x44);
  buf.writeUInt32LE(n, 0x60); buf.writeUInt32LE(cDefs, 0x64);
  let o = data;
  strs.forEach((s, i) => {
    buf.writeUInt32LE(o, sIds + 4 * i);
    buf.writeUInt32LE(i, tIds + 4 * i);
    buf.writeUInt32LE(i, cDefs + 32 * i);
    s.copy(buf, o); o += s.length;
  });
  return buf;
};`;

describe('check-no-google', () => {
  it('flags shipped Firebase, Play services and ML Kit lines, and not test, debug or comment lines', () => {
    const gradle = [
      "  implementation 'com.google.firebase:firebase-messaging:25.0.1'",
      '  add(barcodeDependencyConfiguration, "com.google.android.gms:play-services-code-scanner:16.1.0")',
      '  api "com.google.mlkit:barcode-scanning:17.3.0"',
      "  implementation 'com.android.installreferrer:installreferrer:2.2'",
      '  debugOnly "com.google.android.gms:play-services-code-scanner:16.1.0"',
      '  testImplementation "com.google.firebase:firebase-messaging:25.0.1"',
      '  androidTestImplementation "com.google.mlkit:barcode-scanning:17.3.0"',
      "  // implementation 'com.google.firebase:firebase-messaging:25.0.1'",
      "  implementation 'com.google.zxing:core:3.4.1'",
      "  implementation 'com.google.crypto.tink:tink-android:1.15.0'",
      "  implementation 'com.journeyapps:zxing-android-embedded:4.3.0'",
    ].join('\n');
    const hits = run<{ line: number }[]>(`return m.forbiddenGradleLines(${JSON.stringify(gradle)});`);
    expect(hits.map((h) => h.line)).toEqual([1, 2, 3, 4]);
  });

  it('reads the classes a dex file defines', () => {
    const classes = run<string[]>(`${DEX}\nreturn m.dexClasses(dex(['Lcom/example/A;', 'Lcom/google/firebase/messaging/B;']));`);
    expect(classes).toEqual(['Lcom/example/A;', 'Lcom/google/firebase/messaging/B;']);
  });

  it('refuses a file that is not dex', () => {
    expect(() => run(`return m.dexClasses(Buffer.from('PK\\u0003\\u0004'));`)).toThrow();
  });
});
