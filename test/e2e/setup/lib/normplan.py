"""Normalise a `sy setup --dry-run --json` plan for comparison with a golden file.

    python3 -I normplan.py <home folder> <plan.json>

Prints canonical JSON: the fixture home replaced by $HOME everywhere, hashes of generated
content and the plan id dropped (they follow the sy version and the home's path), and only the
keys a golden should pin (mode, primary, keep, actions, conflicts, needs, summary).
"""
import json
import sys

home, path = sys.argv[1], sys.argv[2]
d = json.load(open(path))


def norm(v):
    if isinstance(v, str):
        return v.replace(home, "$HOME")
    if isinstance(v, list):
        return [norm(x) for x in v]
    if isinstance(v, dict):
        out = {}
        for k, x in v.items():
            if k in ("content_sha256", "plan_id"):
                out[k] = "<hash>"
            elif k == "note" and isinstance(x, str) and x.startswith("switchyard "):
                out[k] = "switchyard <version> (sy-local)"
            else:
                out[k] = norm(x)
        return out
    return v


keep = ("mode", "primary", "keep", "actions", "conflicts", "needs", "summary")
print(json.dumps(norm({k: d[k] for k in keep if k in d}), indent=1, sort_keys=True))
