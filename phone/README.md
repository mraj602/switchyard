# Watchyard phone app (`phone/`)

The hub's screens (Now, Sessions, Session, Accounts, More) as an Expo / React Native app, native
notifications with **Open**, **Snooze 15 min** and **Seen**, and home-screen widgets. It is a
`phone` device on your hub (scopes `read` and `push`): it reads, snoozes and marks seen, nothing
else. Unofficial; not affiliated with Anthropic.

**Android only.** There is no iOS release. Some iOS source (widgets, Live Activity adapters) stays
in the tree and is kept compiling by the checks, but nothing here builds, signs or ships an iOS
app, and no iOS feature is promised. Android 12 (API 31) or newer.

Specs: `docs/spec/phone.md` (the app, P1 onwards) and `docs/spec/push.md` sections 13 and 14 (the
link).

**Nothing in this repository signs for a store or uploads anything.** Your release keystore, if
you make release builds, stays outside the repository (see "Release builds" below).
`scripts/check-no-secrets.mjs` fails the check if a credential file appears.

## Prerequisites

- Node 22.13 or newer (and npm).
- A JDK, 17 or newer (21 works).
- The Android SDK (command-line tools are enough) with `ANDROID_HOME` pointing at it, and the
  platform and build-tools that Gradle asks for (it downloads what it can accept the licenses
  for: run `sdkmanager --licenses` once).
- Expo Go does not have the `sy-link` module, so use a development or debug build, not Expo Go.

## Build an unsigned debug APK (contributors)

```sh
cd phone
npm ci
npm run check                 # tsc, eslint, jest (offline), the secrets check
npx expo prebuild --platform android --no-install --clean   # writes the untracked phone/android/
```

Then, from the repository root:

```sh
make phone-android-debug      # prebuild + ./gradlew :app:assembleDebug
```

The APK lands in `phone/android/app/build/outputs/apk/debug/`. It is signed with the debug key
that the generated project carries, which is fine for your own phone and never for distribution.
`WY_APP_VARIANT=development` (set by that make target) lets the pair screen accept `http://`
hubs on loopback and LAN addresses; leave it unset for a build that only accepts `https://`.

JVM unit tests of the native modules (what CI runs after the prebuild):

```sh
cd phone/android && ./gradlew :sy-link:testDebugUnitTest :sy-tabs:testDebugUnitTest
```

### A self-contained APK for emulator tests

`phone/scripts/e2e-android-apk.sh` builds the Gradle release variant (JavaScript bundled in, no
Metro) signed with the debug key and with cleartext allowed, for `test/e2e/android/run.sh`:

```sh
WY_E2E_GRADLE_ARGS=-PreactNativeArchitectures=arm64-v8a WY_E2E_APK=/tmp/app-e2e.apk \
  phone/scripts/e2e-android-apk.sh
```

By default it builds x86_64 only (for the emulator). Do not distribute it.

## Pair the app with a hub

1. Run a hub (see the repository README) that your phone can reach over `https://` (for example
   a Tailscale hostname).
2. In the hub's web board open Settings → Phones → **Add phone** (or run `wy phone pair` on the
   hub). It shows a QR code and a six-digit code.
3. In the app, scan the QR code, or type the hub's address and the code. The QR also carries the
   hub's key fingerprint, so the phone can check it is talking to the right hub; typing the code
   works but the hub key is then trusted on first use (Settings → Notifications says which).
4. Allow notifications when Android asks (Android 13 and newer), then open Settings →
   Notifications to check the connection, the battery guide for your phone's maker and "Send
   test notification".

To try the app against the mock hub: `go run ./cmd/webui-dev` (repository root, loopback only),
build with `WY_APP_VARIANT=development`, and pair by typing its address and the code it prints.
From an Android emulator the host is `10.0.2.2`.

## Release builds (the maintainer)

`make phone-apk-release` runs `expo prebuild` and Gradle `assembleRelease` and signs with your
own keystore. It refuses to start without one, and nothing it needs is ever written into the
repository (`*.jks`, `*.keystore` and friends are git-ignored; the build also refuses a keystore
or properties file inside the repository). Signing is injected by
`scripts/sy-release-signing.init.gradle` from the environment, because `expo prebuild`
regenerates `android/` each time.

