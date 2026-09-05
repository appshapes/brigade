#!/usr/bin/env python3
"""E5-soak engine (plan row P5-11, E2E-12 / E2E-13): the long-run interactive
pty session, the permanent draining wait, the beat submitter, the
`profile status` rotation poller, the roster poller, the `ps` sampler, the
local-stack provisioning of THREE member profiles (alice, bob, dana) and N
synthetic hook-registered senders per principal, the SQL instruments
(hints, counters), the secret scans and teardown.

It IMPORTS, never copies, the P4-5 rig (`scripts/experiments/E4-interactive/
{e4i,sender}.py`) and E0-8's `common.py` (the expect prelude with `mark`,
`xsend`, `nap`, `submit`, `onboard`, `canary`, `shutdown`; the by-prefix
environment strip; the config-dir resolution). Nothing under E4-interactive/,
E0-8/ or plugin/ is modified.

Deviations from `e4i.PtySession` (brief 4.3), each justified in
docs/experiments/E5-soak.md:

  1. THE PERMANENT DRAINING WAIT. `expect` drains the pty only while an
     `expect` command runs; a redrawing TUI that fills the 64 KB pty buffer
     makes `claude` block on write and silently stop processing input. A
     2 h idle session is 120 minutes of exactly that. Between beats the expect
     body sits in a `while` loop of `expect` commands with a DRAIN_SLICE-second
     timeout, emitting a `drain` mark per slice; never a `sleep`, never an
     unguarded `expect eof`. The proof that it worked is `session.log`'s
     growth curve (M6, and 4.6 item 8).
  2. DISABLE_AUTOUPDATER=1 is load-bearing (a self-update mid-soak voids the
     run); XDG_DATA_HOME is NOT overridden (E4's measured launcher hazard).
  3. Dialog policy: Escape everything, record it, keep going. A word match
     never presses a key by itself: every `approval`/`proceed` match is
     corroborated by E4's `bin/pending` (attempts minus executions); a
     corroborated dialog is Escaped, prose is ignored.
  4. Permissions: `default` mode with `Bash(brigade:*)` and `Skill`
     pre-approved (P4-3's allow-list) -- a rig choice so no beat of a 2 h run
     stalls on a dialog; labelled as such, never read as evidence about D20.
  5. Beats arrive through FILES (`beats/NNN.txt`, atomically renamed into
     place) that the expect loop picks up in order, so the driver never writes
     to the pty from Python and the prompt text never passes through Tcl
     quoting.
"""
import json
import os
import re
import secrets
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import uuid

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.realpath(__file__))
REPO = os.path.dirname(os.path.dirname(os.path.dirname(HERE)))
E4 = os.path.join(REPO, "scripts", "experiments", "E4-interactive")
E08 = os.path.join(REPO, "scripts", "experiments", "E0-8")
sys.path.insert(0, E4)
sys.path.insert(0, E08)
import common          # noqa: E402  (expect prelude, env strip, cfgdir)
import e4i             # noqa: E402  (settings_doc, PENDING, budgets' shape)
import sender as snd   # noqa: E402  (Stack: provisioning, senders, roster, scans, teardown)

PLUGIN = e4i.PLUGIN
BRIGADE = e4i.BRIGADE
PENDING = e4i.PENDING
HINTS_SQL = os.path.join(HERE, "hints.sql")
MARKER = "brigade-e5s-"
# The imported Stack builds its temp roots, the project-dir guard and the
# ~/.claude.json prune from sender.MARKER; rebinding the module attribute
# makes every one of them carry THIS experiment's marker (brief section 1)
# without copying a line of sender.py.
snd.MARKER = MARKER

DB_CONTAINER = "supabase_db_brigade"

# ---- budgets (brief 4.2): each a hang catcher, never a performance bound --- #
ONBOARD_TIMEOUT = 12   # per prompt, 6 rounds (E0-8/common.py `onboard`)
CANARY_TRIES = 3       # a canary failure aborts the run rather than scoring it
DRAIN_SLICE = 5        # the permanent draining wait's expect timeout -- hazard 1 of 4.3
BEAT_SETTLE = 35       # no new assistant record this long after a DONE stop = the beat finished (E4's 35 s)
BEAT_CAP = 180         # one beat's whole life; a beat that caps is recorded, not fatal
DIALOG_SETTLE = 3      # matched IMMEDIATELY after submit, never behind a drain (the loop's own expect is the match)
CANARY_TIMEOUT = 90    # the in-loop canary's expect timeout (a hang catcher; the model answers in ~10 s). The loop's own
                       # timeout is the 5 s drain slice and the canary must not inherit it (M7 of 2026-09-05: READY painted at
                       # 10 s, the canary had given up at 5 s)
SESSION_CAP = 8100     # 120 min + 15 min of slack; hang catcher only
HARD_TIMEOUT = 8400    # expect's own wall cap
POLL_INTERVAL = 30     # the profile-status rotation probe
PS_INTERVAL = 60       # the ps sampler
ROSTER_INTERVAL = 60   # the roster last_seen_at poll
HB_SQL_INTERVAL = 15   # the server's own last_seen_at by SQL (no adapter process): the heartbeat instrument's resolution
LOG_ROTATE_BYTES = 200 * 1000 * 1000   # rotate session.log past this (4.3 hazard 1)
LOG_KEEP_BYTES = 50 * 1000 * 1000      # a rotated generation is trimmed to its first 50 MB
DONE_STOPS = e4i.DONE_STOPS

# The soak's allow-list (brief 4.3 item 4): P4-3's row, a deliberate deviation
# from P4-5's Bash(sleep:*)-only list, stated as a rig choice.
SOAK_PERMISSIONS = {"allow": ["Bash(brigade:*)", "Skill"], "deny": [], "ask": []}


def now_ms():
    return int(time.time() * 1000)


def iso_now():
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def rand_hex(n):
    return secrets.token_hex(n)


def write_atomic(path, content):
    tmp = "%s.tmp-%s" % (path, rand_hex(4))
    with open(tmp, "w") as f:
        f.write(content)
    os.replace(tmp, path)


def append_ndjson(path, rec):
    with open(path, "a") as f:
        f.write(json.dumps(rec, sort_keys=True) + "\n")


def _w(path, content):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(content)


# --------------------------------------------------------------------------- #
# prompts (brief 4.4). No `"`, `[`, `$` or `\`: the setup prompt reaches the
# pty through a beat file, but the rule keeps every prompt Tcl-safe anyway.
# --------------------------------------------------------------------------- #
def setup_prompt(me, peer_name, peer_id):
    p = ("Soak rules for this session. You are %s in Brigade team ops; your teammate %s has session id %s. "
         "When a Brigade message arrives, reply with one short line via brigade send and nothing else. "
         "Run brigade only as the bare command brigade on PATH, never by absolute path. Never edit files; "
         "never run anything except brigade. Reply now with one short line."
         % (me, peer_name, peer_id))
    assert len(p) < 400, len(p)
    return p


def beat_prompt(peer_id, n, hexs):
    return ("Run: brigade sessions --json   Then run: brigade send %s with the body TICK-%d-%s on stdin "
            "(a heredoc). Then reply with one short line." % (peer_id, n, hexs))


