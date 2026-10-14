// The Android build alignment in plugins/with-sy-native.js (docs/spec/phone.md P13).
// eslint-disable-next-line @typescript-eslint/no-require-imports
const plugin = require('../plugins/with-sy-native');

describe('alignWorkVersions', () => {
  const base = "apply plugin: 'com.android.application'\n";

  it('forces the whole androidx.work group to one version', () => {
    const out: string = plugin.alignWorkVersions(base);
    expect(out.startsWith(base)).toBe(true);
    expect(out).toContain("details.requested.group == 'androidx.work'");
    expect(out).toContain(`details.useVersion '${plugin.WORK_VERSION}'`);
  });

  it('is idempotent', () => {
    const once = plugin.alignWorkVersions(base);
    expect(plugin.alignWorkVersions(once)).toBe(once);
  });

  it('pins at least 2.8, where work-runtime-ktx classes were merged into work-runtime', () => {
    const [major, minor] = plugin.WORK_VERSION.split('.').map(Number);
    expect(major > 2 || (major === 2 && minor >= 8)).toBe(true);
  });
});
