#!/usr/bin/env python3
"""E0-5 shared library: pidfile format, start-time token, liveness.

Mirrors plan 6.6 / section 154:

  ${BRIGADE_STATE_DIR}/watchers/<claude_pid>.json   mode 0600, O_CREAT|O_EXCL
  {pid, start_token, brigade_session_id, socket_path, token_sha256}

  start_token  = `ps -o lstart= -p <pid>` (macOS, 1 s resolution), compared
                 BYTE FOR BYTE after a single normalisation: .strip().
  token_sha256 = hex SHA-256 of CLAUDE_CODE_MESSAGING_TOKEN, never the token.

  liveness(pid, stored_token) = kill(pid, 0) is nil AND start_token(pid) == stored
  ESRCH -> gone.  EPERM (process now owned by another user) -> treated as reuse.
"""

import datetime
import errno
import hashlib
import json
import os
import subprocess


def iso(t=None):
    if t is None:
        t = datetime.datetime.now()
    else:
        t = datetime.datetime.fromtimestamp(t)
    return t.astimezone().isoformat(timespec="milliseconds")


def start_token(pid):
    """The process START TIME as ps prints it. None when ps has no such pid."""
    r = subprocess.run(["ps", "-o", "lstart=", "-p", str(int(pid))],
                       capture_output=True, text=True)
    if r.returncode != 0:
        return None
    s = r.stdout.strip()
    return s or None


def kill0(pid):
    """(alive, errno_name). EPERM is reported, not swallowed."""
    try:
        os.kill(int(pid), 0)
        return True, None
    except OSError as e:
        if e.errno == errno.ESRCH:
            return False, "ESRCH"
        if e.errno == errno.EPERM:
            return False, "EPERM"
        return False, errno.errorcode.get(e.errno, str(e.errno))


def sha256_hex(s):
    return hashlib.sha256((s or "").encode()).hexdigest()


def watchers_dir(state_dir):
    d = os.path.join(state_dir, "watchers")
    os.makedirs(d, mode=0o700, exist_ok=True)
    return d


def pidfile_path(state_dir, claude_pid):
    return os.path.join(watchers_dir(state_dir), "%s.json" % claude_pid)


def write_pidfile_excl(path, record):
    """O_CREAT|O_EXCL, mode 0600. Raises FileExistsError when one is already there."""
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump(record, f, indent=2, sort_keys=True)
        f.write("\n")
    return path


def read_pidfile(path):
    try:
        with open(path) as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


def evaluate(entry):
    """The liveness routine under test. Returns a verbose decision dict."""
    if not entry:
        return {"verdict": "absent", "reason": "no pidfile"}
    pid = entry.get("pid")
    stored = entry.get("start_token")
    alive, err = kill0(pid)
    if not alive:
        return {"verdict": "dead", "reason": "kill(pid,0) -> %s" % err,
                "pid": pid, "kill0": err}
    if err == "EPERM":  # unreachable via kill0's contract, kept for symmetry
        return {"verdict": "dead", "reason": "EPERM: another user owns the pid -> reuse",
                "pid": pid, "kill0": err}
    current = start_token(pid)
    match = (current is not None and stored is not None and current == stored)
    return {
        "verdict": "alive" if match else "dead",
        "reason": ("kill0 ok and start_token matches byte for byte" if match else
                   "kill0 ok but start_token MISMATCH -> pid reuse"),
        "pid": pid, "kill0": "nil",
        "stored_start_token": stored,
        "current_start_token": current,
        "start_token_match": match,
    }
