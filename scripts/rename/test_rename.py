#!/usr/bin/env python3
"""Tests of rename.py: python3 -m unittest discover -s scripts/rename (make rename-test)."""

import os
import subprocess
import sys
import tempfile
import unittest
from collections import Counter
from pathlib import Path

sys.path.insert(0, os.path.dirname(__file__))
import rename  # noqa: E402

N = rename.Names("switchyard", "Switchyard", "zebrafin", "Zebrafin", "keep")
SPACED = rename.Names("switchyard", "Switchyard", "zebrafin", "Zebra Fin", "keep")


def line(s, n=N):
    return rename.Rewriter(n).line(s, Counter())


class LineTest(unittest.TestCase):
    def test_variants(self):
        self.assertEqual(line("github.com/mraj602/switchyard/internal/x"), "github.com/mraj602/zebrafin/internal/x")
        self.assertEqual(line("Switchyard hub (merged mode)"), "Zebrafin hub (merged mode)")
        self.assertEqual(line("SWITCHYARD_TRAY_ICON"), "ZEBRAFIN_TRAY_ICON")
        self.assertEqual(line("t.SwitchyardPlugins"), "t.ZebrafinPlugins")
        self.assertEqual(line("detectSwitchyard(ctx)"), "detectZebrafin(ctx)")

    def test_spaced_display_name(self):
        self.assertEqual(line("Open Switchyard to see this", SPACED), "Open Zebra Fin to see this")
        self.assertEqual(line("t.SwitchyardPlugins", SPACED), "t.ZebraFinPlugins")
        self.assertEqual(line("'Switchyard-desktop-box'", SPACED), "'ZebraFin-desktop-box'")

    def test_kept_tokens(self):
        self.assertEqual(line('t.Setenv("SWITCHYARD_DATA", "/x")'), 't.Setenv("SWITCHYARD_DATA", "/x")')

    def test_android_id(self):
        s = "package: io.github.mraj602.switchyard.dev; desktop io.github.mraj602.switchyard.desktop"
        self.assertEqual(line(s), "package: io.github.mraj602.switchyard.dev; desktop io.github.mraj602.zebrafin.desktop")
        new = rename.Names("switchyard", "Switchyard", "zebrafin", "Zebrafin", "new")
        self.assertEqual(line(s, new), "package: io.github.mraj602.zebrafin.dev; desktop io.github.mraj602.zebrafin.desktop")

    def test_path(self):
        p = "phone/modules/sy-link/android/src/main/java/dev/switchyard/link/A.kt"
        self.assertEqual(rename.Rewriter(N).path(p), p)
        self.assertEqual(rename.Rewriter(rename.Names("switchyard", "Switchyard", "zebrafin", "Zebrafin", "new")).path(p),
                         "phone/modules/sy-link/android/src/main/java/dev/zebrafin/link/A.kt")
        self.assertEqual(rename.Rewriter(N).path("plugin/switchyard/x.md"), "plugin/zebrafin/x.md")

    def test_kotlin_package_kept(self):
        self.assertEqual(line("import dev.switchyard.link.WidgetOpen; // Switchyard"), "import dev.switchyard.link.WidgetOpen; // Zebrafin")
        self.assertEqual(line("java/dev/switchyard/link/"), "java/dev/switchyard/link/")


W = rename.Names("switchyard", "Switchyard", "watchyard", "Watchyard", "new", "sy", "wy")


