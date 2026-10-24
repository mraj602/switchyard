# Releasing

For the maintainer. It covers the one-time setup, then the steps for every release of `wy`, the
desktop app and the Android app. Contributors don't need it.

## Versions and tags

- One version number is shared by `wy`, the desktop app (`app/package.json`) and the Android
  app (`phone/package.json`), so a bug report's version means the same thing everywhere.
- `vX.Y.Z` releases `wy`, the hub, the install scripts and the desktop app packages
  (`.github/workflows/release.yml`).
  The tagged commit must be on `main`, and CI must be green on it.
- `phone-vX.Y.Z` releases the Android APK. You build and sign it on your own machine. CI never
  sees the signing key.
- Android's `versionCode` is `MAJOR*10000 + MINOR*100 + PATCH`, so it can only rise. Never
  reuse or lower a version.

## One-time setup: the Android signing key

Every APK you ever publish must be signed with the same key. Android refuses to install an
update signed with a different key: users would have to uninstall (and lose their pairing)
and reinstall. Treat the key like the master key of the project.

1. **Create it**, in a normal terminal (not inside an AI agent). Use a strong passphrase from
   your password manager.

   ```sh
   mkdir -p ~/secrets && chmod 700 ~/secrets
   keytool -genkeypair -v -keystore ~/secrets/watchyard-release.jks -alias watchyard \
     -keyalg RSA -keysize 4096 -validity 10000 \
     -dname "CN=Watchyard, O=Watchyard contributors"
   chmod 600 ~/secrets/watchyard-release.jks
   ```

2. **Back it up** before the first release, in at least two places that are offline and
   encrypted (for example two USB drives in different places). Store the passphrase in your
   password manager, not next to the file. Test that a backup can be read:
   `keytool -list -keystore <backup copy>`.

3. **Record the certificate fingerprint.** Users and app stores check it, and it never changes:

   ```sh
   keytool -list -v -keystore ~/secrets/watchyard-release.jks -alias watchyard | grep SHA256
   ```

   Put it in `docs/guide/android.md` under "Verify the APK", and in each `phone-v*` release
   note.

   **Also pin it in the hub.** The hub's APK mirror only publishes a release signed by this
   certificate. Put the same fingerprint, as 64 lower-case hex digits without colons, in
   `ReleaseCertSHA256` in `internal/hub/appmirror/pin.go` and commit it through the normal
   flow. It ships in the next `vX.Y.Z` hub build. Until then (the value is empty) a hub
   mirrors nothing and logs why. A fork with its own key can set it at build time with
   `-ldflags "-X github.com/mraj602/watchyard/internal/hub/appmirror.ReleaseCertSHA256=<hex>"`.
   Release the hub with the pin before the first `phone-v*` release. If you ever rotate the
   key, update the pin in the same hub release as the first APK signed with the new key.

4. **Optional:** a signing properties file outside the repo, so you don't type four
   variables each time (see `phone/README.md`):

   ```properties
   # ~/secrets/sy-signing.properties  (chmod 600)
   storeFile=/home/you/secrets/watchyard-release.jks
   keyAlias=watchyard
   ```

   Leave the passwords out of the file and export them in the shell when you build.

**If the key leaks**, the passphrase still protects it. Rotate it while you still have the old
key, with APK Signature Scheme v3 rotation (`apksigner rotate --out lineage
--old-signer ... --new-signer ...`). Then sign new releases with the lineage, and installed apps
accept the new key. **If the key is lost**, there is no recovery: publish under a new
application id and tell users to reinstall.

## Every release

### 1. Before tagging

- `main` is green in all workflows (`ci`, `app`, `phone`).
- `CHANGELOG.md` has a section for the version.
- Set the version in `app/package.json` and `phone/package.json`, and commit it to `main`
  through the normal flow.
- Test by hand on real machines: follow `docs/TESTING.md`.

### 2. `wy` and the hub

```sh
git checkout main && git pull
git tag -s vX.Y.Z -m "vX.Y.Z"      # -a if you don't sign tags
git push origin vX.Y.Z
```