PROBE_PROMPT = "Run: brigade whoami   Then reply with one short line."
# B1's responsiveness probe is an ORDINARY beat, judged from the transcript. It is not the split-token READY
# canary: measured 2026-09-05 (M7, the burst pilot), the THIRD READY canary of a session was refused by the
# provider's safeguard (`API Error: ... safeguards flagged this message ... [reasoning_extraction]`, 2 of 2),
# the first two never were (9 of 9), and every turn after a flag was refused too (4 of 4). The READY canary runs
# once per session, at setup.


def final_prompt(peer_id, hexs):
    return ("Run: brigade send %s with the body FINAL-%s on stdin (a heredoc). Then reply with one short line."
            % (peer_id, hexs))


# --------------------------------------------------------------------------- #
# Stack: alice + bob + dana on the local stack, N senders per principal
# --------------------------------------------------------------------------- #
class Stack(snd.Stack):
    """E4's Stack, generalised: THREE member profiles (dana is B2's second
    principal, brief 5.8), any number of synthetic hook-registered sender
    sessions on any principal, sink-mode watchers for the no-model
    measurements (M1, M3), the SQL instruments and the secret scans over all
    profiles."""

    PROFILES = ("alice", "bob", "dana")

    def __init__(self, run_id, log=print):
        super().__init__(run_id, log=log)
        self.senders = []          # [{profile, title, pid, session_id, sleeper}]
        self.sink_watchers = []    # [{profile, title, pid(sleeper), session_id, proc, sink, log}]
        self.cap = os.path.join(self.root, "cap")
        os.makedirs(self.cap, exist_ok=True)

    # ----- provisioning (three members; the secret through a pipe) ------- #
    def provision(self, with_sender=True):
        cap = self.cap
        self._adapter("alice", cap, "describe", "describe")
        for p in self.PROFILES:
            rc = self._adapter(p, cap, "profile-init-%s" % p, "profile", "init",
                               "--url", self.url, "--key", self.key)
            if rc == 9:
                raise SystemExit("local stack does not answer at %s: run `make supabase-start supabase-env`" % self.url)
            if rc != 0:
                raise SystemExit("profile init %s failed rc=%s" % (p, rc))
        secret = os.path.join(self.scratch, "ops.secret")   # OUTSIDE every scanned root
        doc = json.dumps({"team_name": "ops", "human_label": "alice@proof.invalid"})
        rc = self._adapter("alice", cap, "team-create", "team", "create", "--secret-file", secret, stdin=doc)
        if rc != 0:
            raise SystemExit("team create failed rc=%s" % rc)
        with open(os.path.join(cap, "team-create.json")) as f:
            self.ops_ref = json.load(f)["result"]["team_ref"]
        for p in ("bob", "dana"):
            # the secret goes from the 0600 file into `team join`'s stdin, never
            # a variable that outlives this block, never argv, never stdout
            with open(secret) as f:
                joindoc = json.dumps({"join_secret": f.read().strip(), "human_label": "%s@proof.invalid" % p})
            rc = self._adapter(p, cap, "team-join-%s" % p, "team", "join", stdin=joindoc)
            del joindoc
            if rc != 0:
                raise SystemExit("team join %s failed rc=%s" % (p, rc))
        os.remove(secret)
        if os.path.exists(secret):
            raise SystemExit("the join-secret file survived the joins")
        self.log("ok: provisioned alice+bob+dana in team ops (%s), secret deleted" % self.ops_ref)
        if with_sender:
            self._register_sender(cap)   # E4's payments-api on alice, kept for parity

    # ----- synthetic sender sessions on any principal --------------------- #
    def sender_env_for(self, profile, pid):
        return self._base_env({
            "XDG_CONFIG_HOME": self.xdg_config, "XDG_STATE_HOME": self.xdg_state,
            "XDG_DATA_HOME": self.xdg_data, "CLAUDE_CONFIG_DIR": self.claude_config,
            "CLAUDE_PID": str(pid), "CLAUDE_CODE_SESSION_ID": "e5s-sender-%s" % pid,
            "CLAUDECODE": "1", "CLAUDE_CODE_ENTRYPOINT": "cli",
            "CLAUDE_PLUGIN_OPTION_PROFILE": profile, "CLAUDE_PLUGIN_OPTION_TEAM_INBOUND": "accept",
        })

    def register_sender(self, profile, title):
        """A hook-registered session of `profile`'s principal with a sleeper as
        CLAUDE_PID and NO socket -> no watcher (sender.py:141-193, generalised
        to any principal). Returns the sender record."""
        sleeper = subprocess.Popen(["sleep", "100000"])
        pid = sleeper.pid
        doc = json.dumps({
            "session_id": "e5s-sender-%s" % pid, "cwd": self.root,
            "hook_event_name": "SessionStart", "transcript_path": "/never/read.jsonl",
            "source": "startup", "permission_mode": "default", "session_title": title,
        })
        out = subprocess.run([BRIGADE, "hook", "session-start"], input=doc, env=self.sender_env_for(profile, pid),
                             capture_output=True, text=True, timeout=60)
        _w(os.path.join(self.cap, "sender-start-%s-%s.out" % (profile, pid)), out.stdout + "\n---\n" + out.stderr)
        mp = os.path.join(self.bypid_dir, "%d.json" % pid)
        if not os.path.exists(mp):
            sleeper.terminate()
            raise SystemExit("the hook wrote no by-pid map for sender %s/%s (stderr: %s)" % (profile, title, out.stderr[:300]))
        with open(mp) as f:
            sid = json.load(f).get("brigade_session_id")
        if os.path.exists(os.path.join(self.state, "watchers", "%d.json" % pid)):
            raise SystemExit("a watcher was spawned for a sender (it has no socket)")
        rec = {"profile": profile, "title": title, "pid": pid, "session_id": sid, "sleeper": sleeper}
        self.senders.append(rec)
        return rec

    def send_from(self, snd_rec, recipient, body, summary=None, reply_to=None, timeout=120):
        """`brigade send` as a synthetic sender: the body through a 0600 file
        under the secret scratch (outside every scanned root), --json."""
        tmp = os.path.join(self.scratch, "body-%s-%s.txt" % (snd_rec["pid"], rand_hex(4)))
        _w(tmp, body)
        os.chmod(tmp, 0o600)
        argv = [BRIGADE, "send", recipient, "--body-file", tmp, "--json"]
        if summary:
            argv[3:3] = ["--summary", summary]
        if reply_to:
            argv[3:3] = ["--reply-to", reply_to]
        sent_ms = now_ms()
        r = subprocess.run(argv, env=self.sender_env_for(snd_rec["profile"], snd_rec["pid"]),
                           capture_output=True, text=True, timeout=timeout)
        rec = snd._sendrec(r, sent_ms)
        rec["sender"] = {"profile": snd_rec["profile"], "title": snd_rec["title"], "session_id": snd_rec["session_id"]}
        rec["recipient"] = recipient
        rec["body_sha12"] = _sha12(body)
        try:
            os.remove(tmp)
        except OSError:
            pass
        return rec

    # ----- a REAL watcher + adapter pair with no Claude (M1, M3) ---------- #
    def launch_sink_watcher(self, profile, title, log_level="debug"):
        """Register a session of `profile` through the hook (sleeper as
        CLAUDE_PID, no socket) and run the shipped `brigade watch --sink` for
        it with the hook-shaped environment (proof.sh's `watcher_env`: the six
        config.WatcherEnv names, the log level, the Claude config dir, NO
        messaging socket). The watcher spawns the real `adapter supabase
        message watch` child, joins the realtime topic and drains on hints --
        the pair the soak's hooks would spawn, minus the socket."""
        rec = self.register_sender(profile, title)
        sink = os.path.join(self.root, "sink-%s.ndjson" % rec["pid"])
        env = self._base_env({
            "HOME": os.environ.get("HOME", ""),
            "XDG_CONFIG_HOME": self.xdg_config, "XDG_STATE_HOME": self.xdg_state,
            "XDG_DATA_HOME": self.xdg_data, "CLAUDE_CONFIG_DIR": self.claude_config,
            "BRIGADE_CLAUDE_PID": str(rec["pid"]), "BRIGADE_PROFILE": profile,
            "BRIGADE_CONFIG_DIR": self.cfg, "BRIGADE_STATE_DIR": self.state,
            "BRIGADE_ADAPTER_COMMAND": "[]", "BRIGADE_TEAM_INBOUND": "accept",
            "BRIGADE_LOG_LEVEL": log_level,
        })
        env.pop("CLAUDE_CODE_MESSAGING_SOCKET", None)
        env.pop("CLAUDE_CODE_MESSAGING_TOKEN", None)
        out = open(os.path.join(self.cap, "watch-%s.stdout" % rec["pid"]), "w")
        err = open(os.path.join(self.cap, "watch-%s.stderr" % rec["pid"]), "w")
        proc = subprocess.Popen([BRIGADE, "watch", "--sink", sink, "--log-level", log_level],
                                stdin=subprocess.DEVNULL, stdout=out, stderr=err, env=env)
        rec.update({"proc": proc, "sink": sink,
                    "log": os.path.join(self.state, "logs", "watcher-%d.log" % rec["pid"]),
                    "adapter_log": os.path.join(self.state, "logs", "adapter-%s.log" % profile)})
        self.sink_watchers.append(rec)
        return rec

    def stop_sink_watcher(self, rec, timeout=15):
        p = rec.get("proc")
        if p and p.poll() is None:
            p.terminate()
            try:
                p.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                p.kill()
                p.wait(timeout=5)
        return p.returncode if p else None

    # ----- adapter one-shots ---------------------------------------------- #
    def adapter(self, profile, name, *args, stdin=None):
        """One adapter command, stdout/stderr captured to cap/<name>.{json,err};
        returns (rc, parsed-or-None)."""
        rc = self._adapter(profile, self.cap, name, *args, stdin=stdin)
        try:
            with open(os.path.join(self.cap, "%s.json" % name)) as f:
                return rc, json.load(f)
        except Exception:
            return rc, None

    def profile_status(self, profile, json_flag):
        """The rotation probe (brief M1, 4.6 item 1): `profile status` reads
        profile.json and session.json only -- no lock, no network, no refresh
        (profile.go identity()). Returns a record with token_expires_at and the
        exit status; never a token (the result carries none)."""
        argv = [BRIGADE, "adapter", "supabase", "--profile", profile, "profile", "status"]
        if json_flag:
            argv.append("--json")
        t0 = now_ms()
        r = subprocess.run(argv, env=self.terminal_env(), capture_output=True, text=True, timeout=60)
        rec = {"ms": t0, "t": iso_now(), "rc": r.returncode, "elapsed_ms": now_ms() - t0, "profile": profile,
               "json_flag": json_flag}
        try:
            d = json.loads(r.stdout)
            res = d.get("result") or {}
            rec["ok"] = d.get("ok")
            rec["state"] = res.get("state")
            rec["token_expires_at"] = res.get("token_expires_at")
            rec["principal_ref"] = res.get("principal_ref")
            rec["error_code"] = (d.get("error") or {}).get("code")
        except Exception:
            rec["ok"] = None
            rec["raw_head"] = r.stdout[:120]
        return rec

    def roster_snapshot(self, profile="bob"):
        rc, d = self.adapter(profile, "roster-%d" % now_ms(), "session", "list", "--include-offline")
        sessions = ((d or {}).get("result") or {}).get("sessions") or []
        return {"ms": now_ms(), "t": iso_now(), "rc": rc,
                "sessions": [{k: s.get(k) for k in ("session_id", "session_name", "state", "activity",
                                                     "last_seen_at", "lease_until", "inbound")} for s in sessions]}

    # ----- SQL instruments (docker exec psql, the house way) --------------- #
    def psql(self, sql, variables=None, timeout=120):
        """`docker exec -i supabase_db_brigade psql -U postgres -d postgres`:
        no host psql, no password on argv; the SQL on stdin; -At output.
        Returns (rc, stdout, stderr)."""
        argv = ["docker", "exec", "-i", DB_CONTAINER, "psql", "-U", "postgres", "-d", "postgres",
                "-At", "-F", "|", "-v", "ON_ERROR_STOP=1"]
        for k, v in (variables or {}).items():
            argv += ["-v", "%s=%s" % (k, v)]
        r = subprocess.run(argv, input=sql, capture_output=True, text=True, timeout=timeout)
        return r.returncode, r.stdout, r.stderr

    def psql_one(self, sql, variables=None):
        rc, out, err = self.psql(sql, variables)
        if rc != 0:
            return None
        lines = [x for x in out.split("\n") if x.strip()]
        return lines[-1] if lines else ""

    def hints(self, session_id, n, out_path):
        """The hint generator (brief 5.2, M3, B1): hints.sql through psql with
        the topic's session id and the count as psql variables. The rendered
        SQL and psql's output go to files; returns the parsed sent count."""
        with open(HINTS_SQL) as f:
            sql = f.read()
        rc, out, err = self.psql(sql, {"sid": session_id, "n": str(n)})
        _w(out_path, "rc=%d\n--- stdout ---\n%s\n--- stderr ---\n%s" % (rc, out, err))
        sent = None
        for line in out.split("\n"):
            if line.startswith("sent|"):
                try:
                    sent = int(line.split("|")[1])
                except ValueError:
                    pass
        return rc, sent, out

    def realtime_rows(self, session_id, since_iso=None):
        """Server-side count of realtime.send rows on the session's topic --
        the 1,000 of B1, counted where they land (realtime.messages)."""
        cond = ""
        if since_iso:
            cond = " and inserted_at >= '%s'::timestamptz" % since_iso
        return self.psql_one("select count(*) from realtime.messages where topic = 'brigade:session:' || :'sid'"
                             " and event = 'message_accepted'" + cond, {"sid": session_id})

    def inbox_state(self, session_id):
        """delivery_state histogram of the session's inbox rows."""
        rc, out, _ = self.psql("select delivery_state, count(*) from brigade.messages where recipient_session_id = :'sid'::uuid"
                               " group by 1 order by 1", {"sid": session_id})
        d = {}
        for line in out.split("\n"):
            if "|" in line:
                k, v = line.split("|", 1)
                d[k] = int(v)
        return d

    def message_rows(self, recipient, sender=None, since_iso=None):
        cond = ""
        if sender:
            cond += " and sender_session_id = '%s'::uuid" % sender
        if since_iso:
            cond += " and created_at >= '%s'::timestamptz" % since_iso
        rc, out, _ = self.psql("select id, sender_session_id, delivery_state, created_at from brigade.messages"
                               " where recipient_session_id = :'sid'::uuid" + cond + " order by seq",
                               {"sid": recipient})
        rows = []
        for line in out.split("\n"):
            if line.count("|") >= 3:
                i, s, st, c = line.split("|", 3)
                rows.append({"message_id": i, "sender_session_id": s, "delivery_state": st, "created_at": c})
        return rows

    def pss_calls(self, needle):
        """pg_stat_statements calls for statements containing `needle` (the
        server-side drain counter candidate of M3; attributable only when the
        stack is otherwise quiet, so the writeup records the background rate)."""
        v = self.psql_one("select coalesce(sum(calls),0) from pg_stat_statements where query ilike '%%' || :'q' || '%%'",
                          {"q": needle})
        try:
            return int(v)
        except (TypeError, ValueError):
            return None

    def refresh_family(self, principal_id):
        """M2's server-side rotation instrument: the auth.refresh_tokens rows of
        the principal's auth sessions, by session_id: count, revoked count,
        the newest row's revoked flag, and auth.sessions.refresh_token_counter.
        Column names verified on this build by `\\d auth.refresh_tokens`."""
        rc, out, err = self.psql(
            "select s.id, s.refresh_token_counter, count(r.id), count(r.id) filter (where r.revoked),"
            " max(r.updated_at), count(r.id) filter (where r.parent is not null)"
            " from auth.sessions s left join auth.refresh_tokens r on r.session_id = s.id"
            " where s.user_id = :'uid'::uuid group by s.id, s.refresh_token_counter order by s.id",
            {"uid": principal_id})
        rows = []
        for line in out.split("\n"):
            if line.count("|") >= 5:
                sid, ctr, n, rev, upd, par = line.split("|", 5)
                rows.append({"auth_session_id": sid, "refresh_token_counter": ctr, "rows": int(n),
                             "revoked_rows": int(rev), "max_updated_at": upd, "rows_with_parent": int(par)})
        return rows

    def heartbeat_rows(self, session_ids):
        """The server's own `brigade.sessions.last_seen_at` (and activity,
        closed_at) for the run's sessions, read by SQL: no adapter process,
        no RPC, nothing on the credential path. The watcher's `heartbeat`
        line is DEBUG and unreachable at the shipped level, and the 60 s
        roster poll cannot resolve a 30 s cadence; this can."""
        rc, out, _ = self.psql("select id, last_seen_at, activity, closed_at from brigade.sessions"
                               " where id = any(:'ids'::uuid[]) order by id",
                               {"ids": "{" + ",".join(session_ids) + "}"})
        rows = []
        for line in out.split("\n"):
            if line.count("|") >= 3:
                i, ls, act, cl = line.split("|", 3)
                rows.append({"session_id": i, "last_seen_at": ls, "activity": act, "closed_at": cl or None})
        return {"ms": now_ms(), "t": iso_now(), "rc": rc, "sessions": rows}

    def stack_uptime(self):
        """The database's start time (brief risk 4: a restart mid-run voids the
        soak) and the containers' status lines."""
        started = self.psql_one("select pg_postmaster_start_time()")
        r = subprocess.run(["docker", "ps", "--format", "{{.Names}} {{.Status}}", "--filter", "name=supabase_"],
                           capture_output=True, text=True, timeout=30)
        return {"pg_postmaster_start_time": started, "containers": sorted(r.stdout.strip().split("\n"))}

    # ----- ps sampling: -o pid,rss,%cpu,etime,args ONLY (never ps e/-E/eww) - #
    def ps_sample(self, cap_name=None):
        r = subprocess.run(["ps", "-A", "-o", "pid,rss,%cpu,etime,args"], capture_output=True, text=True)
        rows = []
        for line in r.stdout.split("\n")[1:]:
            parts = line.split(None, 4)
            if len(parts) < 5:
                continue
            pid, rss, cpu, etime, args = parts
            if not _is_ours(args):
                continue
            try:
                rows.append({"pid": int(pid), "rss_kb": int(rss), "cpu": float(cpu), "etime": etime, "args": args[:300]})
            except ValueError:
                continue
        if cap_name:
            # the E2E-14 shape: a full `ps -A -o args=` sample for the secret scans
            r2 = subprocess.run(["ps", "-A", "-o", "args="], capture_output=True, text=True)
            _w(os.path.join(self.cap, "ps-args-%s.txt" % cap_name), r2.stdout)
        return {"ms": now_ms(), "t": iso_now(), "rows": rows}

    # ----- the exact-token scan over ALL profiles (E4's, generalised) ------ #
    def exact_token_scan(self, scan_roots):
        pat = os.path.join(self.scratch, "patterns.txt")
        with open(pat, "w"):
            pass
        os.chmod(pat, 0o600)
        n = 0
        with open(pat, "a") as f:
            for p in self.PROFILES:
                sf = os.path.join(self.cfg, "profiles", p, "session.json")
                if os.path.exists(sf):
                    try:
                        d = json.load(open(sf))
                    except Exception:
                        d = {}
                    for k in ("refresh_token", "access_token"):
                        v = d.get(k)
                        if v:
                            f.write(v + "\n")
                            n += 1
            sentinel = "e5s-sentinel-" + rand_hex(12)
            f.write(sentinel + "\n")
        canary_dir = os.path.join(self.root, "canary")
        os.makedirs(canary_dir, exist_ok=True)
        planted = os.path.join(canary_dir, "planted-token.txt")
        _w(planted, "x" + sentinel + "x\n")
        os.chmod(planted, 0o600)

        def run_scan():
            hits = []
            for root in list(scan_roots) + [canary_dir]:
                if not os.path.isdir(root):
                    continue
                r = subprocess.run(["find", root, "-type", "f", "!", "-name", "session.json",
                                    "-exec", "grep", "-al", "-F", "-f", pat, "{}", "+"],
                                   capture_output=True, text=True, env={"LC_ALL": "C", "PATH": os.environ.get("PATH", "")})
                hits += [h for h in r.stdout.split("\n") if h.strip()]
            return hits

        ctrl = run_scan()
        control_ok = (ctrl == [planted])
        os.remove(planted)
        hits = run_scan()
        os.remove(pat)
        return {"patterns": n + 1, "control_found_only_canary": control_ok,
                "control_hits": ctrl, "hits_after_removal": hits, "clean": (hits == [])}

    # ----- teardown: senders, sink watchers, then E4's -------------------- #
    def teardown(self, scan_roots=None, bundle_cap=None):
        for rec in self.sink_watchers:
            self.stop_sink_watcher(rec)
        for rec in self.senders:
            mp = os.path.join(self.bypid_dir, "%d.json" % rec["pid"])
            if os.path.exists(mp):
                doc = json.dumps({"session_id": "e5s-sender-%s" % rec["pid"], "cwd": self.root,
                                  "hook_event_name": "SessionEnd", "transcript_path": "/never/read.jsonl",
                                  "reason": "exit"})
                try:
                    subprocess.run([BRIGADE, "hook", "session-end"], input=doc,
                                   env=self.sender_env_for(rec["profile"], rec["pid"]),
                                   capture_output=True, text=True, timeout=60)
                except Exception:
                    pass
            try:
                rec["sleeper"].terminate()
                rec["sleeper"].wait(timeout=10)
            except Exception:
                pass
        return super().teardown(scan_roots=scan_roots, bundle_cap=bundle_cap)


