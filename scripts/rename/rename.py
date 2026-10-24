#!/usr/bin/env python3
"""Rename the project in one pass (docs/spec/rename.md, scripts/rename/RUNBOOK.md).

    scripts/rename/rename.py --to-slug watchyard --to-name Watchyard --cli wy --android-id new \
        --reissue [--dry-run] [--list]

(--reissue: the released versions are published again under the new name, so the whole
CHANGELOG is rewritten and gets no rename note; --version X.Y.Z instead bumps app/ and phone/
and records the rename under a new changelog heading.)

Rewrites every tracked text file and renames every tracked path that carries the old name,
with its case variants:

    switchyard -> <slug>        (module path, folders, units, ids, URLs, package names)
    Switchyard -> <Display>     (prose; <Ident>, the display name without spaces, inside
                                 identifiers such as SwitchyardPlugins or Switchyard-desktop)
    SWITCHYARD -> <SLUG>        (environment variables)

and, with --cli, the command: the word sy (sy setup, ~/.local/bin/sy, cmd/sy, e.sy), sy-local
and SY_ (environment variables and their prefix).

What it never touches:

- lines that contain the marker `rename:keep`, and everything between `rename:keep-begin`
  and `rename:keep-end` (the legacy-name constants the migrations need, docs/spec/rename.md);
- CHANGELOG.md history: only the part above the first released version, and the
  `[Unreleased]:` link, are rewritten (with --reissue the whole file is);
- with --cli: sy_ (token prefixes, the session cookie), sy-<word> (protocol labels, file
  markers, Android modules) and JSON keys named "sy" (docs/spec/rename.md 2.12);
- KEEP_TOKENS (names that must stay as they are, e.g. SWITCHYARD_DATA, which `sy` asserts it
  never reads);
- the Android application id (io.github.mraj602.switchyard and its .dev variant) and the
  Kotlin packages dev.switchyard.* (and their java/dev/switchyard folders) unless
  --android-id new: a new id is a new app (docs/spec/rename.md 4);
- untracked files, binary files, node_modules, go.sum, this folder, and in npm lockfiles every
  line except the package's own "name" (integrity hashes stay byte for byte).

Python 3 standard library only. Run it from anywhere inside the repository. With --dry-run
it prints what it would do and changes nothing.
"""

from __future__ import annotations

import argparse
import os
import re
import shutil
import subprocess
import sys
from collections import Counter, defaultdict
from dataclasses import dataclass, field
from pathlib import Path

OLD_SLUG = "switchyard"
OLD_NAME = "Switchyard"

KEEP_MARK = "rename:keep"
KEEP_BEGIN = "rename:keep-begin"
KEEP_END = "rename:keep-end"

# Whole tokens that are kept as they are wherever they appear.
KEEP_TOKENS = [
    # paths.md: sy never reads SWITCHYARD_DATA (the prototype's variable); the tests assert it.
    "SWITCHYARD_DATA",
]

# Path prefixes (relative, POSIX) never rewritten or renamed.
SKIP_PREFIXES = (
    "scripts/rename/",
    "node_modules/",
)
SKIP_PARTS = {"node_modules", ".git"}
SKIP_FILES = {"go.sum"}
LOCKFILES = {"package-lock.json", "npm-shrinkwrap.json"}
BINARY_EXT = {
    ".png", ".jpg", ".jpeg", ".gif", ".ico", ".webp", ".mp4", ".webm", ".woff", ".woff2",
    ".ttf", ".otf", ".jks", ".keystore", ".apk", ".aab", ".zip", ".gz", ".tgz", ".pdf", ".bin",
}

SLUG_RE = re.compile(r"^[a-z][a-z0-9]{1,30}$")
CLI_RE = re.compile(r"^[a-z][a-z0-9-]{0,15}$")


@dataclass
class Names:
    old_slug: str
    old_name: str
    slug: str
    name: str
    android_id: str  # "keep" or "new"
    old_cli: str = "sy"
    cli: str = "sy"  # equal to old_cli: the command keeps its name

    @property
    def ident(self) -> str:
        return re.sub(r"[^A-Za-z0-9]", "", self.name)

    @property
    def upper(self) -> str:
        return self.slug.upper()

    @property
    def old_upper(self) -> str:
        return self.old_slug.upper()


