// Package appmirror keeps the hub's mirror of the newest Android app release (phone.md P16.2).
// Once a day it asks GitHub for the project's releases, picks the newest published
// `phone-vX.Y.Z` one, downloads manifest.json, SHA256SUMS and the APK, checks the APK's SHA-256
// against both, and only then publishes it under <dir>/<version>/. The hub never re-signs or
// changes the APK. Only the current and the previous version are kept.
//
// Depends on: the standard library only (the HTTP client is given by the caller).
package appmirror
