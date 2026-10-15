#!/usr/bin/env bash
# make phone-apk-release: the self-hosted Android release build (docs/spec/phone.md P13, P16.1).
#
# You run it, on your laptop; no agent does. It signs with your own keystore, which stays
# outside the repository. Nothing is uploaded, tagged or installed.
#
# Signing input, from your shell or from a properties file outside the repo
# (SY_ANDROID_SIGNING_PROPERTIES=/path/outside/repo/sy-signing.properties with the keys
# storeFile, keyAlias, storePassword, keyPassword); an environment variable wins:
#   SY_ANDROID_KEYSTORE  SY_ANDROID_KEY_ALIAS  SY_ANDROID_KEYSTORE_PASSWORD  [SY_ANDROID_KEY_PASSWORD]
# Without them the build refuses to start. Passwords are passed to Gradle and keytool through
# the environment only and are never printed.
#
# Output in phone/dist/: switchyard-<version>.apk, SHA256SUMS, manifest.json.
# Optional: SY_RELEASE_NOTES (the manifest's notes), SY_GRADLE_ARGS (extra Gradle arguments),
# SY_SKIP_PREBUILD=1 (reuse phone/android, for a repeat after a Gradle failure).
set -euo pipefail
set +x

phone="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
repo="$(cd "$phone/.." && pwd -P)"
cd "$phone"

die() { echo "phone-apk-release: $*" >&2; exit 1; }

# --- signing input -----------------------------------------------------------------------
prop() { # prop <file> <key>: the value after the first '=' of the last matching line
  local line
  line="$(grep -E "^[[:space:]]*$2[[:space:]]*=" "$1" | tail -n 1 || true)"
  line="${line#*=}"
  line="${line#"${line%%[![:space:]]*}"}"
  printf '%s' "${line%"${line##*[![:space:]]}"}"
}
if [ -n "${SY_ANDROID_SIGNING_PROPERTIES:-}" ]; then
  pf="$SY_ANDROID_SIGNING_PROPERTIES"
  [ -f "$pf" ] || die "SY_ANDROID_SIGNING_PROPERTIES is not a file"
  case "$(realpath "$pf")" in "$repo"/*) die "the properties file is inside the repository; keep it in your secrets folder" ;; esac
  : "${SY_ANDROID_KEYSTORE:=$(prop "$pf" storeFile)}"
  : "${SY_ANDROID_KEY_ALIAS:=$(prop "$pf" keyAlias)}"
  : "${SY_ANDROID_KEYSTORE_PASSWORD:=$(prop "$pf" storePassword)}"
  : "${SY_ANDROID_KEY_PASSWORD:=$(prop "$pf" keyPassword)}"
fi
[ -n "${SY_ANDROID_KEYSTORE:-}" ] || die "set SY_ANDROID_KEYSTORE (the path of your release keystore, outside the repo); see phone/README.md"
[ -n "${SY_ANDROID_KEY_ALIAS:-}" ] || die "set SY_ANDROID_KEY_ALIAS"
[ -n "${SY_ANDROID_KEYSTORE_PASSWORD:-}" ] || die "set SY_ANDROID_KEYSTORE_PASSWORD (or use SY_ANDROID_SIGNING_PROPERTIES)"
[ -f "$SY_ANDROID_KEYSTORE" ] || die "SY_ANDROID_KEYSTORE is not a file"
ks="$(realpath "$SY_ANDROID_KEYSTORE")"
case "$ks" in "$repo"/*) die "the keystore is inside the repository; keep it in your secrets folder" ;; esac
export SY_ANDROID_KEYSTORE="$ks" SY_ANDROID_KEY_ALIAS SY_ANDROID_KEYSTORE_PASSWORD
export SY_ANDROID_KEY_PASSWORD="${SY_ANDROID_KEY_PASSWORD:-$SY_ANDROID_KEYSTORE_PASSWORD}"

[ -n "${ANDROID_HOME:-}" ] || die "set ANDROID_HOME to the Android SDK"
command -v keytool >/dev/null || die "keytool (a JDK 17+) is not on PATH"
command -v node >/dev/null || die "node is not on PATH"

# The signing certificate, read from the keystore (the password only through the environment).
cert_sha256="$(keytool -exportcert -alias "$SY_ANDROID_KEY_ALIAS" -keystore "$ks" -storepass:env SY_ANDROID_KEYSTORE_PASSWORD 2>/dev/null | sha256sum | cut -d' ' -f1)" || true
case "$cert_sha256" in
  e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 | "") die "could not read the key from the keystore: check the alias and the keystore password" ;;
esac

if [ "${1:-}" = "--preflight" ]; then
  echo "phone-apk-release: signing input ok (key alias $SY_ANDROID_KEY_ALIAS, certificate $cert_sha256)"
  exit 0
fi

# --- build -------------------------------------------------------------------------------
export SY_APP_VARIANT=production
if [ -z "${SY_SKIP_PREBUILD:-}" ]; then
  npx expo prebuild --platform android --no-install --clean
fi
(cd android && ./gradlew --no-daemon -I "$phone/scripts/sy-release-signing.init.gradle" ${SY_GRADLE_ARGS:-} :app:assembleRelease)

built="$phone/android/app/build/outputs/apk/release/app-release.apk"
[ -f "$built" ] || die "Gradle did not produce $built"

# --- verify what was built ---------------------------------------------------------------
# build-tools: the newest installed one.
bt="$(ls -d "$ANDROID_HOME"/build-tools/*/ 2>/dev/null | sort -V | tail -n 1 || true)"
[ -n "$bt" ] && [ -x "${bt}apksigner" ] || die "apksigner not found in \$ANDROID_HOME/build-tools"
signed="$("${bt}apksigner" verify --print-certs "$built")" || die "apksigner refused the APK"
apk_cert="$(printf '%s\n' "$signed" | sed -n 's/^Signer #1 certificate SHA-256 digest: //p' | head -n 1)"
[ "$apk_cert" = "$cert_sha256" ] || die "the APK is not signed with the keystore's key (apksigner says ${apk_cert:-nothing})"