@dataclass
class Report:
    files_changed: dict = field(default_factory=dict)  # path -> count
    by_variant: Counter = field(default_factory=Counter)
    renames: list = field(default_factory=list)  # (old, new)
    kept_lines: Counter = field(default_factory=Counter)  # path -> lines kept by marker
    skipped: list = field(default_factory=list)  # (path, why)


def git(root: Path, *args: str, check: bool = True) -> str:
    return subprocess.run(["git", "-C", str(root), *args], check=check, capture_output=True, text=True).stdout


def is_binary(path: Path) -> bool:
    if path.suffix.lower() in BINARY_EXT:
        return True
    try:
        with open(path, "rb") as f:
            return b"\0" in f.read(8192)
    except OSError:
        return True


def skip_reason(rel: str) -> str | None:
    if rel.startswith(SKIP_PREFIXES):
        return "tool folder or vendored"
    parts = rel.split("/")
    if SKIP_PARTS.intersection(parts):
        return "vendored"
    if parts[-1] in SKIP_FILES:
        return "checksums"
    return None


class Rewriter:
    def __init__(self, n: Names):
        self.n = n
        protect = [re.escape(t) for t in KEEP_TOKENS]
        if n.android_id == "keep":
            # The published Android id and its .dev/.widget(s) variants, but not the desktop
            # app's appId (io.github.mraj602.switchyard.desktop), which is renamed.
            protect.append(r"io\.github\.mraj602\." + re.escape(n.old_slug) + r"(?!\.desktop)(?:\.[a-z]+)*")
            # The Kotlin packages of the phone's native modules (dev.switchyard.link, ...). They
            # name the app's components, the WorkManager worker and broadcast actions that the
            # installed app has persisted, so they stay with the application id.
            protect.append(r"\bdev[./]" + re.escape(n.old_slug) + r"\b")
        if n.cli != n.old_cli:
            # JSON keys named after the command ("sy": the version in machine_info, the drain
            # hello, link frames, the hub export manifest, `sy desktop` output) are wire formats
            # older releases send and read.
            oc = re.escape(n.old_cli)
            protect.append(rf'json:"{oc}(?=[",])|"{oc}"(?=\s*:)|\'{oc}\'(?=\s*:)')
        self.protect_re = re.compile("|".join(protect)) if protect else None
        o, O, OU = re.escape(n.old_slug), re.escape(n.old_name), re.escape(n.old_upper)
        self.word_re = re.compile(rf"{OU}|{O}|{o}")

        self.cli_re = None
        if n.cli != n.old_cli:
            oc = re.escape(n.old_cli)
            # SY_ (environment variables and their prefix), sy-local (the plugin marketplace)
            # and the word sy (the command, its binary and paths, the code that names it). Not
            # sy_ (token prefixes, the session cookie, stored keys), nor sy-<word> (protocol
            # labels, file markers, the Android modules): those are formats existing installs
            # have stored (docs/spec/rename.md 2.12).
            # Release archives are <cli>_<version>_<os>_<arch>: sy_ before a version, a
            # placeholder, a glob or the code that builds or finds the name.
            arch = (rf"(?<![A-Za-z0-9_-]){oc}_(?=\$\{{|<v|\d|\*)|"
                    rf'(?<![A-Za-z0-9_-]){oc}_(?=["\']\s*\+\s*(?:v|ver|version|Version|r\.Version)\b)|'
                    rf"(?<=\^){oc}_(?=/)|(?<=#){oc}-(?=[a-z])|(?<=#){oc}_(?=\}})|(?i:(?<=startswith\(\'){oc}_(?=\'\)))")
            word_start = r"(?:(?<![A-Za-z0-9_-])|(?<=\\[nt]))"  # also right after a \n or \t escape
            self.cli_re = re.compile(rf"\b{re.escape(n.old_cli.upper())}_|{arch}|{word_start}{oc}-local\b|"
                                     rf"{word_start}{oc}(?![A-Za-z0-9_-])")

    def _sub_cli(self, m: re.Match, counts: Counter) -> str:
        t = m.group(0)
        n = self.n
        counts[t] += 1
        if t == n.old_cli + "_":
            return n.cli + "_"
        if t == n.old_cli + "-":  # a Markdown anchor of a heading that starts with the command
            return n.cli + "-"
        if t.endswith("_"):
            return n.cli.upper() + "_"
        if t.endswith("-local"):
            return n.cli + "-local"
        return n.cli

    def _seg(self, seg: str, s: str, counts: Counter) -> str:
        if self.n.old_slug in seg.lower():
            seg = self.word_re.sub(lambda m: self._sub_one(m, seg, counts), seg)
        if self.cli_re is not None:
            seg = self.cli_re.sub(lambda m: self._sub_cli(m, counts), seg)
        return seg

    def _sub_one(self, m: re.Match, s: str, counts: Counter) -> str:
        t = m.group(0)
        n = self.n
        if t == n.old_upper:
            counts[t] += 1
            return n.upper
        if t == n.old_slug:
            counts[t] += 1
            return n.slug
        # Display name: the plain name in prose, the identifier form when glued to
        # identifier characters or a hyphen.
        counts[t] += 1
        if n.ident == n.name:
            return n.name
        a, b = m.start(), m.end()
        glued = (a > 0 and re.match(r"[A-Za-z0-9_-]", s[a - 1])) or (b < len(s) and re.match(r"[A-Za-z0-9_-]", s[b]))
        return n.ident if glued else n.name

    def line(self, s: str, counts: Counter) -> str:
        low = s.lower()
        if self.n.old_slug not in low and (self.cli_re is None or self.n.old_cli not in low):
            return s
        # Split around protected spans and rewrite only the rest.
        out, pos = [], 0
        spans = list(self.protect_re.finditer(s)) if self.protect_re else []
        for p in spans:
            out.append(self._seg(s[pos:p.start()], s, counts))
            out.append(p.group(0))
            pos = p.end()
        out.append(self._seg(s[pos:], s, counts))
        return "".join(out)

    def path(self, rel: str) -> str:
        c = Counter()
        parts = rel.split("/")
        out = []
        for i, part in enumerate(parts):
            if self.n.android_id == "keep" and part == self.n.old_slug and i > 0 and parts[i - 1] == "dev":
                out.append(part)  # java/dev/switchyard/...: the Kotlin package stays (see above)
            else:
                out.append(self.line(part, c))
        return "/".join(out)


