// Expo app config (docs/spec/phone.md P13). Reads only non-secret environment values:
//   SY_APP_ID                bundle id and package name (default dev.switchyard.app.local)
//   EAS_PROJECT_ID           extra.eas.projectId (no push token without it, P6.3)
//   SY_GOOGLE_SERVICES_JSON  path OUTSIDE the repository to google-services.json (optional)
//   SY_APP_VARIANT           "development" allows cleartext traffic (loopback hubs, P2)
// Never put a credential here: APNs and FCM credentials live in EAS (phone.md P13).
import type { ConfigContext, ExpoConfig } from 'expo/config';
import { existsSync } from 'node:fs';
import { join, resolve } from 'node:path';

const APP_ID = process.env.SY_APP_ID || 'dev.switchyard.app.local';
const GROUP_ID = `group.${APP_ID}`;
const VERSION = '0.8.0';
const DEV = process.env.SY_APP_VARIANT === 'development';

// Assets are added by the screens WP (phone/assets/**); the config works without them.
const asset = (rel: string): string | undefined =>
  existsSync(join(__dirname, rel)) ? `./${rel}` : undefined;

function googleServicesFile(): string | undefined {
  const p = process.env.SY_GOOGLE_SERVICES_JSON;
  if (!p) return undefined;
  const abs = resolve(p);
  // The file must live outside the repository (hard rule 1); check-no-secrets.mjs also refuses it.
  if (abs.startsWith(resolve(__dirname) + '/')) {
    throw new Error('SY_GOOGLE_SERVICES_JSON must point outside phone/ (never commit google-services.json)');
  }
  return abs;
}

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
  const projectId = process.env.EAS_PROJECT_ID || undefined;
  const googleServices = googleServicesFile();

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
      ...(googleServices ? { googleServicesFile: googleServices } : {}),
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
      ...(projectId ? { eas: { projectId } } : {}),
      appGroup: GROUP_ID,
    },
    plugins: [
      'expo-router',
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
          android: { minSdkVersion: 24, usesCleartextTraffic: DEV },
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