cfg="$(npx expo config --type public --json)"
version="$(printf '%s' "$cfg" | node -p 'JSON.parse(require("fs").readFileSync(0,"utf8")).version')"
build="$(printf '%s' "$cfg" | node -p 'JSON.parse(require("fs").readFileSync(0,"utf8")).android.versionCode')"
min_sdk=31 # the self-hosted profile's minSdkVersion (app.config.ts)
if [ -x "${bt}aapt2" ]; then
  badge="$("${bt}aapt2" dump badging "$built" 2>/dev/null || true)"
  b="$(printf '%s\n' "$badge" | sed -n "s/.*versionCode='\([0-9]*\)'.*/\1/p" | head -n 1)"
  m="$(printf '%s\n' "$badge" | sed -n "s/^sdkVersion:'\([0-9]*\)'.*/\1/p" | head -n 1)"
  [ -z "$b" ] || [ "$b" = "$build" ] || die "the APK's versionCode is $b, the config says $build"
  [ -z "$m" ] || min_sdk="$m"
fi

# --- publish files -----------------------------------------------------------------------
apk="switchyard-$version.apk"
mkdir -p dist
rm -f "dist/$apk" dist/SHA256SUMS dist/manifest.json
cp "$built" "dist/$apk"
(cd dist && sha256sum "$apk" > SHA256SUMS)
apk_sha="$(cut -d' ' -f1 dist/SHA256SUMS)"
node -e '
const [version, build, minSdk, apk, size, sha, cert, notes] = process.argv.slice(1);
const m = { v: 1, version, build: Number(build), min_sdk: Number(minSdk), apk, size: Number(size),
  sha256: sha, cert_sha256: cert, notes, published_at: new Date().toISOString() };
require("fs").writeFileSync("dist/manifest.json", JSON.stringify(m) + "\n");
' "$version" "$build" "$min_sdk" "$apk" "$(wc -c < "dist/$apk" | tr -d ' ')" "$apk_sha" "$cert_sha256" "${SY_RELEASE_NOTES:-}"

echo
echo "release $version (build $build, min sdk $min_sdk)"
echo "  apk          phone/dist/$apk"
echo "  sha256       $apk_sha"
echo "  signing cert $cert_sha256   (publish this with the release)"
echo "  files        phone/dist/SHA256SUMS phone/dist/manifest.json"
echo "next, by hand: gh release create phone-v$version phone/dist/$apk phone/dist/SHA256SUMS phone/dist/manifest.json"