def _err_text(rec):
    c = ((rec or {}).get("message") or {}).get("content")
    if isinstance(c, str):
        return c
    if isinstance(c, list):
        return " ".join(b.get("text", "") for b in c if isinstance(b, dict))
    return ""


def _is_ours(args):
    return (("claude" in args and "--session-id" in args) or "brigade watch" in args
            or "brigade adapter supabase" in args or args.startswith("claude ") or args == "claude")


def _sha12(s):
    import hashlib
    return hashlib.sha256(s.encode()).hexdigest()[:12]


# --------------------------------------------------------------------------- #
# the long session's expect body
# --------------------------------------------------------------------------- #
BODY = r"""
# BEFORE spawn: a 24x80 pty wraps the canary token across a line [E3 defect 5].
set stty_init "rows 50 columns 200"

# rotate_log: session.log past LOGMAX bytes is closed, renamed to a numbered
# generation and reopened (4.3 hazard 1: budget the log; the driver trims a
# rotated generation to its first 50 MB, the live file is the tail).
proc rotate_log {results logmax} {
    set p "$results/session.log"
    if {[file exists $p] && [file size $p] > $logmax} {
        log_file
        set k 1
        while {[file exists "$p.$k"]} { incr k }
        file rename $p "$p.$k"
        log_file -a $p
        mark log_rotated k $k
    }
}

# soak_loop: THE PERMANENT DRAINING WAIT (brief 4.3, hazard 1). expect drains
# the pty only while an `expect` command runs, so between beats this loop IS
# the run: an `expect` with a slice-second timeout inside a `while` that
# re-arms, one `drain` mark per slice, never a `sleep`, never an unguarded
# `expect eof`. Beats arrive as files beats/NNN.txt (taken in order); a file
# whose first line is `canary <tail>` runs the prelude's split-token canary
# instead of a plain submit and records its round trip. Dialog policy: every
# `approval`/`proceed` match is corroborated by bin/pending (attempts minus
# executions); a corroborated dialog is Escaped and recorded, prose is
# ignored, and no word match ever presses Enter. The loop ends on the stop
# file (the driver) or on eof (the session died).
proc soak_loop {beatdir stopfile slice results state pending logmax} {
    global timeout
    set old $timeout
    set timeout $slice
    set n 0
    set escaped 0
    set beat 0
    while {![file exists $stopfile]} {
        set next [file join $beatdir [format "%03d.txt" [expr {$beat + 1}]]]
        if {[file exists $next]} {
            set f [open $next r]; set text [string trim [read $f]]; close $f
            incr beat
            if {[string match "canary *" $text]} {
                set tail [string trim [string range $text 7 end]]
                mark beat_submit n $beat kind canary
                set t0 [clock milliseconds]
                set timeout @CANARYTIMEOUT@
                set ok [canary $tail 1]
                set timeout $slice
                mark canary_beat n $beat ok $ok rtt [expr {[clock milliseconds] - $t0}]
            } else {
                mark beat_submit n $beat kind prompt
                submit $text "beat $beat"
            }
            catch {exec touch "$next.taken"}
            mark beat_submitted n $beat
        }
        expect {
            -re {approval|proceed} {
                set word $expect_out(0,string)
                set st "none"
                catch {set st [string trim [exec python3 $pending $state $escaped]]}
                if {$st eq "none"} {
                    mark prose_match_ignored word $word beat $beat
                } else {
                    catch {set f [open "$results/dialog-buffer.txt" a]; puts $f "=== dialog ($st; matched $word; beat $beat) ==="; puts $f $expect_out(buffer); close $f}
                    incr escaped
                    mark dialog_escaped kind $st word $word beat $beat n $escaped
                    xsend "\033" "escape a corroborated dialog (soak policy: escape everything)"
                    nap 1
                }
            }
            timeout {
                incr n
                mark drain n $n beat $beat
                if {$n % 12 == 0} { rotate_log $results $logmax }
            }
            eof { mark eof_in_soak_loop n $n beat $beat ; set timeout $old ; return 0 }
        }
    }
    set timeout $old
    return 1
}

mark spawn arm @ARM@ sid @SID@
spawn -noecho env @UNSETS@ @XDGENV@ claude --permission-mode @MODE@ --session-id @SID@ --plugin-dir "@PLUGIN@" --settings "@SETTINGS@"

onboard
if {![canary @TAIL@ @CANARYTRIES@]} { mark canary_failed ; catch {exec touch "@STOP@"} ; shutdown ; exit 0 }

# `env` execs claude in place, so the spawned pid IS CLAUDE_PID. SessionEnd
# DELETES the by-pid map, so read it live.
set cpid [exp_pid]
mark claude_pid pid $cpid
catch {exec cp "@BYPIDDIR@/$cpid.json" "@RESULTS@/bypid-live.json"} e
mark bypid_snapshot err "$e"

set alive [soak_loop "@BEATDIR@" "@STOP@" @SLICE@ "@RESULTS@" "@STATE@" "@PENDING@" @LOGMAX@]
mark soak_loop_done alive $alive att [ndcount "@ATT@"] exec [ndcount "@EXEC@"]
if {$alive} { shutdown } else { mark session_gone }
"""