class CliTest(unittest.TestCase):
    def test_command_word(self):
        for a, b in [
            ("run `sy setup --yes` then sy doctor", "run `wy setup --yes` then wy doctor"),
            ('exec.Command(e.sy, "hook")', 'exec.Command(e.wy, "hook")'),
            ("~/.local/bin/sy and sy.exe in cmd/sy/", "~/.local/bin/wy and wy.exe in cmd/wy/"),
            ("SY_HOME=/x SY_STATE_DIR", "WY_HOME=/x WY_STATE_DIR"),
            ('strings.HasPrefix(k, "SY_")', 'strings.HasPrefix(k, "WY_")'),
            ("switchyard@sy-local", "watchyard@wy-local"),
            ('out := "home=/h\\nsy=0.4.0\\n"', 'out := "home=/h\\nwy=0.4.0\\n"'),
            ("sy_<ver>_linux_amd64.tar.gz sy_0.5.0_x sy_${ver}_$t sy_*.zip", "wy_<ver>_linux_amd64.tar.gz wy_0.5.0_x wy_${ver}_$t wy_*.zip"),
            ('name := "sy_" + v + "_linux"', 'name := "wy_" + v + "_linux"'),
            ('x + "  sy_" + version + "_darwin"', 'x + "  wy_" + version + "_darwin"'),
            ("([maintenance](docs/guide/maintenance.md#sy-update)) ([below](#sy-update))",
             "([maintenance](docs/guide/maintenance.md#wy-update)) ([below](#wy-update))"),
            ("n ~ /^sy_/ && x; version=${archive#sy_}; f.startsWith('sy_')", "n ~ /^wy_/ && x; version=${archive#wy_}; f.startsWith('wy_')"),
            ('return "sy_" + letter + "_" // sy_<kind>_ sy_<k>_ `^sy_[mdpbo]_`', 'return "sy_" + letter + "_" // sy_<kind>_ sy_<k>_ `^sy_[mdpbo]_`'),
            ('Sy string `json:"sy,omitempty"` // sy version', 'Sy string `json:"sy,omitempty"` // wy version'),
            ('{"kind":"agent","sy":"0.9.0"}', '{"kind":"agent","sy":"0.9.0"}'),
            ("info(url, { 'sy': '0.6.1' }); deps.sy", "info(url, { 'sy': '0.6.1' }); deps.wy"),
            # Stored formats stay: token prefixes, cookie, protocol labels, file markers, modules.
            ("sy_m_abc sy_session sy-link-v1 sy-launcher v1 phone/modules/sy-link easy busy",
             "sy_m_abc sy_session sy-link-v1 sy-launcher v1 phone/modules/sy-link easy busy"),
        ]:
            self.assertEqual(rename.Rewriter(W).line(a, Counter()), b)

    def test_cli_path(self):
        self.assertEqual(rename.Rewriter(W).path("cmd/sy/main.go"), "cmd/wy/main.go")
        self.assertEqual(rename.Rewriter(W).path("app/src/renderer/dev/mock-sy.js"), "app/src/renderer/dev/mock-sy.js")

    def test_version_bump(self):
        lock = '{\n  "name": "x",\n  "version": "1.0.0",\n  "packages": {\n    "": {\n      "name": "x",\n      "version": "1.0.0",\n      "license": "y"\n    },\n    "node_modules/a": {\n      "version": "1.0.0"\n    }\n  }\n}\n'
        out = rename.bump_version("app/package-lock.json", lock, "1.1.0")
        self.assertEqual(out.count('"1.1.0"'), 2)
        self.assertIn('"node_modules/a": {\n      "version": "1.0.0"', out)
        self.assertIn('"version": "1.1.0"', rename.bump_version("app/package.json", '{\n  "version": "1.0.0"\n}', "1.1.0"))

    def test_changelog_version(self):
        t = ("# Changelog\n\n## [Unreleased]\n\n## [1.0.0] - 2026-10-24\n\n- x\n\n"
             "[Unreleased]: https://github.com/mraj602/switchyard/compare/v1.0.0...HEAD\n"
             "[1.0.0]: https://github.com/mraj602/switchyard/releases/tag/v1.0.0\n")
        out = rename.changelog_note(t, W, "1.1.0", "2026-11-01")
        self.assertIn("## [1.1.0] - 2026-11-01\n\n### Changed\n\n- The project is now called Watchyard", out)
        self.assertIn("[1.1.0]: https://github.com/mraj602/watchyard/compare/v1.0.0...v1.1.0", out)
        self.assertIn("[1.0.0]: https://github.com/mraj602/switchyard/releases/tag/v1.0.0", out)


