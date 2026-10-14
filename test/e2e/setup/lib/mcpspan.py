"""Helpers for the MCP scenario (S5), on `.claude.json` files, python3 stdlib only.

    mcpspan.py outside <file>          print the file with the top-level "mcpServers" value cut out
                                       (and the volatile numStartups counter zeroed): everything
                                       sy must leave byte-identical
    mcpspan.py servers <file>          print the mcpServers object, canonical JSON
    mcpspan.py set <file> <name> <json>   add or replace one server (re-serialises the file with
                                       2-space indent, as the fake claude does)
"""
import json
import re
import sys


def span(text):
    """Return (start, end) of the top-level mcpServers value, or None."""
    depth, i, n = 0, 0, len(text)
    while i < n:
        c = text[i]
        if c == '"':
            j = i + 1
            while text[j] != '"':
                j += 2 if text[j] == "\\" else 1
            if depth == 1 and text[i : j + 1] == '"mcpServers"':
                k = j + 1
                while text[k] in " \t\r\n:":
                    k += 1
                start = k
                d, k2 = 0, k
                while True:
                    ch = text[k2]
                    if ch == '"':
                        m = k2 + 1
                        while text[m] != '"':
                            m += 2 if text[m] == "\\" else 1
                        k2 = m
                    elif ch in "{[":
                        d += 1
                    elif ch in "}]":
                        d -= 1
                        if d == 0:
                            return start, k2 + 1
                    k2 += 1
            i = j + 1
            continue
        if c in "{[":
            depth += 1
        elif c in "}]":
            depth -= 1
        i += 1
    return None


cmd, path = sys.argv[1], sys.argv[2]
text = open(path).read()
if cmd == "outside":
    s = span(text)
    if s:
        text = text[: s[0]] + "<mcpServers>" + text[s[1] :]
    sys.stdout.write(re.sub(r'"numStartups":\s*\d+', '"numStartups": 0', text))
elif cmd == "servers":
    print(json.dumps(json.loads(text).get("mcpServers", {}), sort_keys=True))
elif cmd == "set":
    d = json.loads(text)
    d.setdefault("mcpServers", {})[sys.argv[3]] = json.loads(sys.argv[4])
    with open(path, "w") as f:
        json.dump(d, f, indent=2)
        f.write("\n")
else:
    sys.exit("unknown command " + cmd)
