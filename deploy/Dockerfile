# Switchyard hub: a static `sy` binary on Alpine, running `sy hub serve` as a non-root user.
#
#   docker build -f deploy/Dockerfile -t switchyard-hub .
#   docker run -d --name hub -p 127.0.0.1:8787:8787 -v hub-data:/data switchyard-hub
#
# Build args: VERSION and COMMIT are stamped into `sy version` (the .git folder is not in the
# build context, so pass them in: --build-arg VERSION=$(git describe --tags --always)).

FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY webui ./webui
COPY docs/spec ./docs/spec
ARG VERSION=dev
ARG COMMIT=
RUN go build \
      -ldflags "-s -w \
        -X github.com/mraj602/switchyard/internal/version.Version=${VERSION} \
        -X github.com/mraj602/switchyard/internal/version.Commit=${COMMIT}" \
      -o /out/sy ./cmd/sy

FROM alpine:3.22
# ca-certificates: the hub sends web pushes and ntfy over HTTPS. tzdata: quiet hours.
# openssh-client: the hub pulls servers over SSH (`--reach pull`, machines.md 3.4).
RUN apk add --no-cache ca-certificates tzdata openssh-client \
 && addgroup -S -g 10001 sy \
 && adduser -S -u 10001 -G sy -h /data sy \
 && mkdir -p /data/state /data/config \
 && chown -R sy:sy /data
COPY --from=build /out/sy /usr/local/bin/sy

# Everything the hub writes lives under /data (paths.md): the database in <data>, tokens
# and caches in <state>, the pull key and its pinned known_hosts in <state>/pull.
ENV SY_DATA_DIR=/data \
    SY_STATE_DIR=/data/state \
    SY_CONFIG_DIR=/data/config
USER sy
VOLUME /data
EXPOSE 8787
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8787/health || exit 1

# 0.0.0.0 inside the container; the compose file decides which host address publishes it.
# --insecure-lan is required for any non-loopback listen address without TLS in front of
# the process (the proxy or Tailscale in front of the container provides the transport).
ENTRYPOINT ["sy"]
CMD ["hub", "serve", "--listen", "0.0.0.0:8787", "--insecure-lan"]
