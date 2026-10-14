# Switchyard: Go side (sy and the hub). The prototype (Electron/Node) keeps using npm.
#
#   make build       build ./dist/sy
#   make test        go test ./...
#   make lint        gofmt check + go vet
#   make spec-check  load docs/spec/schema.sql into a temp SQLite db, validate docs/spec/api.yaml
#   make fixtures    regenerate testdata/ (spends a little real Claude usage; see testdata/README.md)
#   make check       lint + test + spec-check (what CI runs)
#   make e2e-setup   setup E2E in the Fedora and Ubuntu containers (podman or docker; E2E_ENGINE=...)
#                    or natively: test/e2e/setup/run.sh --native
#   make e2e-machines  sy machine add / pull / remove against an sshd container (SY_E2E_RUNTIME=podman|docker;
#                    skips when no container runtime works)
#   make e2e-terminal  sy board / term / open / ssh end to end (test/e2e/terminal; fake opener, temp HOME)
#   make pane-check  the Claude Code pane mod gate (scripts/pane-check.sh and its --self-test; needs claude)
#   make release-check  a release build of sy contains no test seams (fakeclock, fakeopen)
#   make app-test    the desktop app (app/): npm ci --ignore-scripts, lint, unit tests (no display)
#   make app-smoke   the Electron smoke S1-S12 (Linux: under xvfb-run; a real hub built from here)
#   make app-dist    the bundled sy for this machine + the app packages in app/dist (never signed)
#   make app-e2e     the packaged AppImage end to end (its own e2e build in app/dist/e2e, which alone
#                    allows --remote-debugging-port; Linux, xvfb-run)
#   make phone-deps  install phone/node_modules from the lockfile (npm ci)
#   make phone-check the phone app (phone/): npm ci, tsc, eslint, jest, secrets check, and the
#                    android + ios prebuild checks into a temp copy (never signed, nothing uploaded)
#   make phone-prebuild  only the two prebuild checks (phone.md P14)
#   make phone-android-debug  optional: expo prebuild into phone/android (gitignored) and an
#                    unsigned debug compile (needs ANDROID_HOME and JDK 17+; never distributed)
#   make phone-apk-release  YOU run it, not an agent: expo prebuild + Gradle assembleRelease signed with
#                    your own keystore (SY_ANDROID_KEYSTORE, SY_ANDROID_KEY_ALIAS, SY_ANDROID_KEYSTORE_PASSWORD
#                    or SY_ANDROID_SIGNING_PROPERTIES; refuses without them) -> phone/dist/{apk,SHA256SUMS,manifest.json}

