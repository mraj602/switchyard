#!/usr/bin/env bash
# pane-check.sh: the gate for the Switchyard pane mod (docs/spec/terminal.md T5).
#
#   scripts/pane-check.sh [mod-dir]     default: plugin/switchyard-pane
#   scripts/pane-check.sh --self-test   run the checks on scripts/pane-check.d/forbidden and
#                                       succeed only if they FAIL there
#
# It runs, in order:
#   1. claude plugin validate --json: no errors, and no refusal.
#   2. The allowed surface: the hooks and `$` calls validate reports for the mod must be inside
#      T5's list, and every process.run/spawn in the source must name SY as argv[0].
#   3. tsc -p over the mod, when the claude-code types are there (see below).
#   4. claude plugin test.
#
# Claude Code is never run against your real config: the script makes a temp HOME and
# CLAUDE_CONFIG_DIR, clears XDG_* and SY_*, and runs from an empty folder.
#
# Environment:
#   CLAUDE_BIN                  the claude binary (default: claude)
#   SY_PANE_TYPES               a claude-code.d.ts to type-check against. Default: the mod's own
#                               .claude-plugin/types/claude-code/index.d.ts, which Claude Code lays
#                               when it loads the mod. With neither, tsc is skipped, unless...
#   PANE_CHECK_REQUIRE_TSC=1    ...a missing types file or missing tsc is a failure
#   PANE_CHECK_TSC              the tsc command (default: tsc, else npx --yes typescript@5.6.3)
set -u

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/.." && pwd)
claude_bin=${CLAUDE_BIN:-claude}
types_env=${SY_PANE_TYPES:-}   # read before SY_* is cleared below

if [ "${1:-}" = "--self-test" ]; then
  # Each fixture must fail with its own message: a non-zero exit alone could be a missing
  # claude binary or any other breakage, which would let a dead check pass the self-test.
  st=0
  selftest() { # fixture, then extended regexes that stderr must all match
    local fx=$1 out pat
    shift
    if out=$("$0" "$here/pane-check.d/$fx" 2>&1 >/dev/null); then
      echo "pane-check self-test: the $fx fixture PASSED the check, it must fail" >&2
      st=1
      return
    fi
    for pat in "$@"; do
      if ! grep -Eq -- "$pat" <<<"$out"; then
        echo "pane-check self-test: the $fx fixture failed, but without the expected message /$pat/:" >&2
        echo "$out" >&2
        st=1
        return
      fi
    done
    echo "pane-check self-test: $fx fails with the expected messages"
  }
  selftest forbidden 'FAIL: hook outside the allowed surface: tool\.call' 'FAIL: .*process reference'
  selftest forbidden-alias 'FAIL: .*[1-9][0-9]* process reference'
  selftest forbidden-bracket 'FAIL: .*computed access on \$'
  exit $st
fi

dir=${1:-$root/plugin/switchyard-pane}
dir=$(cd "$dir" && pwd) || { echo "pane-check: no such folder: ${1:-}" >&2; exit 2; }

# Everything claude runs in a temp home, from an empty folder.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/home" "$tmp/cwd"
for v in $(env | sed -n 's/^\(XDG_[A-Za-z_]*\)=.*/\1/p; s/^\(SY_[A-Za-z_]*\)=.*/\1/p'); do unset "$v"; done
unset CLAUDE_CONFIG_DIR
export HOME="$tmp/home" CLAUDE_CONFIG_DIR="$tmp/home/.claude" SY_HOME="$tmp/sy"

claude_run() { (cd "$tmp/cwd" && "$claude_bin" "$@"); }

fail=0
bad() { echo "pane-check: FAIL: $*" >&2; fail=1; }

# 1 + 2: validate and the allowed surface -------------------------------------------------
echo "pane-check: claude plugin validate"
claude_run plugin validate --json "$dir" >"$tmp/validate.json" 2>"$tmp/validate.err"
if [ ! -s "$tmp/validate.json" ]; then
  cat "$tmp/validate.err" >&2
  bad "claude plugin validate printed no report"
else
  python3 - "$tmp/validate.json" "$dir" <<'PY' || fail=1
import json, re, sys

report = json.load(open(sys.argv[1]))
mod = sys.argv[2]
errs = []

def entries():
    yield report.get("manifest") or {}
    for c in report.get("contents") or []:
        yield c

for e in entries():
    for x in e.get("errors") or []:
        errs.append("refused: %s: %s" % (x.get("path", ""), x.get("message", "")))
if not report.get("success"):
    errs.append("validate reports success=false")

HOOKS = {"session.start", "session.end", "command.run", "ui.render", "ui.press"}
COMPONENTS = {"Pane", "AbovePrompt"}
CALL_OK = re.compile(
    r"^\$\.(process\.(spawn|run)|ui\.[A-Za-z]+|command\.register|clock\.[A-Za-z]+"
    r"|state\.[A-Za-z]+|session\.id)$"
)