def rewrite_text(rel: str, text: str, rw: Rewriter, counts: Counter, kept: Counter, reissue: bool = False) -> str:
    lines = text.splitlines(keepends=True)
    base = rel.rsplit("/", 1)[-1]
    out = []
    in_keep = False
    changelog = base == "CHANGELOG.md" and not reissue
    in_history = False
    for ln in lines:
        if KEEP_BEGIN in ln:
            in_keep = True
        if changelog and re.match(r"^## \[\d", ln):
            in_history = True
        keep = in_keep or KEEP_MARK in ln
        if changelog and in_history and not ln.startswith("[Unreleased]:"):
            keep = True
        if base in LOCKFILES and not re.match(r'^\s*"name":', ln):
            keep = True
        if keep:
            if rw.n.old_slug in ln.lower():
                kept[rel] += 1
            out.append(ln)
        else:
            out.append(rw.line(ln, counts))
        if KEEP_END in ln:
            in_keep = False
    return "".join(out)


VERSIONED = {"app/package.json", "app/package-lock.json", "phone/package.json", "phone/package-lock.json"}


def bump_version(rel: str, text: str, version: str) -> str:
    """Sets the package's own version: the first "version" of package.json, and in a lockfile
    the top-level one and the root package's (packages."")."""
    if rel.endswith("package.json"):
        return re.sub(r'^(\s*"version":\s*")[^"]*(")', rf"\g<1>{version}\g<2>", text, count=1, flags=re.M)
    text = re.sub(r'^(  "version":\s*")[^"]*(")', rf"\g<1>{version}\g<2>", text, count=1, flags=re.M)
    return re.sub(r'(\n    "": \{\n(?:      [^\n]*\n)*?      "version":\s*")[^"]*(")', rf"\g<1>{version}\g<2>", text, count=1)


