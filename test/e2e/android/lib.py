#!/usr/bin/env python3
"""Small parsers for test/e2e/android/run.sh (stdin in, text out; no network, no adb).

  lib.py notes PKG        dumpsys notification --noredact  ->  one line per notification of PKG:
                          channel<TAB>title<TAB>text<TAB>actions (comma separated)
  lib.py tap TEXT         uiautomator dump xml             ->  "x y" of the first node whose text or
                          content-desc equals TEXT (exit 1 when there is none)
  lib.py has TEXT         uiautomator dump xml             ->  exit 0 when some node's text or
                          content-desc contains TEXT
  lib.py phone PATH       GET /v1/devices JSON on stdin ->  PATH inside the first phone that is not revoked
  lib.py json PATH        JSON on stdin                    ->  the value at a dotted path (a.0.b), or "" when absent
"""
import json
import re
import sys
import xml.etree.ElementTree as ET


def notes(pkg: str) -> None:
    dump = sys.stdin.read()
    # Each record starts at "NotificationRecord(" and runs to the next one.
    parts = re.split(r"\n\s*(?=NotificationRecord\()", dump)
    for rec in parts:
        if f"pkg={pkg} " not in rec.split("\n", 1)[0]:
            continue
        ch = re.search(r"Notification\(channel=(\S+)", rec)
        title = re.search(r"android\.title=String \((.*)\)\s*$", rec, re.M)
        text = re.search(r"android\.text=String \((.*)\)\s*$", rec, re.M)
        acts = re.findall(r'^\s*\[\d\] "([^"]*)" ->', rec, re.M)
        print("\t".join([ch.group(1) if ch else "", title.group(1) if title else "", text.group(1) if text else "", ",".join(acts)]))


def _nodes(xml_text: str):
    start = xml_text.find("<?xml")
    if start < 0:
        start = xml_text.find("<hierarchy")
    try:
        root = ET.fromstring(xml_text[start:])
    except ET.ParseError:
        return
    for n in root.iter("node"):
        yield n


def _center(bounds: str):
    m = re.match(r"\[(\d+),(\d+)\]\[(\d+),(\d+)\]", bounds)
    x1, y1, x2, y2 = (int(g) for g in m.groups())
    return (x1 + x2) // 2, (y1 + y2) // 2


def tap(text: str) -> int:
    for n in _nodes(sys.stdin.read()):
        if n.get("text") == text or n.get("content-desc") == text:
            print("%d %d" % _center(n.get("bounds")))
            return 0
    return 1


def has(text: str) -> int:
    for n in _nodes(sys.stdin.read()):
        if text in (n.get("text") or "") or text in (n.get("content-desc") or ""):
            return 0
    return 1


def jpath(path: str, phone: bool = False) -> int:
    try:
        v = json.load(sys.stdin)
        if phone:
            v = next((d for d in v.get("devices", []) if d.get("kind") == "phone" and not d.get("revoked_at")), None)
    except ValueError:
        print("")
        return 0
    for k in path.split("."):
        if k == "":
            continue
        try:
            v = v[int(k)] if isinstance(v, list) else v[k]
        except (KeyError, IndexError, TypeError, ValueError):
            print("")
            return 0
    print(json.dumps(v) if isinstance(v, (dict, list)) else ("true" if v is True else "false" if v is False else "" if v is None else v))
    return 0


if __name__ == "__main__":
    cmd = sys.argv[1]
    if cmd == "notes":
        notes(sys.argv[2])
    elif cmd == "tap":
        sys.exit(tap(sys.argv[2]))
    elif cmd == "has":
        sys.exit(has(sys.argv[2]))
    elif cmd == "phone":
        sys.exit(jpath(sys.argv[2], phone=True))
    elif cmd == "json":
        sys.exit(jpath(sys.argv[2]))
    else:
        sys.exit("usage: lib.py notes PKG | tap TEXT | has TEXT | phone PATH | json PATH")