GO       ?= go
PYTHON   ?= python3
SQLITE3  ?= sqlite3
VERSION  ?= $(shell git describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS  := -s -w \
	-X github.com/mraj602/switchyard/internal/version.Version=$(VERSION) \
	-X github.com/mraj602/switchyard/internal/version.Commit=$(COMMIT)
GOFILES   = $(shell git ls-files --cached --others --exclude-standard -- '*.go')

.PHONY: all build test lint fmt spec-check fixtures check clean e2e-setup e2e-setup-fedora e2e-setup-ubuntu e2e-machines \
	e2e-terminal pane-check release-check app-deps app-test app-smoke app-dist app-e2e \
	phone-deps phone-check phone-prebuild phone-android-debug phone-apk-release

all: check build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/sy ./cmd/sy

test:
	$(GO) test ./...
	$(GO) test -tags fakeinbox,peerstest ./internal/peers/... ./cmd/sy

lint:
	@out="$$(gofmt -l $(GOFILES))"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...

fmt:
	gofmt -w $(GOFILES)

# The Go test in internal/hub/store applies schema.sql with modernc.org/sqlite (the driver
# the hub uses). The sqlite3 CLI check runs too when the CLI is installed.
spec-check:
	$(GO) test -run 'TestSpecSchemaApplies|TestRecordKeepsRawBody' ./internal/hub/store
	@if command -v $(SQLITE3) >/dev/null 2>&1; then \
		tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
		$(SQLITE3) -bail "$$tmp/spec.db" < docs/spec/schema.sql && \
		$(SQLITE3) "$$tmp/spec.db" 'PRAGMA foreign_key_check; PRAGMA integrity_check;' | grep -qx ok && \
		echo "schema.sql: loads in sqlite3 $$($(SQLITE3) --version | cut -d' ' -f1)"; \
	else echo "schema.sql: sqlite3 CLI not installed, skipped CLI check"; fi
	@$(PYTHON) -c 'import sys, yaml; d = yaml.safe_load(open("docs/spec/api.yaml")); \
		assert d.get("openapi", "").startswith("3.1"), "not OpenAPI 3.1"; \
		print("api.yaml: parses,", len(d["paths"]), "paths")'
	@if $(PYTHON) -c 'import openapi_spec_validator' 2>/dev/null; then \
		$(PYTHON) -c 'import yaml; from openapi_spec_validator import validate; \
			validate(yaml.safe_load(open("docs/spec/api.yaml"))); print("api.yaml: valid OpenAPI 3.1")'; \
	else echo "api.yaml: openapi-spec-validator not installed, skipped full validation"; fi

# testdata/ is produced by scripts/fixtures (see testdata/README.md). Pass CAPDIR=<dir> to
# reuse an earlier capture instead of making new Claude runs.
fixtures:
	scripts/fixtures/regenerate.sh $(CAPDIR)
	scripts/fixtures/check-redaction.sh

check: lint test spec-check

E2E_ENGINE ?=
e2e-setup: e2e-setup-fedora e2e-setup-ubuntu

e2e-setup-fedora:
	test/e2e/setup/run.sh fedora $(E2E_ENGINE)

e2e-setup-ubuntu:
	test/e2e/setup/run.sh ubuntu $(E2E_ENGINE)

# The phase 5 end-to-end test (docs/phases/5.md): real SSH into a container, never the real HOME.
# -v so a skip says why; the test picks podman, else docker, unless SY_E2E_RUNTIME is set.
e2e-machines:
	$(GO) test -count=1 -v -timeout 15m ./test/e2e/machines

# The desktop app (docs/phases/6.md). npm runs in app/ only; the root prototype is never touched.
# Smoke and e2e use temp HOME / SY_HOME / user data folders; on Linux they run under xvfb-run
# when it is installed (unset WAYLAND_DISPLAY so Electron uses X11).
NPM      ?= npm
XVFB     := $(shell command -v xvfb-run >/dev/null 2>&1 && echo 'env -u WAYLAND_DISPLAY -u XDG_SESSION_TYPE xvfb-run -a --server-args="-screen 0 1440x940x24"')

# Installs app/node_modules with the Electron binary, unless they are there already.
app-deps:
	cd app && { node -e "require('fs').accessSync(require('electron'))" 2>/dev/null || $(NPM) ci; }

# Unit tests need no Electron binary (CI runs npm ci --ignore-scripts); reuse node_modules if present.
app-test:
	cd app && { test -d node_modules || $(NPM) ci --ignore-scripts; } && $(NPM) run lint && $(NPM) test

# The phone app (docs/phases/8.md "Local gates"). No target here signs, uploads or runs eas.
phone-deps:
	cd phone && $(NPM) ci

phone-check: phone-deps
	cd phone && $(NPM) run check
	$(MAKE) phone-prebuild

phone-prebuild:
	cd phone && node scripts/prebuild-check.mjs android && node scripts/prebuild-check.mjs ios

phone-android-debug: phone-deps
	@test -n "$$ANDROID_HOME" || { echo "phone-android-debug: set ANDROID_HOME to the Android SDK"; exit 1; }
	cd phone && SY_APP_VARIANT=development npx expo prebuild --platform android --no-install --clean
	cd phone/android && ./gradlew :app:assembleDebug
	@echo "unsigned debug APK: phone/android/app/build/outputs/apk/debug/ (never distribute it)"

# The signed release build (phone.md P16.1). Never run by CI or an agent; see phone/README.md.
phone-apk-release:
	cd phone && ./scripts/release-apk.sh --preflight
	$(MAKE) phone-deps
	cd phone && ./scripts/release-apk.sh

app-smoke: app-deps
	CGO_ENABLED=0 $(GO) build -o app/node_modules/.cache/sy-smoke/sy ./cmd/sy
	cd app && SY_BIN="$$PWD/node_modules/.cache/sy-smoke/sy" $(XVFB) $(NPM) run smoke

app-dist: app-deps
	cd app && node scripts/fetch-sy.mjs auto auto && CSC_IDENTITY_AUTO_DISCOVERY=false $(NPM) run dist -- --publish never

# The e2e build bakes switchyard.allowCdp=true into the asar (desktop.md C2); app-dist never does.
app-e2e: app-deps
	cd app && node scripts/fetch-sy.mjs auto auto && scripts/dist-e2e.sh
	cd app && $(XVFB) $(NPM) run e2e

clean:
	rm -rf dist

# docs/phases/7.md WP-E: the terminal scenarios, with `sy` built with -tags fakeopen.
e2e-terminal:
	$(GO) test -count=1 -v -timeout 15m ./test/e2e/terminal

pane-check:
	scripts/pane-check.sh
	scripts/pane-check.sh --self-test

release-check:
	scripts/ci/check-release-seams.sh
