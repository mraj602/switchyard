// Package webui embeds the hub's web app: the desktop board and the installable phone app
// (PWA), one responsive single-page app (Now, Sessions, Session detail, Accounts, Machines,
// More, Alerts, Settings, Devices, Pair, Owner, Login) written as plain ES modules
// and CSS with no build step. The hub serves FS() at / and the pages call the read API
// in docs/spec/api.yaml with the session cookie set by POST /v1/session/login.
//
// Layout of static/:
//
//	index.html        page shell, loads styles and app.js
//	manifest.webmanifest, icons/   install metadata and committed PNG icons
//	sw.js, sw-lib.js  service worker (caches, offline copy, push); sw-lib is the testable part
//	pwa.js, push.js   registration, install and update flow; notification permission and subscription
//	route.js, platform.js  pure helpers: hash routes; device and notification guesses, codes
//	app.js            hash router, shell, render loop
//	api.js            fetch helpers, login, EventSource stream with reconnect
//	store.js          overview, sessions and detail kept fresh by the stream or by polling
//	format.js         times, percentages, names
//	components/*.js   DOM helper, state tags, seat colours, window meters, cards, nav
//	views/*.js        one file per page
//	styles/*.css      tokens (light and dark), layout (sidebar or tab bar), components
//
// Development and tests use the mock hub in webui/devserver (run it with
// `go run ./cmd/webui-dev`). The browser smoke test in webui/smoke is gated by
// SY_WEBUI_BROWSER=1.
//
// Handler (serve.go) serves FS() with the build hash templated into sw.js, content types and
// the strict CSP; UIBuild is the hash for /health.
//
// Phases 2 and 4. Depends on: nothing inside the module (static files only).
package webui