def changelog_note(text: str, n: Names, version: str | None = None, date: str | None = None) -> str:
    """Adds the rename note (once): under ## [Unreleased], or under a new ## [<version>] heading
    with its compare link when version is given."""
    cmd = f" The command is now `{n.cli}`; `{n.old_cli}` stays as a link to it for this release." if n.cli != n.old_cli else ""
    app = (" The Android app has a new application id: install it beside the old one, pair it, then "
           "remove the old app.") if n.android_id == "new" else ""
    note = (f"- The project is now called {n.name} (formerly {n.old_name}).{cmd}{app} Existing installs keep "
            f"their folders, services, plugin and data and move to the new names on `{n.cli} update` or "
            f"`{n.cli} setup`: see docs/spec/rename.md. <!-- {KEEP_MARK} -->\n")
    if note.split(" <!--")[0] in text:
        return text
    m = re.search(r"^## \[Unreleased\][^\n]*\n", text, re.M)
    if not m:
        return text
    if version:
        import datetime
        d = date or datetime.date.today().isoformat()
        head = f"\n## [{version}] - {d}\n\n### Changed\n\n"
        prev = re.search(r"^## \[(\d+\.\d+\.\d+)\]", text[m.end():], re.M)
        text = text[: m.end()] + head + note + text[m.end():]
        if prev:
            repo = f"https://github.com/mraj602/{n.slug}"
            text = re.sub(r"^\[Unreleased\]: [^\n]*$", f"[Unreleased]: {repo}/compare/v{version}...HEAD\n"
                          f"[{version}]: {repo}/compare/v{prev.group(1)}...v{version}", text, count=1, flags=re.M)
        return text
    head = "\n### Changed\n\n"
    rest = text[m.end():]
    # Reuse an existing "### Changed" directly under Unreleased.
    mc = re.match(r"\s*### Changed\s*\n\n", rest)
    if mc:
        return text[: m.end()] + rest[: mc.end()] + note + rest[mc.end():]
    return text[: m.end()] + head + note + rest


def builder_note(text: str, n: Names) -> str:
    """Makes the Linux packages of the new name replace the old one (docs/spec/rename.md 2.9).

    Without it a .deb or .rpm of the new name installs beside <old>-desktop. Added by the
    rename, not before: a package that conflicts with its own name is refused by rpm."""
    old = f"{n.old_slug}-desktop"
    if f"--replaces={old}" in text:
        return text
    flags = f'["--replaces={old}", "--conflicts={old}"]'
    return (text.rstrip("\n") + "\n\n"
            f"# The package of the name before the rename: this one replaces it instead of installing\n"
            f"# beside it (docs/spec/rename.md 2.9). {KEEP_BEGIN}\n"
            f"deb:\n  fpm: {flags}\nrpm:\n  fpm: {flags}\n# {KEEP_END}\n")


def tracked(root: Path) -> list[str]:
    out = git(root, "ls-files", "-z")
    return [p for p in out.split("\0") if p]


