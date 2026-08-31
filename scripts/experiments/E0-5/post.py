#!/usr/bin/env python3
"""E0-5 out-of-band poster for check (c).

Speaks the verified inbox socket protocol (auth line, then one user line) to a
nested session's messaging socket, using coordinates read from a 0600 scratch
file -- i.e. exactly the coordinates the WATCHER captured, at the moment it
captured them. Nothing is typed at the pty, so a message that produces a model
turn was DELIVERED, not merely echoed.

--coords selects which snapshot to use:
  the startup one (n=1) is the D9 question -- do the coordinates the watcher
  holds still work after /clear? -- and the post-clear one (n=2) is the control.

The raw token is read but never printed or logged; only its SHA-256 prefix.
"""

import argparse
import datetime
import hashlib
import json
import os
import socket
import sys
import time


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--coords", required=True, help="0600 JSON with socket+token")
    ap.add_argument("--body", required=True)
    ap.add_argument("--log", required=True)
    ap.add_argument("--label", default="")
    a = ap.parse_args()

    with open(a.coords) as f:
        c = json.load(f)
    sock, token = c.get("socket", ""), c.get("token", "")
    tsha = hashlib.sha256(token.encode()).hexdigest() if token else ""

    rec = {"ts": datetime.datetime.now().astimezone().isoformat(timespec="milliseconds"),
           "epoch": round(time.time(), 4), "label": a.label,
           "coords_file": os.path.basename(a.coords), "coords_n": c.get("n"),
           "coords_source": c.get("source"),
           "socket": sock, "token_sha12": tsha[:12],
           "socket_exists": os.path.exists(sock)}

    if not sock or not token:
        rec.update({"posted": False, "error": "missing socket or token"})
    else:
        auth = json.dumps({"type": "auth", "token": token}) + "\n"
        user = json.dumps({"type": "user",
                           "message": {"role": "user", "content": a.body}}) + "\n"
        try:
            s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            s.settimeout(5)
            s.connect(sock)
            s.sendall(auth.encode())
            s.sendall(user.encode())
            s.close()
            rec.update({"posted": True, "bytes": len(user.encode())})
        except Exception as e:
            rec.update({"posted": False, "error": "%s: %s" % (type(e).__name__, e)})

    with open(a.log, "a") as f:
        f.write(json.dumps(rec, sort_keys=True) + "\n")
    print(json.dumps(rec, sort_keys=True))
    return 0 if rec.get("posted") else 1


if __name__ == "__main__":
    sys.exit(main())