class PtySoak:
    """One long interactive session: spawn, onboard, canary, then the
    permanent draining loop that takes beats from files until the driver
    touches the stop file. The Python side submits beats and judges their
    completion from the authoritative transcript (never from the pty)."""

    def __init__(self, stack, results, arm, profile="bob"):
        self.stack = stack
        self.results = results
        self.arm = arm
        self.profile = profile
        os.makedirs(results, exist_ok=True)
        self.state = os.path.join(results, "state")      # BRIGADE_E3_STATE (E4's detector hooks)
        shutil.rmtree(self.state, ignore_errors=True)
        os.makedirs(self.state)
        self.beatdir = os.path.join(results, "beats")
        os.makedirs(self.beatdir, exist_ok=True)
        self.sid = str(uuid.uuid4())
        self.stop = os.path.join(results, "stop")
        self.beats_log = os.path.join(results, "beats.ndjson")
        self.claude_pid = None
        self.brigade_id = None
        self.map = {}
        self.cwd = None
        self.how = None
        self.k = 0
        self.thread = None
        self.t_spawn_ms = None

    def start(self, permissions=None, extra_settings=None, mode="default"):
        self.cwd = tempfile.mkdtemp(prefix="%s%s-" % (MARKER, self.arm), dir=self.stack.root)
        setf = os.path.join(self.results, "settings.json")
        _w(setf, json.dumps(e4i.settings_doc(self.profile, permissions or SOAK_PERMISSIONS, extra_settings), indent=2))
        tail = secrets.token_hex(2).upper()
        env, stripped = common.nested_env({
            "BRIGADE_E3_STATE": self.state,
            "XDG_CONFIG_HOME": self.stack.xdg_config,
            "XDG_STATE_HOME": self.stack.xdg_state,
            # XDG_DATA_HOME deliberately NOT overridden (E4's launcher hazard);
            # DISABLE_AUTOUPDATER is load-bearing for a 2 h window (brief 4.3 item 2).
            "DISABLE_AUTOUPDATER": "1",
        })
        xdgenv = "XDG_CONFIG_HOME=%s XDG_STATE_HOME=%s" % (self.stack.xdg_config, self.stack.xdg_state)
        att = os.path.join(self.state, "attempts.ndjson")
        execf = os.path.join(self.state, "exec.ndjson")
        body = (BODY
                .replace("@ARM@", self.arm).replace("@SID@", self.sid)
                .replace("@UNSETS@", " ".join("-u " + v for v in stripped))
                .replace("@XDGENV@", xdgenv).replace("@MODE@", mode)
                .replace("@PLUGIN@", PLUGIN).replace("@SETTINGS@", setf)
                .replace("@TAIL@", tail).replace("@CANARYTRIES@", str(CANARY_TRIES))
                .replace("@BYPIDDIR@", self.stack.bypid_dir).replace("@RESULTS@", self.results)
                .replace("@STOP@", self.stop).replace("@BEATDIR@", self.beatdir)
                .replace("@SLICE@", str(DRAIN_SLICE)).replace("@STATE@", self.state)
                .replace("@PENDING@", PENDING).replace("@LOGMAX@", str(LOG_ROTATE_BYTES))
                .replace("@ATT@", att).replace("@EXEC@", execf)
                .replace("@CANARYTIMEOUT@", str(CANARY_TIMEOUT)))
        exp = common.write_expect(self.results, body)
        shutil.copy2(exp, os.path.join(self.results, "run.exp"))
        _w(os.path.join(self.results, "meta.json"), json.dumps({
            "arm": self.arm, "sid": self.sid, "profile": self.profile, "cwd": self.cwd,
            "stripped": stripped, "permissions": permissions or SOAK_PERMISSIONS, "mode": mode,
            "canary_tail_split": True}))
        self.t_spawn_ms = now_ms()

        def run():
            self.how = common.run_expect(exp, self.cwd, env, HARD_TIMEOUT, self.results)

        self.thread = threading.Thread(target=run, daemon=True)
        self.thread.start()

    # ----- readiness ------------------------------------------------------- #
    def marks(self):
        return common.read_ndjson(os.path.join(self.results, "marks.ndjson"))

    def has_mark(self, event, **kv):
        for m in self.marks():
            if m.get("event") == event and all(str(m.get(k)) == str(v) for k, v in kv.items()):
                return m
        return None

    def wait_ready(self, timeout=180):
        """Until the canary came back and the by-pid map was read live.
        Returns (ok, elapsed_ms)."""
        t0 = time.monotonic()
        while time.monotonic() - t0 < timeout:
            if self.has_mark("canary_failed") or os.path.exists(self.stop):
                return False, int((time.monotonic() - t0) * 1000)
            m = self.has_mark("claude_pid")
            lp = os.path.join(self.results, "bypid-live.json")
            if m and os.path.exists(lp):
                try:
                    self.claude_pid = int(m.get("pid"))
                    with open(lp) as f:
                        self.map = json.load(f)
                    self.brigade_id = self.map.get("brigade_session_id")
                    if self.brigade_id:
                        return True, int((time.monotonic() - t0) * 1000)
                except Exception:
                    pass
            if self.thread and not self.thread.is_alive():
                return False, int((time.monotonic() - t0) * 1000)
            time.sleep(0.5)
        return False, int((time.monotonic() - t0) * 1000)

    # ----- beats ----------------------------------------------------------- #
    def transcript_path(self):
        return e4i.rm.find_transcript(self.sid)

    def assistant_state(self):
        tr = self.transcript_path()
        if not tr or not os.path.exists(tr):
            return 0, None
        # e4i's counter (records, last stop_reason) does not use `self`.
        return e4i.PtySession._assistant_state(None, tr)

    def assistant_records_since(self, since_ms):
        """(real assistant records, provider-refused records) since since_ms.
        A provider refusal is an assistant record with isApiErrorMessage and
        stop_reason `refusal` (in e4i.DONE_STOPS, so the settle rule alone
        would call it answered -- it is not)."""
        import burst   # lazy: burst imports e5s
        recs = burst.transcript_records_since(self.transcript_path(), since_ms)
        ass = [r for r in recs if r.get("type") == "assistant"]
        errs = [r for r in ass if r.get("isApiErrorMessage")]
        real = [r for r in ass if not r.get("isApiErrorMessage")]
        return real, errs

    def provider_refusals_total(self):
        _, errs = self.assistant_records_since(0)
        return len(errs)

    def submit(self, text, kind, n=None, meta=None):
        """Queue one beat file; the expect loop submits it in order."""
        self.k += 1
        k = self.k
        write_atomic(os.path.join(self.beatdir, "%03d.txt" % k), text + "\n")
        rec = {"k": k, "kind": kind, "n": n, "queued_ms": now_ms(), "prompt_sha12": _sha12(text)}
        if meta:
            rec.update(meta)
        append_ndjson(self.beats_log, rec)
        return k

    def wait_submitted(self, k, timeout=60):
        t0 = time.monotonic()
        while time.monotonic() - t0 < timeout:
            m = self.has_mark("beat_submitted", n=k)
            if m:
                return int(m.get("ms"))
            if os.path.exists(self.stop):
                return None
            time.sleep(0.5)
        return None

    def beat(self, text, kind, n=None, cap=None, meta=None):
        """Submit a prompt and wait for the transcript to settle: at least one
        NEW assistant record after the submit, its stop_reason a DONE stop, and
        no further assistant record for BEAT_SETTLE seconds -- E4's turn
        monitor, with the cap recorded rather than fatal (brief 4.2)."""
        cap = cap or BEAT_CAP
        n_before, _ = self.assistant_state()
        esc_before = len([m for m in self.marks() if m.get("event") == "dialog_escaped"])
        k = self.submit(text, kind, n, meta)
        t_sub = self.wait_submitted(k)
        rec = {"k": k, "kind": kind, "n": n, "submitted_ms": t_sub, "assistant_before": n_before}
        if t_sub is None:
            rec.update({"outcome": "not_submitted"})
            append_ndjson(self.beats_log, rec)
            return rec
        t0 = time.monotonic()
        last = n_before
        last_grow = time.monotonic()
        outcome = "capped"
        while time.monotonic() - t0 < cap:
            if os.path.exists(self.stop):
                outcome = "stopped"
                break
            n_now, sr = self.assistant_state()
            if n_now != last:
                last = n_now
                last_grow = time.monotonic()
            if n_now > n_before and sr in DONE_STOPS and time.monotonic() - last_grow >= BEAT_SETTLE:
                outcome = "settled"
                break
            time.sleep(1)
        esc_after = len([m for m in self.marks() if m.get("event") == "dialog_escaped"])
        import burst   # lazy: burst imports e5s
        real, errs = self.assistant_records_since(t_sub)
        first_ms = min((burst._parse_iso_ms(r["timestamp"]) for r in real if r.get("timestamp")), default=None)
        if errs and not real:
            outcome = "provider_refusal"
        rec.update({"outcome": outcome, "settled_ms": now_ms(), "duration_ms": int((time.monotonic() - t0) * 1000),
                    "assistant_after": last, "new_assistant_records": last - n_before,
                    "dialogs_escaped_during": esc_after - esc_before,
                    "provider_refusals": len(errs), "first_assistant_ms": first_ms,
                    "rtt_first_ms": (first_ms - t_sub) if (first_ms and t_sub) else None,
                    "refusal_head": (_err_text(errs[0])[:160] if errs else None)})
        append_ndjson(self.beats_log, rec)
        return rec

    def probe(self, note):
        """B1's responsiveness probe: an ordinary beat (PROBE_PROMPT), its
        round trip the first real assistant record's timestamp minus the
        submit mark; ok only when it settled with no provider refusal."""
        r = self.beat(PROBE_PROMPT, "probe", meta={"note": note})
        return {"k": r.get("k"), "ok": r.get("outcome") == "settled" and not r.get("provider_refusals"),
                "rtt_ms": r.get("rtt_first_ms") if r.get("rtt_first_ms") is not None else -1,
                "settle_ms": r.get("duration_ms"), "outcome": r.get("outcome"),
                "provider_refusals": r.get("provider_refusals"), "submitted_ms": r.get("submitted_ms")}

    def canary_beat(self, timeout=90):
        """A split-token canary through the loop (B1's responsiveness probe);
        the round trip is measured by expect itself (mark canary_beat)."""
        tail = secrets.token_hex(2).upper()
        k = self.submit("canary " + tail, "canary")
        t0 = time.monotonic()
        while time.monotonic() - t0 < timeout:
            m = self.has_mark("canary_beat", n=k)
            if m:
                rec = {"k": k, "ok": str(m.get("ok")) == "1", "rtt_ms": int(m.get("rtt", -1)),
                       "mark_ms": int(m.get("ms"))}
                append_ndjson(self.beats_log, dict(rec, kind="canary_result"))
                return rec
            if os.path.exists(self.stop):
                break
            time.sleep(0.5)
        rec = {"k": k, "ok": False, "rtt_ms": -1, "timeout": True}
        append_ndjson(self.beats_log, dict(rec, kind="canary_result"))
        return rec

    # ----- evidence and stop ---------------------------------------------- #
    def session_log_bytes(self):
        total = 0
        for name in os.listdir(self.results):
            if name == "session.log" or re.match(r"^session\.log\.\d+$", name):
                total += os.path.getsize(os.path.join(self.results, name))
        return total

    def trim_rotated_logs(self):
        """Keep the first LOG_KEEP_BYTES of every rotated generation."""
        trimmed = []
        for name in os.listdir(self.results):
            if re.match(r"^session\.log\.\d+$", name):
                p = os.path.join(self.results, name)
                if os.path.getsize(p) > LOG_KEEP_BYTES:
                    with open(p, "r+b") as f:
                        f.truncate(LOG_KEEP_BYTES)
                    trimmed.append(name)
        return trimmed

    def snapshot_transcript(self):
        tr = self.transcript_path()
        dst = os.path.join(self.results, "transcript.jsonl")
        if tr and os.path.exists(tr):
            shutil.copy2(tr, dst)
            self.stack.note_project_dir(os.path.dirname(tr))
            return dst
        return None

    def watcher_log_path(self):
        return os.path.join(self.stack.state, "logs", "watcher-%s.log" % (self.claude_pid or ""))

    def pidfile_path(self):
        return os.path.join(self.stack.state, "watchers", "%s.json" % (self.claude_pid or ""))

    def stop_session(self, join_timeout=120):
        """Touch the stop file (the loop ends, `shutdown` runs /exit), wait for
        expect, SIGTERM the claude pid only if it is still alive."""
        if not os.path.exists(self.stop):
            open(self.stop, "w").close()
        if self.thread:
            self.thread.join(timeout=join_timeout)
        alive = False
        if self.claude_pid:
            try:
                os.kill(self.claude_pid, 0)
                alive = True
                os.kill(self.claude_pid, 15)
            except (ProcessLookupError, PermissionError):
                alive = False
        self.snapshot_transcript()
        wl = self.watcher_log_path()
        if os.path.exists(wl):
            shutil.copy2(wl, os.path.join(self.results, "watcher.log"))
        trimmed = self.trim_rotated_logs()
        rec = {"how": self.how, "claude_alive_at_stop": alive, "trimmed_generations": trimmed,
               "session_log_bytes": self.session_log_bytes()}
        _w(os.path.join(self.results, "stop.json"), json.dumps(rec, indent=2))
        return rec


