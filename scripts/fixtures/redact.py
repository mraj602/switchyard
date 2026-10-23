#!/usr/bin/env python3
"""Redact raw Switchyard fixtures. Run as:  python3 -I redact.py <raw dir> <out dir>

Reads every file under <raw dir>, writes a redacted copy under <out dir> with the same relative
paths. Nothing is read from outside <raw dir> except the machine hostname.

What it does
  * identity values are HARVESTED from the raw files themselves (by key name: emailAddress, email,
    accountUuid, organizationUuid, orgId, displayName, organizationName, account_id, label ...) and
    replaced everywhere with stable fakes, in order of first appearance (auth/ files are read first,
    so slot 1 -> user1 / Test User 1 / Org 1 ...). The same real value always maps to the same fake,
    in every file, so joins across files still work.
  * any other e-mail address -> userN@example.com
  * /home/<real user> -> /home/user, hostname -> host-1, area/company words -> area-a / area-b
  * token-looking strings (sk-ant-, gho_/ghp_/github_pat_, Bearer, JWTs, claude.ai session ids) and
    values under secret-like keys (token, cookie, authorization, sessionKey, signature ...) removed
  * under hooks/ and transcripts/: long free text (assistant text, prompts, tool output,
    thinking) cut to its first few words; JSON embedded in string values is redacted recursively
Idempotent: redacting already redacted output is a no-op.
"""
import json, os, re, socket, sys

SRC, DST = sys.argv[1], sys.argv[2]

EMAIL_KEYS = {"emailAddress", "email"}
ACCT_UUID_KEYS = {"accountUuid", "account_uuid"}
ORG_UUID_KEYS = {"organizationUuid", "orgId", "org_uuid"}
NAME_KEYS = {"displayName", "fullName"}
ORGNAME_KEYS = {"organizationName", "orgName", "org_name"}
ACCT_ID_KEYS = {"account_id"}
LABEL_ROWS = ("label",)  # accounts.jsonl label column
SECRET_KEY = re.compile(r"^(token|accesstoken|refreshtoken|idtoken|hooktoken|apikey|secret|clientsecret|password|"
                        r"cookie|cookies|setcookie|authorization|sessionkey|signature|credentials)$", re.I)
TEXT_DIRS = ("hooks", "transcripts")
# Area/company words come from scripts/fixtures/private-words.txt (gitignored), one per line;
# the Nth word becomes area-<letter N>.
_WF = os.path.join(os.path.dirname(os.path.abspath(__file__)), "private-words.txt")
_W = [w.strip() for w in open(_WF)] if os.path.exists(_WF) else []
WORDS = [(re.compile(re.escape(w), re.I), "area-" + chr(ord("a") + i))
         for i, w in enumerate(x for x in _W if x and not x.startswith("#"))]
TOKEN_RES = [
    (re.compile(r"sk-ant-[A-Za-z0-9_\-]{3,}"), "[REDACTED]"),
    (re.compile(r"gh[opsur]_[A-Za-z0-9]{10,}"), "[REDACTED]"),
    (re.compile(r"github_pat_[A-Za-z0-9_]{10,}"), "[REDACTED]"),
    (re.compile(r"(?i)\bBearer\s+[A-Za-z0-9._\-~+/=]{8,}"), "Bearer [REDACTED]"),
    (re.compile(r"eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{5,}"), "[REDACTED]"),
    (re.compile(r"(?i)(sessionKey|sk-ant-sid\d*)=[^;\s\"]+"), r"\1=[REDACTED]"),
    (re.compile(r"\b(session|cse)_[0-9A-Za-z]{16,}"), r"\1_REDACTED"),
]
EMAIL_RE = re.compile(r"[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}")

emails, acct_uuids, org_uuids, names, orgnames, acct_ids, labels, misc = {}, {}, {}, {}, {}, {}, {}, {}

def put(m, real, fake_fmt):
    if isinstance(real, str) and real and real not in m:
        m[real] = fake_fmt.format(n=len(m) + 1)

def walk_harvest(o):
    if isinstance(o, dict):
        if isinstance(o.get("accountUuid"), str):  # names follow their account number
            put(acct_uuids, o["accountUuid"], "aaaaaaaa-0000-4000-8000-{n:012d}")
            n = int(acct_uuids[o["accountUuid"]][-12:])
            for nk in NAME_KEYS:
                if isinstance(o.get(nk), str) and len(o[nk]) >= 4 and o[nk] not in names:
                    names[o[nk]] = "Test User %d" % n
        for k, v in o.items():
            if isinstance(v, str):
                if k in EMAIL_KEYS: put(emails, v, "user{n}@example.com")
                elif k in ACCT_UUID_KEYS: put(acct_uuids, v, "aaaaaaaa-0000-4000-8000-{n:012d}")
                elif k in ORG_UUID_KEYS: put(org_uuids, v, "bbbbbbbb-0000-4000-8000-{n:012d}")
                elif k in NAME_KEYS and len(v) >= 4: put(names, v, "Test User {n}")
                elif k in ORGNAME_KEYS: put(orgnames, v, "Org {n}")
                elif k in ACCT_ID_KEYS: put(acct_ids, v, "acct{n:04d}")
                elif k == "label" and "id" in o and "ord" in o: put(labels, v, "Account {n}")
                elif k == "WARP_TERMINAL_SESSION_UUID": misc[v] = "0" * 31 + "1"
            elif isinstance(v, (dict, list)):
                walk_harvest(v)
            if isinstance(v, str) and v[:1] in "{[" and len(v) > 2:
                try: walk_harvest(json.loads(v))
                except ValueError: pass
    elif isinstance(o, list):
        for x in o: walk_harvest(x)