hook_notes = 0
call_notes = 0
for c in report.get("contents") or []:
    if c.get("type") != "hooks":
        continue
    for note in c.get("notes") or []:
        m = re.match(r"^\S+ hooks: (.*)$", note)
        if m:
            hook_notes += 1
            body = m.group(1).strip()
            if body and body != "nothing":
                # "a, ui.render{component=Pane, requestId=x}, b": split on commas outside braces
                items, depth, cur = [], 0, ""
                for ch in body:
                    depth += ch == "{"
                    depth -= ch == "}"
                    if ch == "," and depth == 0:
                        items.append(cur.strip()); cur = ""
                    else:
                        cur += ch
                items.append(cur.strip())
                for it in items:
                    name = it.split("{")[0].strip()
                    if name not in HOOKS:
                        errs.append("hook outside the allowed surface: %s" % it)
                    if name == "ui.render":
                        comp = re.search(r"component=(\w+)", it)
                        if not comp or comp.group(1) not in COMPONENTS:
                            errs.append("ui.render outside Pane and AbovePrompt: %s" % it)
            continue
        m = re.match(r"^\S+ calls: (.*)$", note)
        if m:
            call_notes += 1
            body = m.group(1).strip()
            if body and not body.startswith("nothing"):
                # split on commas outside parentheses: "(via follow, followNewSession)"
                parts, depth, cur = [], 0, ""
                for ch in body:
                    depth += ch == "("
                    depth -= ch == ")"
                    if ch == "," and depth == 0:
                        parts.append(cur.strip()); cur = ""
                    else:
                        cur += ch
                parts.append(cur.strip())
                for it in parts:
                    name = re.sub(r"\s*\(via [^)]*\)$", "", it)
                    if not CALL_OK.match(name):
                        errs.append("call outside the allowed surface: %s" % it)
            continue
        if "gating hook" in note:
            errs.append("a gating hook: %s" % note)
if hook_notes == 0 or call_notes == 0:
    errs.append("validate's report has no 'hooks:' or 'calls:' line to check (Claude Code changed its report?)")

# Every process.run / process.spawn names SY as argv[0].
import glob, os
for path in glob.glob(os.path.join(mod, "hooks", "**", "*"), recursive=True):
    if not re.search(r"\.(tsx?|jsx?|mjs|cjs|mts|cts)$", path) or re.search(r"\.test\.", path):
        continue
    src = open(path, encoding="utf-8").read()
    # Comments are not code (a // inside a string such as a URL is not preceded by a space).
    src = re.sub(r"/\*.*?\*/", " ", src, flags=re.S)
    src = re.sub(r"(?m)(^|\s)//.*$", " ", src)
    # Conservative rule: the word "process" may appear only as a direct
    # $.process.run([SY, ...) or $.process.spawn({ argv: [SY, ...). An alias
    # (const p = $.process; p.run(...)), a destructure, $['process'] or any other mention
    # is refused, as is any computed access on $ (it could name process without the word).
    good = len(re.findall(r"\$\.process\.run\(\s*\[\s*SY\s*[,\]]", src)) + \
        len(re.findall(r"\$\.process\.spawn\(\s*\{\s*argv\s*:\s*\[\s*SY\s*[,\]]", src))
    total = len(re.findall(r"\bprocess\b", src))
    rel = os.path.relpath(path, mod)
    if total != good:
        errs.append("%s: %d process reference(s), %d of them a direct $.process.run/spawn with SY as argv[0]" % (rel, total, good))
    if re.search(r"\$\s*\??\.?\s*\[", src):
        errs.append("%s: computed access on $ (e.g. $['process']) is not allowed" % rel)

for e in errs:
    print("pane-check: FAIL: " + e, file=sys.stderr)
sys.exit(1 if errs else 0)
PY
fi

# 3: tsc ------------------------------------------------------------------------------------
types=$types_env
[ -z "$types" ] && [ -f "$dir/.claude-plugin/types/claude-code/index.d.ts" ] && types="$dir/.claude-plugin/types/claude-code/index.d.ts"
tsc_cmd=()
if [ -n "${PANE_CHECK_TSC:-}" ]; then
  read -r -a tsc_cmd <<<"$PANE_CHECK_TSC"
elif command -v tsc >/dev/null 2>&1; then
  tsc_cmd=(tsc)
elif command -v npx >/dev/null 2>&1; then
  tsc_cmd=(npx --yes -p typescript@5.6.3 tsc)
fi
if [ -z "$types" ] || [ ${#tsc_cmd[@]} -eq 0 ]; then
  if [ "${PANE_CHECK_REQUIRE_TSC:-}" = 1 ]; then
    bad "tsc needs the claude-code types (SY_PANE_TYPES or .claude-plugin/types/) and tsc"
  else
    echo "pane-check: tsc skipped (no claude-code types or no tsc); set SY_PANE_TYPES to check" >&2
  fi
else
  echo "pane-check: tsc -p"
  work="$tmp/tsmod"
  mkdir -p "$work/.claude-plugin/types/claude-code"
  cp -R "$dir/hooks" "$work/hooks"
  [ -d "$dir/types" ] && cp -R "$dir/types" "$work/types"
  cp "$types" "$work/.claude-plugin/types/claude-code/index.d.ts"
  cat >"$work/tsconfig.json" <<'JSON'
{
  "compilerOptions": {
    "target": "es2023", "lib": ["es2023"], "types": [],
    "module": "esnext", "moduleResolution": "bundler",
    "strict": true, "noUncheckedIndexedAccess": true,
    "noEmit": true, "skipLibCheck": true,
    "jsx": "react", "jsxFactory": "h", "jsxFragmentFactory": "Fragment"
  },
  "include": [".claude-plugin/types", "hooks", "types"]
}
JSON
  (cd "$work" && "${tsc_cmd[@]}" -p .) || bad "tsc -p reported errors"
fi

# 4: the tests ------------------------------------------------------------------------------
echo "pane-check: claude plugin test"
claude_run plugin test "$dir" || bad "claude plugin test failed"

if [ "$fail" -ne 0 ]; then
  echo "pane-check: FAILED" >&2
  exit 1
fi
echo "pane-check: ok"
