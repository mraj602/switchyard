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

describe('stripFirebase (self-hosted profile, P13)', () => {
  const manifest = () => ({
    manifest: {
      $: {},
      application: [
        {
          $: {},
          'meta-data': [
            { $: { 'android:name': 'com.google.firebase.messaging.default_notification_color', 'android:resource': '@color/x' } },
            { $: { 'android:name': 'expo.modules.updates.ENABLED', 'android:value': 'false' } },
          ],
          service: [{ $: { 'android:name': 'dev.switchyard.link.SyLinkService' } }],
        },
      ],
    },
  });

  it('drops the com.google.firebase meta-data and removes the FCM service from the merge', () => {
    const out = plugin.stripFirebase(manifest());
    const app = out.manifest.application[0];
    expect(app['meta-data'].map((m: { $: Record<string, string> }) => m.$['android:name'])).toEqual(['expo.modules.updates.ENABLED']);
    expect(app.service).toContainEqual({ $: { 'android:name': plugin.FCM_SERVICE, 'tools:node': 'remove' } });
    expect(out.manifest.$['xmlns:tools']).toBe('http://schemas.android.com/tools');
    expect(JSON.stringify(out)).not.toContain('com.google.firebase');
  });

  it('is idempotent', () => {
    const once = JSON.stringify(plugin.stripFirebase(manifest()));
    expect(JSON.stringify(plugin.stripFirebase(JSON.parse(once)))).toBe(once);
  });
});
