#!/usr/bin/env python3
"""Turn capture-hook.sh logs into raw (UNREDACTED) hook fixtures.

usage: python3 -I collect-hooks.py <out dir> name=capture.jsonl [name=capture.jsonl ...]
Writes <out>/hooks/runs/<name>.jsonl (envelope lines {event, env, payload}),
<out>/hooks/by-event/<Event>.json (payload of the first occurrence) and
<out>/hooks/by-event/<Event>.<Tool>.json for tool events of a different tool,
plus <out>/hooks/env.json (env of the first line that has any).
"""
import json, os, sys, shutil

out = sys.argv[1]
os.makedirs(f"{out}/hooks/runs", exist_ok=True)
os.makedirs(f"{out}/hooks/by-event", exist_ok=True)
seen, env_done = set(), False
for arg in sys.argv[2:]:
    name, path = arg.split("=", 1)
    shutil.copy(path, f"{out}/hooks/runs/{name}.jsonl")
    for line in open(path):
        rec = json.loads(line)
        ev, p = rec["event"], rec["payload"]
        keys = [ev]
        if "tool_name" in p:
            keys.append(f"{ev}.{p['tool_name']}")
        for k in keys:
            if k in seen:
                continue
            seen.add(k)
            with open(f"{out}/hooks/by-event/{k}.json", "w") as f:
                json.dump(p, f, indent=2, ensure_ascii=False); f.write("\n")
        if rec.get("env") and not env_done:
            env_done = True
            json.dump(rec["env"], open(f"{out}/hooks/env.json", "w"), indent=2); 
print(sorted(seen))