Create the keystore once, outside the repository (keep two offline copies: losing it means every
phone must uninstall and reinstall):

```sh
keytool -genkeypair -v -keystore ~/secrets/watchyard-release.jks -alias watchyard \
  -keyalg RSA -keysize 4096 -validity 10000
```

Then give the build the keystore, in your shell:

```sh
export WY_ANDROID_KEYSTORE=~/secrets/watchyard-release.jks
export WY_ANDROID_KEY_ALIAS=watchyard
read -rs WY_ANDROID_KEYSTORE_PASSWORD && export WY_ANDROID_KEYSTORE_PASSWORD   # no echo, not in history
# WY_ANDROID_KEY_PASSWORD too, only when the key password differs
make phone-apk-release
```

or as a properties file outside the repo (`storeFile`, `keyAlias`, `storePassword`,
`keyPassword`) named by `WY_ANDROID_SIGNING_PROPERTIES`; an environment variable wins over it.
The passwords go to Gradle and keytool through the environment only and are never printed.

Output in `phone/dist/`: `watchyard-<version>.apk`, `SHA256SUMS` and `manifest.json` (version,
versionCode as `build`, size, sha256, the signing certificate's SHA-256 as `cert_sha256`, min
sdk). Publish the three files as a GitHub release tagged `phone-vX.Y.Z` (the hub mirrors it
daily, `docs/spec/phone.md` P16.2; the app installs an update only after checking the checksum
and that the signing key is the one it is already signed with). `WY_RELEASE_NOTES` fills the
manifest's `notes`. The Android `versionCode` is `MAJOR*10000 + MINOR*100 + PATCH` of the app
version, so bump `version` in `app.config.ts` and `package.json` together for each release.

A fork that publishes its own releases changes `DEFAULT_REPO` in
`modules/sy-link/android/src/main/java/dev/watchyard/link/UpdateClient.kt` (and the hub's
`version.Repo`).

## Environment (read by `app.config.ts`; none of them is a secret)

| Variable | Default | Effect |
| --- | --- | --- |
| `WY_APP_ID` | `io.github.mraj602.watchyard` (`.dev` for a development build) | Android package. Forks that distribute their own build use their own reverse-DNS id |
| `WY_APP_VARIANT` | release | `development` allows cleartext `http://` to loopback and LAN hubs; anything else is a release configuration |

## Layout

`app/` (expo-router routes, thin), `src/contract/` (TypeScript mirrors of the hub's JSON),
`src/lib/` (decisions, pure and tested; no React Native or Expo import), `src/state/` (keychain,
client, poller, cache; `glue.tsx` binds notifications and widgets to it, with the logic in
`src/lib/integrate/`), `src/components/` (screens and UI), `widgets/` (Android widgets with
`react-native-android-widget`; iOS widget sources are kept compiling only), `modules/sy-link`
(the push connection and updater, Kotlin), `modules/sy-tabs` (claude.ai account tabs, Kotlin),
`plugins/` (config plugins).

`android/` and `ios/` are generated by `expo prebuild`, git-ignored, and never edited by hand:
every native change is a config plugin or a library option in `app.config.ts`.

## When pushes stop

Open Settings → Notifications. It shows the link's state and last error in words (hub
unreachable, key changed, Android blocked a background start), the battery guide for your
phone's maker, and "Send test notification". The hub's board shows the phone's link target and
whether it is connected.

## Privacy

Notifications travel over the app's own encrypted connection to your hub: no third party sees
them, and the app uses no Firebase project or Google push service for delivery. Turn on
**Private pushes** in the app's Settings (or the hub default) and pushes say only "1 session needs
you"; the details load from your hub when you open them. The device token stays in the Android
Keystore-backed secure store and never appears in logs, widgets, pushes or links.

In a claude.ai account tab, Watchyard asks claude.ai only "which account is this?" (the account
id, as the desktop app does) so Accounts can say whether the tab matches its account. It never
reads your chats, the page or the tab's cookies, and sends nothing from the tab to your hub; what
it remembers (signed in or not, the account id, when) stays on the phone (`docs/spec/phone.md`
P17.7).
