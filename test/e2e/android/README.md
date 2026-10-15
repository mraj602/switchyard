# Android app end to end (`make e2e-android`)

The phone app (`phone/`, with its `sy-link` module) on the Android emulator against a real
`sy hub serve` on this host. It is never part of `make check`; it skips (exit 0, `SKIP: ...`)
without the SDK, the AVD, `/dev/kvm`, python3 or an APK it can build.

```
make e2e-android                          # or: test/e2e/android/run.sh
SY_E2E_PHASES="4 5" test/e2e/android/run.sh   # only some phases (pairing, phase 2, always runs)
```

First run builds the APK (`phone/scripts/e2e-android-apk.sh`, about 6 minutes, x86_64 only) into
`phone/dist/app-e2e.apk` (untracked); later runs reuse it. Delete it after changing phone code.
The whole run takes about 8 minutes once the APK exists.

## What it checks

| Phase | Checks |
| --- | --- |
| 1 | the APK installs, `POST_NOTIFICATIONS` is granted, the app starts from its bundled JS |
| 2 | `switchyard://pair?u=&s=&c=&h=&k=` (the QR's secret, code, hub fingerprint, seal key) opens a card that claims nothing until tapped; the tap pairs; the hub has `keys.reg = qr` and a `link` push target |
| 3 | `SyLinkService` is a foreground service, the hub sees the link connected, the persistent notification says "Connected to hub" |
| 4 | a fake machine (`fakemachine`) posts a hook record, the alert engine fires "asked", the notification arrives on `sy_needs_you` with Open / Snooze 15 min / Seen; Seen and Snooze from the shade reach the hub; the hub's "send test notification" arrives on `sy_test` |
| 5 | hub restart: the phone reconnects, a new alert is delivered |
| 6 | airplane mode: the phone reports Reconnecting, an alert raised offline is held, the hub restarts meanwhile, airplane off: reconnect and replay |
| 7 | `am force-stop`: the service stays down (Android blocks every restart); opening the app restarts it and the alert queued meanwhile replays |
| 8 | swiped out of Recents (task removed): the service and link survive (not conclusive when the launcher ignores the gesture; it says so) |
| 9 | `adb reboot`: `BOOT_COMPLETED` restarts the service without opening the app, the link reconnects |
| 10 | revoke via `DELETE /v1/devices/{id}`: 4403, service stops, "This phone was removed from the hub" stays, no orphaned connection notification, the app shows Pair and says why; then re-pair and `sy hub token revoke` (the running hub notices at its next sweep) |
| 11 | claude.ai account tabs (P17): two seats with hub-made `seat_<ULID>` ids; Accounts > Open claude.ai opens each seat's tab, the WebView loads a claude.ai page (SKIP when the emulator has no internet), no crash; Settings > Claude tabs lists one profile per seat; Open in Claude on B's session opens B's tab; Sign out (A) removes A's profile only, also after an app restart. Never signs in or types into claude.ai. The "non-claude link goes to the browser" check is a SKIP: the hub drops any `remote_url` outside `https://claude.ai/`, so only a link tapped inside the page could do it |
| 12 | analytics: the fake machine posts a `token_rows` record (1M in, 100k out of claude-sonnet-5 = $3.00); More > Analytics shows $3.00, 1.1M tokens and the daily bars |

## How it is built

- **No Metro.** `phone/scripts/e2e-android-apk.sh` prebuilds with `SY_APP_VARIANT=development`
  and builds the Gradle *release* variant: the debug variant carries expo-dev-client, whose
  launcher wants a Metro server. The generated project signs release with its debug key; nothing
  is a store build, nothing is uploaded, nothing is committed (`phone/android` is untracked).
  `SY_APP_VARIANT=development` allows cleartext in the manifest and (`extra.cleartext`) lets the
  pair screen accept `http://10.0.2.2:<port>`.
- **Hub.** `dist/sy hub serve --listen 127.0.0.1:<free port>`; the emulator reaches it at
  `10.0.2.2`. `HOME` and every `XDG_*` dir are a temp dir (`env -i`), the `rule_asked` delay is
  set to 0 and the flood limit raised so alerts are immediate.
- **Emulator.** An attached `emulator-*` is reused (and left running); otherwise the AVD starts
  headless on port 5584 and is killed at the end (`SY_E2E_KEEP_EMU=1` keeps it). All `adb` calls
  use `-s <serial>`; real devices are never addressed. `-gpu swangle_indirect -feature
  -Vulkan,-GLDirectMem` is deliberate: `swiftshader_indirect` and `guest` segfault the emulator's
  renderer on some hosts (`SY_E2E_GPU` overrides).
- On failure (or `SY_E2E_KEEP=1`) the temp dir is kept with `hub.log`, `emulator.log`,
  `logcat-*.txt`, `ui-*.xml`.

Environment variables are listed at the top of `run.sh`. `lib.py` holds the small parsers
(`dumpsys notification`, `uiautomator` dumps, JSON paths); `fakemachine/` is the Go program
that plays a machine.

## Found and fixed while writing it

1. The app crashed at start on Android: `expo-secure-store`'s keychain constants are iOS-only.
2. A development-variant bundle could not pair with an `http://` hub (the gate was `__DEV__`).
3. `switchyard://pair` dropped the QR's `h` and `k`, so "Open in app" paired without instant delivery.
4. Stopping the service left an orphaned "Not connected" connection notification.
5. The "This phone was removed from the hub" notice was wiped by the app's own unpair cleanup.
6. Claude tabs: Sign out failed with "Close the tab first" after any tab had been opened in the process, with no tab open (WebView keeps a closed tab's profile in use until the process ends).

## Not covered

- The camera QR (the deep link carries the same pins), Doze / App Standby, OEM battery killers.
- Swipe-away from Recents depends on the launcher's gesture; the service surviving it is
  asserted only when the card actually went away.
- The 15-minute watchdog (WorkManager) and the long-poll fallback.
