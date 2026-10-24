#!/usr/bin/env bash
# Saves the mock hub's replies (webui/devserver) as the app's fixtures:
# phone/test/fixtures/hub/*.json. Pairs as a phone with the devserver's fixed pair secret
# (cookie: false, so the claim reply carries the bearer token), then reads every endpoint the
# app uses with that bearer. The devserver's data is relative to the time it starts, so
# _meta.json records "now" for tests that format times.
#
# Usage: phone/scripts/dump-fixtures.sh [port]   (needs go, curl and node; offline, loopback only)
set -euo pipefail

port="${1:-18788}"
here="$(cd "$(dirname "$0")/.." && pwd)"
root="$(cd "$here/.." && pwd)"
out="$here/test/fixtures/hub"
base="http://127.0.0.1:$port"
pair_secret="ZGV2c2VydmVyLXBhaXItc2VjcjE" # webui/devserver PairSecret (a public test value)

tmp="$(mktemp -d)"
cleanup() {
  [[ -n "${pid:-}" ]] && kill "$pid" 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT

(cd "$root" && go build -o "$tmp/webui-dev" ./cmd/webui-dev)
"$tmp/webui-dev" -addr "127.0.0.1:$port" >"$tmp/log" 2>&1 &
pid=$!
for _ in $(seq 1 50); do
  curl -fsS "$base/health" >/dev/null 2>&1 && break
  sleep 0.1
done

mkdir -p "$out"
pretty() { node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>process.stdout.write(JSON.stringify(JSON.parse(s),null,2)+"\n"))'; }

curl -fsS -X POST "$base/v1/pair/claim" -H 'Content-Type: application/json' \
  -d "{\"secret\":\"$pair_secret\",\"name\":\"Pixel\",\"expect_kind\":\"phone\",\"cookie\":false,\"client\":\"watchyard-phone/0.8.0\"}" \
  | pretty >"$out/pair-claim.json"
token="$(node -e 'process.stdout.write(require(process.argv[1]).token)' "$out/pair-claim.json")"

get() { # get <path> <file>
  curl -fsS -H "Authorization: Bearer $token" "$base$1" | pretty >"$out/$2"
}
get /health health.json
get /v1/me me.json
get /v1/overview overview.json
get '/v1/sessions?limit=50' sessions.json
get /v1/accounts accounts.json
get /v1/machines machines.json
get '/v1/alerts?limit=50' alerts.json
get /v1/rules rules.json
get /v1/push/config push-config.json
get /v1/peers/matrix peers-matrix.json
get '/v1/peers/audit?limit=50' peers-audit.json

session="$(node -e 'const s=require(process.argv[1]).sessions||[];process.stdout.write(s.length?s[0].id:"")' "$out/sessions.json")"
[[ -n "$session" ]] && get "/v1/sessions/$session" session.json
alert="$(node -e 'const a=require(process.argv[1]).alerts||[];process.stdout.write(a.length?a[0].id:"")' "$out/alerts.json")"
[[ -n "$alert" ]] && get "/v1/alerts/$alert" alert.json

node -e 'process.stdout.write(JSON.stringify({source:"webui/devserver via phone/scripts/dump-fixtures.sh",dumped_at:new Date().toISOString(),now_ms:Date.now()},null,2)+"\n")' >"$out/_meta.json"
echo "fixtures written to $out"