# --------------------------------------------------------------------------- #
# Pollers: the profile-status rotation probe, the roster and the ps sampler
# --------------------------------------------------------------------------- #
class Pollers:
    def __init__(self, stack, evroot, profile="bob", json_flag=True, log=print, session_ids=None):
        self.stack = stack
        self.evroot = evroot
        self.profile = profile
        self.json_flag = json_flag
        self.log = log
        self.session_ids = list(session_ids or [])
        self._stop = threading.Event()
        self.threads = []
        self.status_path = os.path.join(evroot, "profile-status.ndjson")
        self.roster_path = os.path.join(evroot, "roster.ndjson")
        self.ps_path = os.path.join(evroot, "ps.ndjson")
        self.hb_path = os.path.join(evroot, "heartbeat-sql.ndjson")
        self.last_status = None

    def _loop(self, interval, fn):
        while not self._stop.is_set():
            try:
                fn()
            except Exception as e:  # noqa: BLE001
                self.log("FAIL: poller %s: %s: %s" % (fn.__name__, type(e).__name__, e))
            self._stop.wait(interval)

    def _status(self):
        rec = self.stack.profile_status(self.profile, self.json_flag)
        self.last_status = rec
        append_ndjson(self.status_path, rec)

    def _roster(self):
        append_ndjson(self.roster_path, self.stack.roster_snapshot(self.profile))

    def _ps(self):
        append_ndjson(self.ps_path, self.stack.ps_sample())

    def _hb_sql(self):
        if self.session_ids:
            append_ndjson(self.hb_path, self.stack.heartbeat_rows(self.session_ids))

    def start(self):
        os.makedirs(self.evroot, exist_ok=True)
        for interval, fn in ((POLL_INTERVAL, self._status), (ROSTER_INTERVAL, self._roster), (PS_INTERVAL, self._ps),
                             (HB_SQL_INTERVAL, self._hb_sql)):
            t = threading.Thread(target=self._loop, args=(interval, fn), daemon=True)
            t.start()
            self.threads.append(t)

    def stop(self):
        self._stop.set()
        for t in self.threads:
            t.join(timeout=90)

    def token_expires_at(self):
        r = self.last_status or {}
        return r.get("token_expires_at")