`release.yml` checks the tag, runs `make check`, builds the six archives and the desktop app
packages (section 3), attests their build provenance. It then publishes the GitHub release with
the install scripts, in one step, only if every build succeeded. After it finishes, check:

- `curl -fsSL https://github.com/mraj602/watchyard/releases/latest/download/install.sh | sh` works
  on a clean machine;
- `gh attestation verify <archive> --repo mraj602/watchyard` passes for one archive.

### 3. The desktop app

Nothing to run: the same `vX.Y.Z` tag builds and publishes the desktop app, in `release.yml`.

- A `desktop` job per OS (Linux, macOS, Windows) builds the unsigned packages: AppImage, deb and
  rpm; dmg; NSIS installer. It builds the bundled `wy` from the tagged commit with the same
  ldflags as `scripts/release/build.sh`, and fails if `app/package.json`'s version is not the
  tag's version or the bundled `wy` reports another version. So **`app/package.json` must be
  set to the release version before you tag** (step 1); a mismatch fails the run before
  anything is published.
- The `release` job waits for all of them, writes `SHA256SUMS-desktop`, attests every package
  and creates the release with the `wy` archives, install scripts and packages together. If one
  OS fails there is no release (and no tag-triggered half release); fix it, delete the tag
  (`git push --delete origin vX.Y.Z`, `git tag -d vX.Y.Z`) and tag again on a fixed commit on
  `main`.
- The builds are unsigned (no paid certificates), so the release note links to the guide's
  "Unsigned builds on macOS and Windows" section (`docs/guide/desktop.md`).

After the run, check that the release lists the five packages and `SHA256SUMS-desktop`, and
that `gh attestation verify <package> --repo mraj602/watchyard` passes for one of them.

### 4. The Android app

On your machine, from a clean checkout of the `vX.Y.Z` tag:

```sh
export WY_ANDROID_KEYSTORE=~/secrets/watchyard-release.jks
export WY_ANDROID_KEY_ALIAS=watchyard
read -rs WY_ANDROID_KEYSTORE_PASSWORD && export WY_ANDROID_KEYSTORE_PASSWORD
WY_RELEASE_NOTES="Short notes for the in-app updater" make phone-apk-release
```

This writes `watchyard-X.Y.Z.apk`, `SHA256SUMS` and `manifest.json` to `phone/dist/`, and
prints the signing certificate's fingerprint. It must match the one you recorded.

Check before publishing:

```sh
apksigner verify --print-certs phone/dist/watchyard-X.Y.Z.apk   # fingerprint matches
(cd phone/dist && sha256sum -c SHA256SUMS)
```

Publish:

```sh
gh release create phone-vX.Y.Z phone/dist/watchyard-X.Y.Z.apk phone/dist/SHA256SUMS \
  phone/dist/manifest.json --title "Android X.Y.Z" --notes-file <notes with the fingerprint>
```

Paired phones see the update under **Settings → Check for update**. Within a day the hub
mirrors the newest `phone-v*` release from GitHub (`docs/spec/phone.md` P16.2), and the app
checks the hash and the signing certificate before it offers the install.

### 5. Announce

Update the release line on the website (`site/index.html`) and the README if a step
changed. Write the release note so it stands on its own: what changed, how to update
(`wy update`, the app's **Check for update**), and the APK fingerprint.

## Where the Android app is published

For now the APK is published **only on GitHub Releases**, as `phone-vX.Y.Z`. Users install it
from there; after that the app's **Check for update** and Obtainium keep it current. There are
no store submissions yet. The notes below are for later.

- **GitHub Releases** is the only source.
- **Obtainium:** users add `https://github.com/mraj602/watchyard` and filter releases with
  the regex `^Android ` (the release title), so `wy` releases are skipped. Nothing for you to
  do.
- **IzzyOnDroid (later, optional):** open an inclusion request on their repository once a signed `phone-v*`
  release exists. It needs a FOSS licence (AGPL-3.0), an APK in GitHub Releases, no
  proprietary libraries (`phone.yml` checks this), and the same signing key on every release.
- **F-Droid:** possible later. F-Droid builds from source itself. With reproducible builds it
  can ship your signature, so users can move between F-Droid and GitHub builds. That needs
  the build to be byte-for-byte reproducible first.