def run(root: Path, n: Names, dry: bool, fmt: bool = True, quiet: bool = False,
        version: str | None = None, date: str | None = None, reissue: bool = False) -> Report:
    rep = Report()
    rw = Rewriter(n)
    files = tracked(root)
    changed_go = []
    # 1. Text.
    for rel in files:
        why = skip_reason(rel)
        p = root / rel
        if why or p.is_symlink() or not p.is_file():
            if why and n.old_slug in rel.lower():
                rep.skipped.append((rel, why))
            continue
        if is_binary(p):
            try:
                if n.old_slug.encode() in p.read_bytes().lower():
                    rep.skipped.append((rel, "binary"))
            except OSError:
                pass
            continue
        try:
            text = p.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            rep.skipped.append((rel, "not UTF-8"))
            continue
        counts = Counter()
        new = rewrite_text(rel, text, rw, counts, rep.kept_lines, reissue)
        if rel == "CHANGELOG.md" and not reissue:
            new = changelog_note(new, n, version, date)
        if version and rel in VERSIONED:
            new = bump_version(rel, new, version)
        if rel == "app/electron-builder.yml":
            new = builder_note(new, n)
        if new != text:
            rep.files_changed[rel] = sum(counts.values())
            rep.by_variant.update(counts)
            if not dry:
                # Keep the file mode: write in place.
                with open(p, "w", encoding="utf-8", newline="") as f:
                    f.write(new)
            if rel.endswith(".go"):
                changed_go.append(rel)
    # 2. Paths (git mv), deepest first so a folder rename never strands a file.
    moves = []
    for rel in files:
        if skip_reason(rel):
            continue
        nrel = rw.path(rel)
        if nrel != rel:
            moves.append((rel, nrel))
    for old, new in moves:
        rep.renames.append((old, new))
        if dry:
            continue
        (root / new).parent.mkdir(parents=True, exist_ok=True)
        git(root, "mv", old, new)
    if not dry:
        # Remove folders the moves left empty.
        dirs = sorted({str(Path(o).parent) for o, _ in moves}, key=lambda d: -d.count("/"))
        for d in dirs:
            cur = root / d
            while cur != root and cur.is_dir() and not any(cur.iterdir()):
                cur.rmdir()
                cur = cur.parent
        if fmt and changed_go and shutil.which("gofmt"):
            # Renamed identifiers change struct-field alignment.
            go_files = [rw.path(g) for g in changed_go]
            subprocess.run(["gofmt", "-w", *go_files], cwd=root, check=True)
    return rep


