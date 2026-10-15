// Expo app config (docs/spec/phone.md P13). The self-hosted build: pushes arrive over the app's
// own link to the hub (push.md 13-14), with no Firebase, no Google push service and no EAS
// project. Reads only non-secret environment values:
//   SY_APP_ID                bundle id and package name (default dev.switchyard.app.local)
//   SY_APP_VARIANT           "development" allows cleartext traffic (loopback hubs, P2)
// Never put a credential here (phone.md P13).
import type { ConfigContext, ExpoConfig } from 'expo/config';
import { existsSync } from 'node:fs';
import { join } from 'node:path';

const APP_ID = process.env.SY_APP_ID || 'dev.switchyard.app.local';
const GROUP_ID = `group.${APP_ID}`;
const VERSION = '0.8.0';
const DEV = process.env.SY_APP_VARIANT === 'development';

/** Android versionCode (the updater's `build`, P16.1): MAJOR*10000 + MINOR*100 + PATCH of VERSION, so it only rises with the version. */
export const VERSION_CODE = (() => {
  const [major = 0, minor = 0, patch = 0] = VERSION.split('.').map(Number);
  return major * 10000 + minor * 100 + patch;
})();

// Assets are added by the screens WP (phone/assets/**); the config works without them.
const asset = (rel: string): string | undefined =>
  existsSync(join(__dirname, rel)) ? `./${rel}` : undefined;

/** Widgets of P11, shared by expo-widgets (iOS) and react-native-android-widget (Android). */
export const IOS_WIDGETS = [
  {
    name: 'NeedsYouSmall',
    displayName: 'Needs you',
    description: 'Sessions that need you and the next limit reset.',
    supportedFamilies: ['systemSmall'],
    contentMarginsDisabled: false,
    ios: { supportedFamilies: ['systemSmall'] },
    android: null,
  },
  {
    name: 'AccountsMedium',
    displayName: 'Accounts',
    description: 'Usage rings and reset times of up to four accounts.',
    supportedFamilies: ['systemMedium'],
    contentMarginsDisabled: false,
    ios: { supportedFamilies: ['systemMedium'] },
    android: null,
  },
] as const;

export const ANDROID_WIDGETS = [
  {
    name: 'SyNeedsYou',
    label: 'Switchyard: needs you',
    description: 'Sessions that need you and the next limit reset',
    minWidth: '110dp',
    minHeight: '110dp',
    targetCellWidth: 2,
    targetCellHeight: 2,
    updatePeriodMillis: 1800000,
    ...(asset('assets/widget-previews/sy-needs-you.png') ? { previewImage: './assets/widget-previews/sy-needs-you.png' } : {}),
  },
  {
    name: 'SyAccounts',
    label: 'Switchyard: accounts',
    description: 'Usage and reset times of up to four accounts',
    minWidth: '250dp',
    minHeight: '110dp',
    targetCellWidth: 4,
    targetCellHeight: 2,
    updatePeriodMillis: 1800000,
    ...(asset('assets/widget-previews/sy-accounts.png') ? { previewImage: './assets/widget-previews/sy-accounts.png' } : {}),
  },
] as const;

export default ({ config }: ConfigContext): ExpoConfig => {
  const notificationIcon = asset('assets/notification-icon.png');
  const icon = asset('assets/icon.png');

  return {
    ...config,
    name: 'Switchyard',
    slug: 'switchyard',
    version: VERSION,
    scheme: 'switchyard',
    orientation: 'portrait',
    userInterfaceStyle: 'automatic',
    ...(icon ? { icon } : {}),
    ios: {
      bundleIdentifier: APP_ID,
      deploymentTarget: '16.4',
      supportsTablet: false,
      infoPlist: { ITSAppUsesNonExemptEncryption: false },
    },
    android: {
      package: APP_ID,
      versionCode: VERSION_CODE,
      // Never back up the app's data (the keychain token, P3); with-sy-native.js also sets it.
      allowBackup: false,
      // Listed explicitly so the prebuilt manifest shows them (expo-notifications would also
      // merge POST_NOTIFICATIONS at Gradle time); nothing else is needed (P13).
      permissions: ['android.permission.POST_NOTIFICATIONS', 'android.permission.CAMERA'],
      blockedPermissions: [
        'android.permission.RECORD_AUDIO',
        'android.permission.SYSTEM_ALERT_WINDOW',
        'android.permission.READ_EXTERNAL_STORAGE',
        'android.permission.WRITE_EXTERNAL_STORAGE',
      ],
    },
    extra: {
      appGroup: GROUP_ID,
      // SY_APP_VARIANT=development: the pair screen also accepts http:// loopback / LAN hubs in a
      // bundled (non-__DEV__) build, matching usesCleartextTraffic above (phone.md P2).
      cleartext: DEV,
    },
    plugins: [
      'expo-router',
      // Before expo-notifications: mods run in reverse order, so this one sees its FCM meta-data.
      './plugins/with-sy-no-firebase.js',
      ['expo-notifications', { color: '#0E7C76', ...(notificationIcon ? { icon: notificationIcon } : {}) }],
      ['expo-secure-store', { faceIDPermission: false, configureAndroidBackup: false }],
      [
        'expo-camera',
        {
          cameraPermission: 'Scan the pairing QR code from your hub',
          microphonePermission: false,
          recordAudioAndroid: false,
          barcodeScannerEnabled: true,
        },
      ],
      [
        'expo-build-properties',
        {
          // 31: Keystore key agreement (push.md 14.2).
          android: { minSdkVersion: 31, usesCleartextTraffic: DEV },
        },
      ],
      [
        'expo-widgets',
        {
          groupIdentifier: GROUP_ID,
          // The widget extension's bundle id (register it with Apple next to SY_APP_ID).
          bundleIdentifier: `${APP_ID}.widgets`,
          enablePushNotifications: false,
          enableAndroid: false,
          widgets: IOS_WIDGETS,
        },
      ],
      ['react-native-android-widget', { widgets: ANDROID_WIDGETS }],
      ['./plugins/with-sy-native.js', { development: DEV }],
    ],
    experiments: { typedRoutes: false },
  };
};
