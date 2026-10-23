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
//	-ldflags "-X github.com/mraj602/switchyard/internal/hub/appmirror.ReleaseCertSHA256=<hex>"
var ReleaseCertSHA256 = "0b715b03eb515301851200431ff21a5114f51d97d615812f6554e46dd644eddd"
