#!/usr/bin/env bash
# Test-only: build an APK that runs WITHOUT Metro (JS bundled in) and may talk cleartext HTTP to
# a local hub, for test/e2e/android/run.sh. Output: $WY_E2E_APK (default phone/dist/app-e2e.apk).
#
# It is the Gradle *release* variant (the debug variant carries expo-dev-client, whose launcher
# asks for a Metro server instead of starting the bundled JS), signed with the debug key the
# generated project already uses, prebuilt with WY_APP_VARIANT=development: cleartext allowed in
# the manifest and extra.cleartext, so the pair screen accepts an http:// hub on loopback / LAN /
# 10.0.2.2 (phone.md P2). Nothing is committed: it prebuilds into the untracked phone/android.
#
# Env: ANDROID_HOME (default ~/Android/Sdk), WY_E2E_APK, WY_E2E_GRADLE_ARGS (extra Gradle args;
#      the default builds x86_64 only, for the emulator: ~6 min instead of ~15; set
#      WY_E2E_GRADLE_ARGS="" for all ABIs), WY_SKIP_PREBUILD=1 (reuse phone/android),
#      WY_APP_ID (default io.github.mraj602.watchyard.dev, the development build's id).
# Never a store build, never uploads. Gradle runs --no-daemon with -Xmx2g.
set -euo pipefail
phone="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$phone"
export ANDROID_HOME="${ANDROID_HOME:-$HOME/Android/Sdk}"
export ANDROID_SDK_ROOT="$ANDROID_HOME"
[ -d "$ANDROID_HOME" ] || { echo "e2e-android-apk: no Android SDK at $ANDROID_HOME" >&2; exit 1; }
[ -d node_modules ] || npm ci
export WY_APP_VARIANT=development CI=1 EXPO_NO_TELEMETRY=1
if [ -z "${WY_SKIP_PREBUILD:-}" ]; then
  npx expo prebuild --platform android --no-install --clean
fi
(cd android && ./gradlew --no-daemon -Dorg.gradle.jvmargs=-Xmx2g ${WY_E2E_GRADLE_ARGS--PreactNativeArchitectures=x86_64} :app:assembleRelease)
out="${WY_E2E_APK:-$phone/dist/app-e2e.apk}"
mkdir -p "$(dirname "$out")"
cp android/app/build/outputs/apk/release/app-release.apk "$out"
echo "e2e-android-apk: $out"
