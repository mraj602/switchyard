# Switchyard phone app (`phone/`)

The phase 8 store app: the hub's screens (Now, Sessions, Session, Accounts, More) as an Expo
app for iPhone and Android, native pushes with **Open**, **Snooze 15 min** and **Seen**, small
and medium home-screen widgets, and an iOS Lock Screen countdown to a limit reset. It is a
`phone` device on your hub (scopes `read` and `push`): it reads, snoozes and marks seen,
nothing else. Unofficial; not affiliated with Anthropic.

Specs: `docs/spec/phone.md` (the app, P1 to P15), `docs/spec/push.md` section 12 (the hub's
Expo channel), `docs/phases/8.md` (work split, tests, decisions, risks, manual steps).

**Nothing in this repository signs, builds for a store or uploads.** Accounts, credentials,
EAS builds, TestFlight and every check on a real phone are the manual steps below, done by
you. No credential file ever goes into the repo (`scripts/check-no-secrets.mjs` fails the
check if one appears).

## Self-hosted Android build (N1, the default)

Since N1 the Android app delivers notifications over its own connection to your hub (the
`sy-link` module in `modules/sy-link`, `docs/spec/push.md` sections 13 and 14): no Expo account,
no EAS, no Firebase project and no Google push service. `SY_PUSH` defaults to `link`; in that
profile `app.config.ts` sets no EAS project id, refuses `SY_GOOGLE_SERVICES_JSON`, sets
`minSdkVersion` 31 and drops expo-notifications' FCM entry point. `eas.json` is gone. Pair the
phone by scanning the hub's QR (its keys go in sealed), then check Settings → Notifications
(battery optimisation, the maker guide, "Verified by QR"). Build the APK yourself with
`expo prebuild` and Gradle (phone.md P13); nothing here signs or uploads it.

### Release build and updates (N2): you run it

`make phone-apk-release` does `expo prebuild` and Gradle `assembleRelease` on your laptop and
signs with **your own keystore**. It refuses to start without one, and nothing it needs is ever
written into the repo. No agent and no CI job runs it, and nothing is uploaded.

Create the keystore once, outside the repository (a secrets folder; keep two offline copies,
because losing it means every phone must uninstall and reinstall). This is the command; no
`make` target runs it for you:

```sh
keytool -genkeypair -v -keystore ~/secrets/switchyard-release.jks -alias switchyard \
  -keyalg RSA -keysize 4096 -validity 10000
```

Then give the build the keystore, either in your shell:

```sh
export SY_ANDROID_KEYSTORE=~/secrets/switchyard-release.jks
export SY_ANDROID_KEY_ALIAS=switchyard
read -rs SY_ANDROID_KEYSTORE_PASSWORD && export SY_ANDROID_KEYSTORE_PASSWORD   # no echo, not in history
# SY_ANDROID_KEY_PASSWORD too, only when the key password differs
make phone-apk-release
```

or as a properties file outside the repo (`storeFile`, `keyAlias`, `storePassword`,
`keyPassword`) named by `SY_ANDROID_SIGNING_PROPERTIES`; an environment variable wins over it.
The passwords go to Gradle and keytool through the environment only and are never printed.

Output in `phone/dist/`: `switchyard-<version>.apk`, `SHA256SUMS` and `manifest.json` (version,
versionCode as `build`, size, sha256, the signing certificate's SHA-256 as `cert_sha256`, min
sdk). The certificate fingerprint is printed too: publish it with the release, and compare it
after a build with `keytool -list -v -keystore <your keystore>`. Then, by hand, publish the three
files as a GitHub release tagged `phone-vX.Y.Z` (the hub mirrors it daily, `docs/spec/phone.md`
P16.2; the app installs it after checking the checksum and that the signing key is yours).
`SY_RELEASE_NOTES` fills the manifest's `notes`. The Android `versionCode` is
`MAJOR*10000 + MINOR*100 + PATCH` of the app version, so bump `version` in `app.config.ts` and
`package.json` for each release.

The steps below are the phase 8 Expo profile (`SY_PUSH=expo`) and the iPhone.

## What you need, in order (Expo profile)

