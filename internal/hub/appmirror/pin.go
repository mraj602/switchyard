package appmirror

// ReleaseCertSHA256 is the SHA-256 fingerprint (64 lower-case hex digits, no colons) of the
// certificate that signs every released Android APK (docs/RELEASING.md, "One-time setup: the
// Android signing key"; the same value is in docs/guide/android.md). When it is empty the
// mirror refuses to mirror anything and says why. The mirror publishes a release only when
// both manifest.json and the APK's own signing block name this certificate, so a hostile or
// hijacked GitHub release cannot choose the key the phone is told to trust.
//
// A fork that signs with its own key sets this at build time instead of editing the file:
//
//	-ldflags "-X github.com/mraj602/watchyard/internal/hub/appmirror.ReleaseCertSHA256=<hex>"
var ReleaseCertSHA256 = "a9e68504609ac2be216a35d787bb3e9d23b5fc66b9c4d939af2678e04724c89f"
