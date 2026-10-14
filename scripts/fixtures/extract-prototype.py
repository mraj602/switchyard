#!/usr/bin/env python3
"""Extract raw (UNREDACTED) prototype fixtures from a COPY of usage.db.

usage: python3 -I extract-prototype.py <usage.db copy> <out dir>
Never point this at the live db; copy usage.db, usage.db-wal and usage.db-shm first.
Output goes to <out>/prototype/*.jsonl, schema.sql, counts.txt and <out>/usage/*.json;
run redact.py over <out> before it goes anywhere near testdata/.
"""
import json, os, sqlite3, sys

db, out = sys.argv[1], sys.argv[2]
con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
con.row_factory = sqlite3.Row
os.makedirs(f"{out}/prototype", exist_ok=True)
os.makedirs(f"{out}/usage", exist_ok=True)

def rows(sql, *a):
    return [dict(r) for r in con.execute(sql, a)]

def dump(name, data):
    with open(f"{out}/prototype/{name}.jsonl", "w") as f:
        for r in data:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")

# schema + counts
with open(f"{out}/prototype/schema.sql", "w") as f:
    for (sql,) in con.execute("select sql from sqlite_master where sql is not null and name not like 'sqlite_%' order by rowid"):
        f.write(sql.rstrip(";") + ";\n")
with open(f"{out}/prototype/counts.txt", "w") as f:
    for (t,) in con.execute("select name from sqlite_master where type='table' and name not like 'sqlite_%' order by name").fetchall():
        f.write(f"{t}\t{con.execute(f'select count(*) from \"{t}\"').fetchone()[0]}\n")

# --- snapshots: raw claude.ai usage responses -------------------------------
def nonnull(raw):
    try:
        return sum(v is not None for v in json.loads(raw).values())
    except Exception:
        return 0

snaps = rows("select rowid rid, * from snapshots")
accts = sorted({s["account_id"] for s in snaps})
chosen = {}
for a in accts:
    mine = [s for s in snaps if s["account_id"] == a]
    chosen[("latest", a)] = max(mine, key=lambda s: s["ts"])
    chosen[("richest", a)] = max(mine, key=lambda s: (nonnull(s["raw"]), s["ts"]))
    chosen[("earliest", a)] = min(mine, key=lambda s: s["ts"])
    ig = [s for s in mine if json.loads(s["raw"]).get("iguana_necktie") is not None]
    if ig:
        chosen[("iguana", a)] = ig[-1]
    ex = [s for s in mine if (json.loads(s["raw"]).get("extra_usage") or {}).get("is_enabled")]
    if ex:
        chosen[("extra_usage", a)] = ex[-1]
    s7 = [s for s in mine if json.loads(s["raw"]).get("seven_day") is not None]
    if s7:
        chosen[("seven_day", a)] = s7[-1]
sel, seen = [], set()
for k, s in chosen.items():
    if s["rid"] in seen:
        continue
    seen.add(s["rid"]); sel.append((k, s))
manifest = []
for i, (k, s) in enumerate(sorted(sel, key=lambda x: (x[0][1], x[1]["ts"]))):
    fn = f"response-{i+1:02d}.json"
    with open(f"{out}/usage/{fn}", "w") as f:
        f.write(json.dumps(json.loads(s["raw"]), indent=2, ensure_ascii=False) + "\n")
    manifest.append({"file": fn, "ts": s["ts"], "account_id": s["account_id"], "why": k[0], "plan": s["plan"]})
with open(f"{out}/usage/index.json", "w") as f:
    json.dump(manifest, f, indent=2)
snap_rows = [{k: v for k, v in s.items() if k != "rid"} for _, s in sel]
dump("snapshots", snap_rows)
snap_ts = [(s["account_id"], s["ts"]) for _, s in sel]

# windows + breakdown for the chosen snapshots (same ts) so joins work
w, b = [], []
for a, ts in snap_ts:
    w += rows("select * from windows where account_id=? and ts=?", a, ts)
    b += rows("select * from breakdown where account_id=? and ts=?", a, ts)
dump("windows", w)
if not b:
    b = rows("select * from breakdown order by ts desc limit 8")
dump("breakdown", b)

# --- sessions: a few with events + tokens + limit hits --------------------------
good = [r["session_id"] for r in rows("""
  select s.session_id from sessions s
  where exists (select 1 from events e where e.session_id=s.session_id)
    and exists (select 1 from tokens t where t.session_id=s.session_id)
    and exists (select 1 from session_owner o where o.session_id=s.session_id)
  order by (select count(*) from tokens t where t.session_id=s.session_id) asc limit 40""")]
# prefer small sessions, and one per account
pick, used = [], set()
for sid in good:
    a = rows("select account_id from sessions where session_id=?", sid)[0]["account_id"]
    if a not in used:
        used.add(a); pick.append(sid)
if len(pick) < 3:
    pick += [s for s in good if s not in pick][: 3 - len(pick)]
hit = rows("select distinct session_id from limit_hits limit 1")
if hit and hit[0]["session_id"] not in pick:
    pick.append(hit[0]["session_id"])
ph = ",".join("?" * len(pick))
dump("sessions", rows(f"select * from sessions where session_id in ({ph})", *pick))
ev = rows(f"select * from events where session_id in ({ph}) order by ts limit 40", *pick)
ev += rows(f"select * from events where session_id not in ({ph}) and kind='permission' order by ts desc limit 2", *pick)
ev += rows(f"select * from events where session_id is null or session_id='' order by ts desc limit 2")
dump("events", ev)
tk = []
for sid in pick:
    tk += rows("select * from tokens where session_id=? order by seq limit 12", sid)
tk += rows("select * from tokens where sidechain=1 order by seq desc limit 3")
tk += rows("select * from tokens where thinking>0 order by seq desc limit 3")
dump("tokens", tk)
dump("session_dirs", rows(f"select * from session_dirs where session_id in ({ph})", *pick))
dump("session_owner", rows(f"select * from session_owner where session_id in ({ph})", *pick))
dump("limit_hits", rows(f"select * from limit_hits where session_id in ({ph})", *pick)
     + rows("select * from limit_hits order by ts desc limit 3"))
dump("accounts", rows("select * from accounts order by ord"))
dump("config_dirs", rows("select * from config_dirs order by dir"))
dump("files", rows("select * from files order by mtime desc limit 5"))
dump("kv", rows("select * from kv"))
print("ok", len(snap_rows), "snapshots", len(pick), "sessions")