class TextTest(unittest.TestCase):
    def rw(self, rel, text):
        return rename.rewrite_text(rel, text, rename.Rewriter(N), Counter(), Counter())

    def test_keep_markers(self):
        t = ('a := "switchyard"\nLegacySlug = "switchyard" // rename:keep\n'
             "<!-- rename:keep-begin -->\nswitchyard\n<!-- rename:keep-end -->\nswitchyard\n")
        self.assertEqual(self.rw("x.go", t), 'a := "zebrafin"\nLegacySlug = "switchyard" // rename:keep\n'
                         "<!-- rename:keep-begin -->\nswitchyard\n<!-- rename:keep-end -->\nzebrafin\n")

    def test_changelog_history_kept(self):
        t = ("# Changelog\n\nAll changes to Switchyard.\n\n## [Unreleased]\n\n## [1.0.0] - 2026-10-24\n\n"
             "- Switchyard 1.0\n\n[Unreleased]: https://github.com/mraj602/switchyard/compare/v1.0.0...HEAD\n"
             "[1.0.0]: https://github.com/mraj602/switchyard/releases/tag/v1.0.0\n")
        out = self.rw("CHANGELOG.md", t)
        self.assertIn("All changes to Zebrafin.", out)
        self.assertIn("- Switchyard 1.0", out)
        self.assertIn("[Unreleased]: https://github.com/mraj602/zebrafin/compare", out)
        self.assertIn("[1.0.0]: https://github.com/mraj602/switchyard/releases/tag/v1.0.0", out)
        note = rename.changelog_note(out, N)
        self.assertIn("now called Zebrafin (formerly Switchyard)", note)
        self.assertEqual(rename.changelog_note(note, N), note)

    def test_changelog_reissue(self):
        t = ("# Changelog\n\nAll changes to Switchyard.\n\n## [Unreleased]\n\n## [1.0.0] - 2026-10-24\n\n"
             "- Switchyard 1.0\n\n[1.0.0]: https://github.com/mraj602/switchyard/releases/tag/v1.0.0\n")
        out = rename.rewrite_text("CHANGELOG.md", t, rename.Rewriter(N), Counter(), Counter(), reissue=True)
        self.assertNotIn("switchyard", out.lower())
        self.assertIn("- Zebrafin 1.0", out)
        self.assertIn("[1.0.0]: https://github.com/mraj602/zebrafin/releases/tag/v1.0.0", out)

    def test_lockfile_only_name(self):
        t = '{\n  "name": "switchyard-desktop",\n  "x": "sha512-switchyard"\n}\n'
        self.assertEqual(self.rw("app/package-lock.json", t), '{\n  "name": "zebrafin-desktop",\n  "x": "sha512-switchyard"\n}\n')


    def test_builder_note(self):
        out = rename.builder_note("linux:\n  target: [deb]\n", N)
        self.assertIn('deb:\n  fpm: ["--replaces=switchyard-desktop", "--conflicts=switchyard-desktop"]', out)
        self.assertEqual(rename.builder_note(out, N), out)
        # A second rename keeps the first block (keep markers) and adds nothing twice.
        self.assertEqual(self.rw("app/electron-builder.yml", out), out.replace("target: [deb]", "target: [deb]"))


class RepoTest(unittest.TestCase):
    def test_end_to_end_in_a_temp_repo(self):
        with tempfile.TemporaryDirectory() as d:
            r = Path(d)
            g = lambda *a: subprocess.run(["git", "-C", d, *a], check=True, capture_output=True)  # noqa: E731
            g("init", "-q")
            (r / "dev/switchyard/link").mkdir(parents=True)
            (r / "dev/switchyard/link/A.kt").write_text("package dev.switchyard.link\n")
            (r / "logo.png").write_bytes(b"\x89PNG\0switchyard")
            (r / "go.sum").write_text("github.com/mraj602/switchyard v1\n")
            g("add", ".")
            g("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "x")
            rep = rename.run(r, rename.Names("switchyard", "Switchyard", "zebrafin", "Zebrafin", "new"), dry=False, fmt=False)
            self.assertEqual(rep.renames, [("dev/switchyard/link/A.kt", "dev/zebrafin/link/A.kt")])
            self.assertEqual((r / "dev/zebrafin/link/A.kt").read_text(), "package dev.zebrafin.link\n")
            self.assertFalse((r / "dev/switchyard").exists())
            self.assertEqual((r / "logo.png").read_bytes(), b"\x89PNG\0switchyard")
            self.assertIn("switchyard", (r / "go.sum").read_text())
            self.assertIn(("logo.png", "binary"), rep.skipped)


if __name__ == "__main__":
    unittest.main()
