#!/usr/bin/env python3
"""PE0 lab helper (docs/TESTING.md, docs/spec/peers.md 13). Touches only /tmp/sy-lab and the registry file of one lab pid."""
import glob, json, os, socket, sys, uuid

ROOT, SOCKS, WORK = "/tmp/sy-lab", "/tmp/sy-lab/socks/", "/tmp/sy-lab/work"

def die(msg):
    sys.exit("lab: refused: " + msg)

def registry(pid):
    """The registry JSON of one lab session; refuses anything that is not a lab session."""
    if not pid.isdigit():
        die("pid must be digits")
    files = glob.glob(os.path.expanduser("~/.claude*/sessions/%s.json" % pid))
    if len(files) != 1:
        die("expected exactly one registry file for pid %s, found %d" % (pid, len(files)))
    with open(files[0]) as f:
        r = json.load(f)
    if not str(r.get("name", "")).startswith("lab-"):
        die("session name does not start with lab-")
    if os.path.realpath(str(r.get("cwd", ""))) != os.path.realpath(WORK):
        die("session cwd is not " + WORK)
    return r

def cmd_info(pid):
    r = registry(pid)
    print(json.dumps({k: r.get(k) for k in ("pid", "name", "cwd", "sessionId", "messagingSocketPath", "status")}, indent=1))

def cmd_frame(mode, session, text, frm="uds:/tmp/sy-lab/reply/r1.sock", hop=""):
    if mode == "plain":
        content = text
    else:
        attrs = ['from="%s"' % frm, 'from-session="lab-poster"']
        if hop:
            attrs.append('hop-chain="%s"' % hop)
        attrs.append('from-name="lab-poster"')
        if mode in ("bypass", "prompting"):
            attrs.append('from-mode="%s"' % mode)
        content = "<cross-session-message %s>\n%s\n</cross-session-message>" % (" ".join(attrs), text)
    f = {"msgV": 1, "msg_id": str(uuid.uuid4()), "type": "user"}
    if session != "-":
        f["session_id"] = session
    f.update({"message": {"role": "user", "content": content}, "priority": "next", "from": frm})
    print(json.dumps(f, ensure_ascii=False, separators=(",", ":")))

def cmd_post(sock, path, pid):
    if not os.path.realpath(sock).startswith(SOCKS):
        die("socket is not under " + SOCKS)
    r = registry(pid)
    if os.path.realpath(str(r.get("messagingSocketPath", ""))) != os.path.realpath(sock):
        die("socket is not that lab session's messagingSocketPath")
    data = open(path, "rb").read()
    s = socket.socket(socket.AF_UNIX); s.settimeout(5); s.connect(sock); s.sendall(data); s.close()
    print("posted %d bytes to %s" % (len(data), sock))

def cmd_listen(sock):
    if not os.path.realpath(os.path.dirname(sock)).startswith(ROOT):
        die("listen only under " + ROOT)
    if os.path.exists(sock):
        os.unlink(sock)
    s = socket.socket(socket.AF_UNIX); s.bind(sock); s.listen(5)
    print("listening on", sock, flush=True)
    while True:
        c, _ = s.accept()
        print(c.makefile().read(), flush=True)

if __name__ == "__main__":
    a = sys.argv[1:]
    try:
        {"info": cmd_info, "frame": cmd_frame, "post": cmd_post, "listen": cmd_listen}[a[0]](*a[1:])
    except (KeyError, IndexError, TypeError):
        sys.exit("usage: lab.py info PID | frame plain|bypass|prompting|none SESSION_ID|- TEXT [FROM [HOPS]] | post SOCKET FILE PID | listen SOCKET")