def read_records(path):
    txt = open(path, encoding="utf-8").read()
    if path.endswith(".jsonl"):
        return [json.loads(l) for l in txt.splitlines() if l.strip()]
    if path.endswith(".json"):
        return json.loads(txt)
    return None

files = []
for root, _, fs in os.walk(SRC):
    for f in fs:
        p = os.path.join(root, f)
        files.append((os.path.relpath(p, SRC), p))
files.sort(key=lambda t: (0 if t[0].startswith("auth") else 1, t[0]))

for rel, p in files:
    rec = read_records(p)
    if rec is not None:
        walk_harvest(rec)

host = socket.gethostname()
home_user = os.path.basename(os.path.expanduser("~"))
fixed = {host: "host-1", host.split(".")[0]: "host-1"} if host else {}
fixed = {k: v for k, v in fixed.items() if len(k) >= 3 and k != home_user}  # hostname == username: the home rule wins
# one combined literal map, longest first, so "x's Organization" wins over the e-mail inside it
literals = {}
for m in (emails, acct_uuids, org_uuids, names, orgnames, acct_ids, labels, misc, fixed):
    literals.update(m)
for k in list(literals):  # real e-mail local parts alone (e.g. in display names)
    pass
LIT_RE = re.compile("|".join(re.escape(k) for k in sorted(literals, key=len, reverse=True))) if literals else None
PATH_RE = re.compile(r"(/home/|-home-)" + re.escape(home_user)) if home_user else None
HOME_RE = re.compile(re.escape(home_user)) if home_user else None
extra_emails = {}

def scrub(s):
    if PATH_RE: s = PATH_RE.sub(lambda m: m.group(1) + "user", s)
    if LIT_RE: s = LIT_RE.sub(lambda m: literals[m.group(0)], s)
    def em(m):
        e = m.group(0)
        if e.endswith("@example.com") and re.fullmatch(r"user\d+@example.com", e): return e
        if e not in extra_emails:
            extra_emails[e] = "user%d@example.com" % (len(emails) + len(extra_emails) + 1)
        return extra_emails[e]
    s = EMAIL_RE.sub(em, s)
    for rx, rep in TOKEN_RES: s = rx.sub(rep, s)
    if HOME_RE: s = HOME_RE.sub("user", s)
    for rx, rep in WORDS: s = rx.sub(rep, s)
    return s

def looks_structural(s):
    return s[:1] in "/~" or re.match(r"^[a-z]+://", s) or " " not in s.strip()

def truncate(s, key):
    if len(s) <= 60 or looks_structural(s): return s
    w = s.split()
    return " ".join(w[:5]) + " ...[truncated]"

def redact(o, key, text_mode):
    if isinstance(o, dict):
        out = {}
        for k, v in o.items():
            nk = scrub(k)
            if SECRET_KEY.match(k) and isinstance(v, str) and v:
                out[nk] = "[REDACTED]"
            elif k in ("token", "tokens") and isinstance(v, str):
                out[nk] = "[REDACTED]"
            else:
                out[nk] = redact(v, k, text_mode)
        return out
    if isinstance(o, list):
        return [redact(x, key, text_mode) for x in o]
    if isinstance(o, str):
        if o[:1] in "{[" and len(o) > 2:
            try:
                inner = json.loads(o)
                return json.dumps(redact(inner, key, text_mode), ensure_ascii=False, separators=(",", ":"))
            except ValueError:
                pass
        o = scrub(o)
        return truncate(o, key) if text_mode else o
    return o

for rel, p in files:
    top = rel.split(os.sep)[0]
    text_mode = top in TEXT_DIRS and not rel.endswith((".sql", ".txt"))
    dst = os.path.join(DST, rel)
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    rec = read_records(p)
    if rec is None:
        data = scrub(open(p, encoding="utf-8").read())
        open(dst, "w", encoding="utf-8").write(data)
    elif p.endswith(".jsonl"):
        with open(dst, "w", encoding="utf-8") as f:
            for r in rec:
                f.write(json.dumps(redact(r, "", text_mode), ensure_ascii=False, separators=(",", ":")) + "\n")
    else:
        with open(dst, "w", encoding="utf-8") as f:
            json.dump(redact(rec, "", text_mode), f, indent=2, ensure_ascii=False); f.write("\n")
print("redacted %d files: %d emails, %d account uuids, %d org uuids, %d names, %d org names, %d account ids"
      % (len(files), len(emails), len(acct_uuids), len(org_uuids), len(names), len(orgnames), len(acct_ids)))