# --------------------------------------------------------------------------- #
# the secret scans (E2E-14, U-25, criterion 1), each with a positive control
# --------------------------------------------------------------------------- #
BRG1_SHAPE = r"brg1\.[^[:space:]\"'<>`.]+(\.[^[:space:]\"'<>`.]+)+"
JWT_TRIPLE = r"eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}"


def _find_grep(roots, args, excludes=()):
    hits = []
    for root in roots:
        if not os.path.isdir(root):
            continue
        argv = ["find", root, "-type", "f"]
        for ex in excludes:
            argv += ["!", "-name", ex]
        argv += ["-exec", "grep", "-al"] + args + ["{}", "+"]
        r = subprocess.run(argv, capture_output=True, text=True, env={"LC_ALL": "C", "PATH": os.environ.get("PATH", "")})
        hits += [h for h in r.stdout.split("\n") if h.strip()]
    return hits


def secret_scans(stack, roots, out_dir):
    """The three scans of proof-headless.sh:1413-1527 in Python: the join-secret
    SHAPE, the EXACT tokens of every profile, and the supply-chain shapes
    (`sb_secret_` assembled at run time, the JWT triple, `service_role`); each
    plants a canary, requires the scan to find ONLY it, removes it, and scans
    again. File names only, never contents. The ps samples are scanned by
    the same three."""
    os.makedirs(out_dir, exist_ok=True)
    canary_dir = os.path.join(stack.root, "canary")
    os.makedirs(canary_dir, exist_ok=True)
    roots = [r for r in roots if os.path.isdir(r)]
    report = {"roots": roots}

    # 1. the join-secret shape
    planted = os.path.join(canary_dir, "planted-brg1.txt")
    _w(planted, "brg1.aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.0123456789abcdef0123456789abcdef\n")
    ctrl = _find_grep(roots + [canary_dir], ["-E", "-e", BRG1_SHAPE])
    os.remove(planted)
    hits = _find_grep(roots + [canary_dir], ["-E", "-e", BRG1_SHAPE])
    report["join_secret_shape"] = {"control_hits": ctrl, "control_ok": ctrl == [planted], "hits": hits, "clean": hits == []}

    # 2. the exact tokens (all profiles) -- Stack.exact_token_scan
    report["exact_tokens"] = stack.exact_token_scan(roots)

    # 3. the supply-chain shapes; the canary VALUE is assembled at run time
    sb_secret = "sb_" + "secret_[A-Za-z0-9_-]{8,}"
    sb_canary = "sb" + "_sec" + "ret_0123456789abcdef"
    planted = os.path.join(canary_dir, "planted-sb.txt")
    _w(planted, sb_canary + "\n")

    def supply():
        a = _find_grep(roots + [canary_dir], ["-E", "-e", sb_secret, "-e", JWT_TRIPLE, "-e", "service_role"],
                       excludes=("session.json", "profile.json"))
        b = []
        for root in roots:
            r = subprocess.run(["find", root, "-type", "f", "(", "-name", "session.json", "-o", "-name", "profile.json", ")",
                                "-exec", "grep", "-alE", "-e", sb_secret, "-e", "service_role", "{}", "+"],
                               capture_output=True, text=True, env={"LC_ALL": "C", "PATH": os.environ.get("PATH", "")})
            b += [h for h in r.stdout.split("\n") if h.strip()]
        return a + b

    ctrl = supply()
    os.remove(planted)
    hits = supply()
    report["supply_chain"] = {"control_hits": ctrl, "control_ok": ctrl == [planted], "hits": hits, "clean": hits == []}

    # the ps samples, all three shapes
    ps_files = sorted(os.path.join(stack.cap, n) for n in os.listdir(stack.cap) if n.startswith("ps-args-"))
    ps_hits = {}
    if ps_files:
        r1 = subprocess.run(["grep", "-alE", "-e", BRG1_SHAPE] + ps_files, capture_output=True, text=True, env={"LC_ALL": "C", "PATH": os.environ.get("PATH", "")})
        r3 = subprocess.run(["grep", "-alE", "-e", sb_secret, "-e", JWT_TRIPLE, "-e", "service_role"] + ps_files, capture_output=True, text=True, env={"LC_ALL": "C", "PATH": os.environ.get("PATH", "")})
        ps_hits = {"join_secret_shape": [h for h in r1.stdout.split("\n") if h.strip()],
                   "supply_chain": [h for h in r3.stdout.split("\n") if h.strip()]}
    report["ps_samples"] = {"files": ps_files, "hits": ps_hits,
                            "clean": all(not v for v in ps_hits.values())}
    report["all_clean"] = (report["join_secret_shape"]["clean"] and report["exact_tokens"]["clean"]
                           and report["supply_chain"]["clean"] and report["ps_samples"]["clean"])
    report["all_controls_fired"] = (report["join_secret_shape"]["control_ok"]
                                    and report["exact_tokens"]["control_found_only_canary"]
                                    and report["supply_chain"]["control_ok"])
    _w(os.path.join(out_dir, "secret-scans.json"), json.dumps(report, indent=2))
    return report


