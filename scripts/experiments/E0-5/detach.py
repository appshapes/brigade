#!/usr/bin/env python3
"""E0-5 `detach` — the stand-in for `brigade watch`.

Behaves the way the real 6.6 watcher is specified to:

  * setsid() in-process, so it leaves the spawning hook's session and process
    group and is therefore not on the receiving end of the pty's SIGHUP when
    the interactive session goes away;
  * writes ${BRIGADE_STATE_DIR}/watchers/<claude_pid>.json with O_CREAT|O_EXCL
    and mode 0600;
  * polls every 2 s and exits when kill(CLAUDE_PID,0) gives ESRCH, or when the
    socket path is ENOENT;
  * on exit runs a stand-in `session close` (a timestamped record appended to
    session-close.ndjson) and then removes its pidfile;
  * writes one NDJSON line per state transition, with wall-clock and monotonic
    timestamps, so every latency below is measurable from the log alone.

Honesty notes for the measurement:
  * SIGHUP / SIGTERM / SIGINT are LOGGED and then re-raised with the default
    disposition. The handler never suppresses a signal, so "it survived" cannot
    be an artefact of this program ignoring the signal that was supposed to
    kill it.
  * --exit-delay inserts a pause between detecting the exit condition and
    running `session close`. It exists only so check (a) can photograph the
    process after the interactive session is gone; detection is still logged at
    the instant it happens. Runs that measure latency use --exit-delay 0.
"""

import argparse
import json
import os
import signal
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import wlib  # noqa: E402


class Log(object):
    def __init__(self, path):
        self.path = path
        self.t0 = time.monotonic()

    def __call__(self, event, **kw):
        rec = {"ts": wlib.iso(), "mono": round(time.monotonic() - self.t0, 3),
               "epoch": round(time.time(), 3), "event": event}
        rec.update(kw)
        with open(self.path, "a") as f:
            f.write(json.dumps(rec, sort_keys=True) + "\n")
            f.flush()
        return rec


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--state-dir", required=True)
    ap.add_argument("--claude-pid", required=True, type=int)
    ap.add_argument("--socket", required=True)
    ap.add_argument("--brigade-session-id", required=True)
    ap.add_argument("--token-sha256", required=True)
    ap.add_argument("--tag", default="")
    ap.add_argument("--poll", type=float, default=2.0)
    ap.add_argument("--exit-delay", type=float, default=0.0)
    ap.add_argument("--ready-file", default="")
    ap.add_argument("--spawned-by", default="")
    args = ap.parse_args()

    logdir = os.path.join(args.state_dir, "logs")
    os.makedirs(logdir, mode=0o700, exist_ok=True)
    log = Log(os.path.join(logdir, "watcher-%s-%d.ndjson" % (args.tag or "x", os.getpid())))

    pre = {"pid": os.getpid(), "ppid": os.getppid(), "pgid": os.getpgid(0),
           "sid": os.getsid(0)}
    log("pre_setsid", **pre)

    # ---- detach ----
    setsid_err = None
    try:
        os.setsid()
    except OSError as e:
        setsid_err = str(e)
        # Already a group leader: fall back to a fork so the child can setsid.
        if os.fork() > 0:
            os._exit(0)
        os.setsid()
    post = {"pid": os.getpid(), "ppid": os.getppid(), "pgid": os.getpgid(0),
            "sid": os.getsid(0)}
    log("post_setsid", setsid_err=setsid_err, **post)

    # stdio off the pty so a closing terminal cannot deliver EIO/SIGHUP either
    devnull = os.open(os.devnull, os.O_RDWR)
    os.dup2(devnull, 0)
    os.dup2(devnull, 1)
    os.dup2(devnull, 2)

    # ---- signals: log, then die the default death. Never suppressed. ----
    def handler(signum, frame):
        log("signal_received", signal=signal.Signals(signum).name)
        signal.signal(signum, signal.SIG_DFL)
        os.kill(os.getpid(), signum)

    for s in (signal.SIGHUP, signal.SIGTERM, signal.SIGINT):
        signal.signal(s, handler)

    # ---- pidfile ----
    path = wlib.pidfile_path(args.state_dir, args.claude_pid)
    tok = wlib.start_token(os.getpid())
    record = {
        "pid": os.getpid(),
        "start_token": tok,
        "brigade_session_id": args.brigade_session_id,
        "socket_path": args.socket,
        "token_sha256": args.token_sha256,
    }
    try:
        wlib.write_pidfile_excl(path, record)
        st = os.stat(path)
        log("pidfile_created", path=path, mode=oct(st.st_mode & 0o777), **record)
    except FileExistsError:
        log("pidfile_exists_abort", path=path)
        os._exit(3)

    if args.ready_file:
        with open(args.ready_file, "w") as f:
            json.dump({"watcher_pid": os.getpid(), "pidfile": path,
                       "start_token": tok, "ts": wlib.iso()}, f)

    log("poll_loop_start", poll_seconds=args.poll, claude_pid=args.claude_pid,
        socket=args.socket, exit_delay=args.exit_delay)

    # ---- poll ----
    reason = None
    detail = {}
    n = 0
    while reason is None:
        n += 1
        alive, err = wlib.kill0(args.claude_pid)
        sock_ok = os.path.exists(args.socket)
        log("poll", n=n, claude_alive=alive, kill0=err or "nil", socket_present=sock_ok)
        if not alive:
            reason = "claude_gone"
            detail = {"kill0": err}
        elif not sock_ok:
            reason = "socket_enoent"
            detail = {"socket": args.socket}
        else:
            time.sleep(args.poll)

    log("exit_condition_detected", reason=reason, polls=n, **detail)

    if args.exit_delay > 0:
        log("exit_delay_begin", seconds=args.exit_delay)
        time.sleep(args.exit_delay)
        log("exit_delay_end")

    # ---- stand-in `session close` ----
    log("session_close_begin", reason=reason)
    closelog = os.path.join(args.state_dir, "session-close.ndjson")
    with open(closelog, "a") as f:
        f.write(json.dumps({
            "ts": wlib.iso(), "epoch": round(time.time(), 3),
            "op": "session close", "reason": reason, "tag": args.tag,
            "brigade_session_id": args.brigade_session_id,
            "claude_pid": args.claude_pid, "watcher_pid": os.getpid(),
        }, sort_keys=True) + "\n")
        f.flush()
    log("session_close_done", reason=reason, log=closelog)

    # ---- remove the pidfile, but only if it is still OURS ----
    # A pidfile that has been replaced (see check (i)) now belongs to a newer
    # watcher; deleting it blind would orphan the live one.
    cur = wlib.read_pidfile(path)
    if cur is None:
        log("pidfile_remove_skipped", why="already gone", path=path)
    elif cur.get("pid") == os.getpid() and cur.get("start_token") == tok:
        os.unlink(path)
        log("pidfile_removed", path=path)
    else:
        log("pidfile_remove_skipped", why="pidfile no longer ours",
            path=path, found_pid=cur.get("pid"), our_pid=os.getpid())

    log("watcher_exit", reason=reason)
    os._exit(0)


if __name__ == "__main__":
    main()