def cli_report(root: Path, cli: str) -> int:
    """`sy` is more than a command name: print what renaming it would touch."""
    files = tracked(root)
    cats = {
        "command word in text (`sy ...`)": re.compile(r"(?<![\w./-])sy (?=[a-z])"),
        "SY_* environment variables": re.compile(r"\bSY_[A-Z_]+"),
        "token prefixes sy_<kind>_ (stored token hashes, every paired device)": re.compile(r"\bsy_[mdpbo]_"),
        "signed-protocol labels sy-*-v1 (link, keys, peers; on the wire)": re.compile(r"\bsy-(link|doc|owner|reg|recovery|peer)-v1"),
        "marketplace sy-local (every Claude config's settings.json)": re.compile(r"\bsy-local\b"),
        "Android modules sy-link/sy-notify/sy-scan/sy-tabs": re.compile(r"\bsy-(link|notify|scan|tabs)\b"),
        "binary path ~/.local/bin/sy, cmd/sy, dist/sy": re.compile(r"(\.local/bin/sy\b|cmd/sy\b|dist/sy\b)"),
    }
    hits = defaultdict(lambda: [0, set()])
    for rel in files:
        p = root / rel
        if skip_reason(rel) or not p.is_file() or is_binary(p):
            continue
        try:
            t = p.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        for k, rx in cats.items():
            c = len(rx.findall(t))
            if c:
                hits[k][0] += c
                hits[k][1].add(rel)
    print(f"--cli {cli}: renaming `sy` is not automated (docs/spec/rename.md 5). It touches:")
    for k in cats:
        c, fs = hits[k]
        print(f"  {c:6d} in {len(fs):4d} files  {k}")
    print("Only the command word and the binary path could change; the protocol labels, token\n"
          "prefixes, SY_* variables and sy-local must stay for existing installs. Keep `sy`, or\n"
          "plan the command rename as its own change with a `sy` alias.")
    return 2


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--to-slug", required=True, help="new lower-case name: [a-z][a-z0-9]{1,30}")
    ap.add_argument("--to-name", required=True, help="new display name, e.g. Zebrafin")
    ap.add_argument("--from-slug", default=OLD_SLUG)
    ap.add_argument("--from-name", default=OLD_NAME)
    ap.add_argument("--cli", help="new command name for sy: rewrites the command word, sy-local and SY_* "
                    "(docs/spec/rename.md 5)")
    ap.add_argument("--from-cli", default="sy")
    ap.add_argument("--cli-report", action="store_true", help="only print what renaming the command touches")
    ap.add_argument("--version", help="release version to bump app/ and phone/ to (package.json and lockfile), "
                    "with the changelog note under its own heading")
    ap.add_argument("--reissue", action="store_true", help="the released versions are published again under the "
                    "new name: rewrite the whole CHANGELOG, add no rename note, bump nothing")
    ap.add_argument("--date", help="release date for the changelog heading (default today)")
    ap.add_argument("--android-id", choices=["keep", "new"], default="keep",
                    help="keep the published Android application id (default) or change it (a new app: re-pair, reinstall)")
    ap.add_argument("--dry-run", action="store_true", help="print what would change; change nothing")
    ap.add_argument("--no-gofmt", action="store_true")
    ap.add_argument("--list", action="store_true", help="list every changed file and rename")
    ap.add_argument("--root", default=None, help="repository root (default: git rev-parse --show-toplevel)")
    a = ap.parse_args(argv)

    if not SLUG_RE.match(a.to_slug):
        ap.error("--to-slug must match [a-z][a-z0-9]{1,30} (it becomes a Go module path element, a Kotlin "
                 "package, an Android id part and an environment variable prefix)")
    if not a.to_name.strip() or not re.match(r"^[A-Za-z]", a.to_name):
        ap.error("--to-name must start with a letter")
    if a.to_slug == a.from_slug:
        ap.error("--to-slug is the current name")
    if a.to_slug in a.from_slug or a.from_slug in a.to_slug:
        ap.error("--to-slug must not contain the old name or be part of it: the migrations tell old "
                 "from new names by the slug they contain (docs/spec/rename.md 3)")
    root = Path(a.root) if a.root else Path(subprocess.run(
        ["git", "rev-parse", "--show-toplevel"], check=True, capture_output=True, text=True).stdout.strip())
    if a.cli and not CLI_RE.match(a.cli):
        ap.error("--cli must match [a-z][a-z0-9-]{0,15}")
    if a.cli and a.cli != a.from_cli and (a.cli in a.from_cli or a.from_cli in a.cli):
        ap.error("--cli must not contain the old command name or be part of it")
    if a.cli_report:
        return cli_report(root, a.cli or a.from_cli)
    if a.version and a.reissue:
        ap.error("--reissue and --version exclude each other")
    if a.version and not re.match(r"^\d+\.\d+\.\d+$", a.version):
        ap.error("--version must be MAJOR.MINOR.PATCH")
    if not a.dry_run and git(root, "status", "--porcelain", "--untracked-files=no").strip():
        print("rename: the working tree has uncommitted changes; commit or stash them first", file=sys.stderr)
        return 1

    n = Names(a.from_slug, a.from_name, a.to_slug, a.to_name, a.android_id, a.from_cli, a.cli or a.from_cli)
    rep = run(root, n, a.dry_run, fmt=not a.no_gofmt, version=a.version, date=a.date, reissue=a.reissue)

    verb = "would change" if a.dry_run else "changed"
    print(f"rename {n.old_name} -> {n.name} (slug {n.old_slug} -> {n.slug}, identifier {n.ident}, "
          f"env {n.old_upper} -> {n.upper}, command {n.old_cli} -> {n.cli}, Android id {a.android_id}"
          + (f", version {a.version}" if a.version else "") + (", reissue" if a.reissue else "") + ")")
    print(f"  {verb} {len(rep.files_changed)} files, {sum(rep.files_changed.values())} occurrences: "
          + ", ".join(f"{k} {v}" for k, v in sorted(rep.by_variant.items())))
    print(f"  {'would rename' if a.dry_run else 'renamed'} {len(rep.renames)} paths (git mv)")
    print(f"  kept {sum(rep.kept_lines.values())} lines in {len(rep.kept_lines)} files (rename:keep, CHANGELOG history, lockfiles)")
    for path, why in rep.skipped:
        print(f"  skipped {path} ({why})")
    if a.list:
        for f in sorted(rep.files_changed):
            print(f"    M {f} ({rep.files_changed[f]})")
        for o, nw in rep.renames:
            print(f"    R {o} -> {nw}")
        for f, c in sorted(rep.kept_lines.items()):
            print(f"    K {f} ({c} lines)")
    if not a.dry_run:
        print("next: review `git diff --stat -M`, run the checks in scripts/rename/RUNBOOK.md, then grep for leftovers:\n"
              f"  git grep -n -i {n.old_slug}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