# --------------------------------------------------------------------------- #
# preconditions (brief 4.2): die, exit 2, one stderr line
# --------------------------------------------------------------------------- #
def preconditions(need_claude=True):
    def die(msg):
        sys.stderr.write("e5s: %s\n" % msg)
        raise SystemExit(2)
    if not os.access(BRIGADE, os.X_OK):
        die("bin/brigade is not executable: run `make build`")
    for tool in ("jq", "pgrep", "expect", "python3", "docker"):
        if shutil.which(tool) is None:
            die("%s is not on PATH" % tool)
    r = subprocess.run(["docker", "ps", "--format", "{{.Names}}", "--filter", "name=%s" % DB_CONTAINER],
                       capture_output=True, text=True, timeout=30)
    if DB_CONTAINER not in r.stdout:
        die("container %s is not running: run `make supabase-start supabase-env`" % DB_CONTAINER)
    if not os.path.isfile(os.path.join(REPO, ".env.test")):
        die(".env.test is missing: run `make supabase-env`")
    if shutil.which("brigade") is not None:
        die("another `brigade` is on PATH (%s); remove it for the run" % shutil.which("brigade"))
    cfg = common.cfgdir()
    if not (os.path.isabs(cfg) and os.path.isdir(cfg)):
        die("CLAUDE_CONFIG_DIR (%s) is not an absolute directory" % cfg)
    version = None
    if need_claude:
        if shutil.which("claude") is None:
            die("claude is not on PATH")
        r = subprocess.run(["claude", "--version"], capture_output=True, text=True, timeout=60)
        version = r.stdout.strip()
        if not version:
            die("`claude --version` printed nothing")
    # no top-level crossSessionInbound other than accept in the three files the scan reads
    for p in (os.path.join(cfg, "settings.json"), os.path.join(REPO, ".claude", "settings.json"),
              os.path.join(REPO, ".claude", "settings.local.json")):
        try:
            with open(p) as f:
                v = json.load(f).get("crossSessionInbound")
            if v not in (None, "accept"):
                die("%s sets crossSessionInbound=%s; it would silence every injection" % (p, v))
        except (OSError, ValueError):
            pass
    return {"claude_version": version, "claude_cfg": cfg, "brigade": BRIGADE}


def claude_version():
    r = subprocess.run(["claude", "--version"], capture_output=True, text=True, timeout=60)
    return r.stdout.strip()


def now_stamp():
    return time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
