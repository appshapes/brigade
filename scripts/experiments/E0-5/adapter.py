#!/usr/bin/env python3
"""E0-5 stand-in Brigade adapter for check (d).

A unix-socket server that answers a `session close` after --delay seconds. The
SessionEnd hook connects with a 1.0 s cap, so:

  --delay 0.10  -> the close COMPLETES inside the budget
  --delay 3.00  -> the cap fires; the hook records a clean lease expiry and
                   carries on instead of blocking the session's exit

Logs every accepted connection with the epoch it arrived and the epoch it
replied, so the hook's measurement can be corroborated from the other end.
"""

import argparse
import json
import os
import socket
import threading
import time


def serve(path, delay, log):
    try:
        os.unlink(path)
    except OSError:
        pass
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.bind(path)
    os.chmod(path, 0o600)
    s.listen(8)

    def handle(c):
        t0 = time.time()
        try:
            c.settimeout(10)
            data = c.recv(65536)
            time.sleep(delay)
            c.sendall(b'{"ok": true, "op": "session close"}\n')
            rec = {"received_epoch": round(t0, 4), "replied_epoch": round(time.time(), 4),
                   "delay": delay, "request": data.decode("utf-8", "replace").strip()[:400]}
        except Exception as e:
            rec = {"received_epoch": round(t0, 4), "error": str(e), "delay": delay}
        finally:
            try:
                c.close()
            except OSError:
                pass
        with open(log, "a") as f:
            f.write(json.dumps(rec, sort_keys=True) + "\n")
            f.flush()

    while True:
        c, _ = s.accept()
        threading.Thread(target=handle, args=(c,), daemon=True).start()


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--socket", required=True)
    ap.add_argument("--delay", type=float, default=0.1)
    ap.add_argument("--log", required=True)
    a = ap.parse_args()
    serve(a.socket, a.delay, a.log)
