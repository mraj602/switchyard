// The Android build alignment in plugins/with-wy-native.js (docs/spec/phone.md P13).
// eslint-disable-next-line @typescript-eslint/no-require-imports
const plugin = require('../plugins/with-wy-native');

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

describe('brandSplash (the Android splash)', () => {
  const styles = () => ({
    resources: {
      style: [
        { $: { name: 'AppTheme', parent: 'Theme.AppCompat.DayNight.NoActionBar' }, item: [] },
        { $: { name: 'Theme.App.SplashScreen', parent: 'AppTheme' }, item: [{ _: '@drawable/splashscreen_logo', $: { name: 'android:windowBackground' } }] },
      ],
    },
  });
  const items = (out: { resources: { style: { $: { name: string }; item: { _: string; $: { name: string } }[] }[] } }) =>
    Object.fromEntries(out.resources.style.find((x) => x.$.name === 'Theme.App.SplashScreen')!.item.map((i) => [i.$.name, i._]));

  it('gives the splash theme the ink background and the adaptive foreground as its icon', () => {
    expect(items(plugin.brandSplash(styles()))).toEqual(plugin.SPLASH_ITEMS);
    expect(plugin.SPLASH_ITEMS['android:windowSplashScreenAnimatedIcon']).toBe('@mipmap/ic_launcher_foreground');
  });

  it("paints AppTheme's window with the ink too, so no white frame follows the splash", () => {
    const out = plugin.brandSplash(styles());
    const app = out.resources.style.find((x: { $: { name: string } }) => x.$.name === 'AppTheme');
    expect(app.item).toContainEqual({ _: '@color/splashscreen_background', $: { name: 'android:windowBackground' } });
  });

  it('adds the style when the template has none, and is idempotent', () => {
    const fresh = plugin.brandSplash({ resources: {} });
    expect(items(fresh)).toEqual(plugin.SPLASH_ITEMS);
    const once = JSON.stringify(plugin.brandSplash(styles()));
    expect(JSON.stringify(plugin.brandSplash(JSON.parse(once)))).toBe(once);
  });

  it('sets splashscreen_background to the brand ink, replacing the template white', () => {
    const out = plugin.setColor({ resources: { color: [{ _: '#FFFFFF', $: { name: 'splashscreen_background' } }] } }, 'splashscreen_background', plugin.BRAND_INK);
    expect(out.resources.color).toEqual([{ _: '#17191B', $: { name: 'splashscreen_background' } }]);
  });
});

describe('backup rules', () => {
  it('exclude every domain from cloud backup and device transfer', () => {
    const rules: string = plugin.dataExtractionRulesXml();
    const cloud = rules.slice(rules.indexOf('<cloud-backup>'), rules.indexOf('</cloud-backup>'));
    const transfer = rules.slice(rules.indexOf('<device-transfer>'), rules.indexOf('</device-transfer>'));
    for (const section of [cloud, transfer]) {
      for (const d of plugin.BACKUP_DOMAINS) expect(section).toContain(`<exclude domain="${d}" path="." />`);
      expect(section).not.toContain('<include');
    }
    const legacy: string = plugin.fullBackupContentXml();
    for (const d of plugin.BACKUP_DOMAINS) expect(legacy).toContain(`<exclude domain="${d}" path="." />`);
    expect(legacy).not.toContain('<include');
  });

  it('cover the domains Android defines', () => {
    expect([...plugin.BACKUP_DOMAINS].sort()).toEqual(['database', 'device_file', 'device_root', 'external', 'file', 'root', 'sharedpref']);
  });
});

describe('widget providers open links with an explicit intent', () => {
  const generated = 'package io.github.mraj602.watchyard.widget;\n\nimport com.reactnativeandroidwidget.RNWidgetProvider;\n\npublic class WyNeeds extends RNWidgetProvider {\n}\n';

  it('hands the tap to WidgetOpen before the library sees it', () => {
    const out: string = plugin.patchWidgetProvider(generated);
    expect(out).toContain('import dev.watchyard.link.WidgetOpen;');
    expect(out).toContain('public class WyNeeds extends RNWidgetProvider {');
    expect(out.indexOf('WidgetOpen.handle(context, intent)')).toBeLessThan(out.indexOf('super.onReceive(context, intent)'));
  });

  it('is idempotent and refuses a provider it does not know', () => {
    const once = plugin.patchWidgetProvider(generated);
    expect(plugin.patchWidgetProvider(once)).toBe(once);
    expect(plugin.patchWidgetProvider('public class X extends RNWidgetProvider { int a; }')).toBeNull();
  });
});