1. **An Expo account.** In `phone/`: `npx eas-cli@24.8.0 login`, then
   `npx eas-cli@24.8.0 init`, which creates the EAS project. Put its id in `EAS_PROJECT_ID`
   (a build without it can show the hub but cannot receive pushes: Settings says "This
   build can't receive pushes").
2. **Apple Developer Program** (USD 99 a year), for the iPhone. Register the bundle id
   (`SY_APP_ID`) with the Push Notifications and App Groups capabilities, the App Group
   `group.<SY_APP_ID>`, and the widget extension's id `<SY_APP_ID>.widgets` with the same
   App Group. `eas build` can create these for you when you let it manage credentials.
3. **Firebase**, for Android pushes: a Firebase project with an Android app for `SY_APP_ID`.
   Download `google-services.json` **outside this repository** and point
   `SY_GOOGLE_SERVICES_JSON` at it (the config refuses a path inside `phone/`).
4. Optional: a Google Play developer account (USD 25), only for Play internal testing. The
   sideloaded APK needs none.
5. **Push credentials, once.** They live in EAS, never on the hub and never in git:
   - APNs: let `eas credentials` create and manage the key, or upload your own `.p8`.
   - FCM v1: create a service-account key in Firebase, upload it with `eas credentials` →
     Android → FCM V1, then delete the local JSON.
6. **The hub**: deploy the phase 8 hub (schema v6 is applied at start) and allow outbound
   HTTPS to `exp.host` (`deploy/README.md`). Then follow "Device checks" in
   `docs/phases/8.md`.

## Environment (read by `app.config.ts`; none of them is a secret)

| Variable | Default | Effect |
| --- | --- | --- |
| `SY_PUSH` | `link` | `link`: the self-hosted Android build (no Expo token, no Firebase, minSdk 31); `expo`: the phase 8 Expo/FCM path |
| `SY_APP_ID` | `dev.switchyard.app.local` | Bundle id and Android package. Use your own reverse-DNS id; the App Group is `group.<SY_APP_ID>`, the widget extension `<SY_APP_ID>.widgets` |
| `EAS_PROJECT_ID` | unset | `SY_PUSH=expo` only: `extra.eas.projectId`, the Expo push token is issued for this project |
| `SY_GOOGLE_SERVICES_JSON` | unset | `SY_PUSH=expo` only (refused with `link`): path (outside `phone/`) to `google-services.json`; omitted when unset |
| `SY_APP_VARIANT` | release | `development` allows cleartext `http://` to loopback and LAN hubs (`--insecure-lan` testing); anything else is a release config |

## Develop

Node 22.13 or newer. Expo Go cannot receive remote pushes on Android, so use a development
build.

```
cd phone
npm ci
npm run check            # tsc, eslint, jest (offline), the secrets check
npm start                # Metro for a development build (expo start --dev-client)
```

Or from the repo root: `make phone-deps`, `make phone-check` (adds both prebuild checks,
which run `expo prebuild` into a temporary copy) and `make phone-android-debug` (optional:
an unsigned debug compile with `ANDROID_HOME` and JDK 17+; the APK is for your own phone,
never distributed).

To try the app against the mock hub: `go run ./cmd/webui-dev` (from the repo root, it listens
on loopback), build with `SY_APP_VARIANT=development`, and pair by typing its address and the
code it prints.

`ios/` and `android/` are generated by `expo prebuild` (continuous native generation). They
are git-ignored and never edited by hand: every native change is a config plugin
(`plugins/with-sy-native.js`) or a library option in `app.config.ts`.

Layout (phone.md P1): `app/` (expo-router routes, thin), `src/contract/` (TypeScript mirrors
of the hub's JSON), `src/lib/` (all decisions, pure and tested; no React Native or Expo
import), `src/state/` (the app state: keychain, client, poller, cache; `glue.tsx` binds
notifications, widgets and the Live Activity to it, with the logic in `src/lib/integrate/`), `src/components/` (screens and UI),
`src/tasks/` (the background notification task), `widgets/` (iOS `expo-widgets` and Android
`react-native-android-widget` components).

## Builds (you, with EAS)

These run on Expo's build servers with your credentials. Agents never run them.

```
npx eas-cli@24.8.0 build --profile development --platform all     # development builds
npx eas-cli@24.8.0 build -p android --profile preview             # an APK to sideload
npx eas-cli@24.8.0 build -p ios --profile production              # then:
npx eas-cli@24.8.0 submit -p ios                                  # TestFlight (internal testers need no review)
```

On a Mac you can also build locally: `npx expo run:ios` / `npx expo run:android`.

For the APK: install it on the phone and allow installs from unknown sources for the app
you open it with.

## Expo's enhanced push security (recommended)

Without it, anyone who learns a phone's Expo push token can send it a push through your EAS
project. To turn it on:

1. EAS dashboard → your project → Credentials (or Settings) → turn on **Enhanced security
   for push notifications**.
2. Create an access token (account settings → Access tokens).
3. Paste it into the board: Settings → Channels → App → Access token. The hub keeps it as a
   secret, never shows it again (only "set"), and sends it as a bearer token to Expo.

`sy doctor` on the hub shows the App channel, its live targets, whether a token is set, the
last receipt error, and a warning when push tickets older than an hour have no receipt.

## When pushes stop

| Symptom (Settings test push, `sy doctor`, board) | Fix |
| --- | --- |
| "Expo has no push credentials for this app (see phone/README)" (`InvalidCredentials`) | Upload the APNs key / FCM v1 service account again with `eas credentials`; check the bundle id matches `SY_APP_ID` |
| "Expo access token missing or wrong" | Enhanced security is on: paste a valid access token in the board (above) |
| The app's target shows **gone** | The phone uninstalled the app or the token changed (`DeviceNotRegistered`); open the app, it registers again |
| Tickets without receipts (`sy doctor`) | The hub cannot reach `exp.host`; check the egress policy |

## Privacy

Push text goes through Expo, Apple and Google, who can read it (push.md 5.5). Turn on
**Private pushes** in the app's Settings (or the hub default) and pushes say only "1 session
needs you"; the details load from your hub when you open them. The device token stays in the
OS keychain and never appears in logs, widgets, pushes or links.
