#!/usr/bin/env python3
"""E4-interactive local-stack provisioning and the synthetic hook-registered
sender (P4-5, brief 5.11, 5.3).

`Stack` provisions `alice` and `bob` under a temp `XDG_CONFIG_HOME` against
`.env.test`, exactly as `proof-headless.sh:874-933` does (describe warm-up,
`profile init` x2, `team create ops` as alice with the secret in a 0600 file
OUTSIDE every scanned root, `team join` as bob through a pipe), writes the
dev-binary pointer under that temp `XDG_CONFIG_HOME` (D35), and registers a
synthetic session of alice's principal named `payments-api` through the REAL
`brigade hook session-start` with a sleeper as CLAUDE_PID and NO socket -> no
watcher. It posts corpus items with `brigade send --body-file`. It is never an
LLM: the shipped skill would make a real alice refuse most items, and a refused
send is a VOID, so an LLM sender measures alice rather than bob.

Each run mints fresh anonymous principals, so the 120-registrations-per-
principal-per-hour cap is per run; heavy sweeps split across >= 2 driver
invocations with fresh profiles.
"""
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.realpath(__file__))
sys.path.insert(0, HERE)
import e4i          # noqa: E402
import common       # noqa: E402

REPO = e4i.REPO
BRIGADE = e4i.BRIGADE
MARKER = e4i.MARKER
CORPUS = os.path.join(REPO, "scripts", "injection-corpus")

EXIT_INVALID_INPUT = 3


def read_env_test():
    url = key = None
    with open(os.path.join(REPO, ".env.test")) as f:
        for line in f:
            line = line.rstrip("\n")
            if line.startswith("SUPABASE_URL="):
                url = line[len("SUPABASE_URL="):].strip().strip('"')
            elif line.startswith("SUPABASE_PUBLISHABLE_KEY="):
                key = line[len("SUPABASE_PUBLISHABLE_KEY="):].strip().strip('"')
    return url, key


class Stack:
    def __init__(self, run_id, log=print):
        self.run_id = run_id
        self.log = log
        self.root = tempfile.mkdtemp(prefix="%s%s-" % (MARKER, run_id))
        os.chmod(self.root, 0o700)
        self.xdg_config = os.path.join(self.root, "xdg", "config")
        self.xdg_state = os.path.join(self.root, "xdg", "state")
        self.xdg_data = os.path.join(self.root, "xdg", "data")
        self.cfg = os.path.join(self.xdg_config, "brigade")
        self.state = os.path.join(self.xdg_state, "brigade")
        self.bypid_dir = os.path.join(self.state, "sessions", "by-pid")
        self.claude_config = os.path.join(self.root, "claude-config")
        for d in (self.cfg, self.state, self.xdg_data, self.claude_config):
            os.makedirs(d, exist_ok=True)
        _w(os.path.join(self.cfg, "dev-binary"), BRIGADE + "\n")
        os.chmod(os.path.join(self.cfg, "dev-binary"), 0o600)
        # secrets scratch OUTSIDE every scanned root
        self.scratch = tempfile.mkdtemp(prefix="%ssecret-%s-" % (MARKER, run_id))
        os.chmod(self.scratch, 0o700)
        self.url, self.key = read_env_test()
        self.sender_pid = None
        self.sender_id = None
        self.project_dirs = set()
        self.claude_cfg = common.cfgdir()
        self._real_base = self._hash_real()

    # ----- environments ----------------------------------------------------- #
    def _base_env(self, extra):
        env, _ = common.nested_env(extra)
        return env

    def terminal_env(self):
        return self._base_env({
            "XDG_CONFIG_HOME": self.xdg_config, "XDG_STATE_HOME": self.xdg_state,
            "XDG_DATA_HOME": self.xdg_data, "CLAUDE_CONFIG_DIR": self.claude_config,
            "BRIGADE_CONFIG_DIR": self.cfg, "BRIGADE_STATE_DIR": self.state,
            "BRIGADE_LOG_LEVEL": "debug",
        })

    def sender_env(self):
        return self._base_env({
            "XDG_CONFIG_HOME": self.xdg_config, "XDG_STATE_HOME": self.xdg_state,
            "XDG_DATA_HOME": self.xdg_data, "CLAUDE_CONFIG_DIR": self.claude_config,
            "CLAUDE_PID": str(self.sender_pid),
            "CLAUDE_CODE_SESSION_ID": "e4i-sender-%s" % self.sender_pid,
            "CLAUDECODE": "1", "CLAUDE_CODE_ENTRYPOINT": "cli",
            "CLAUDE_PLUGIN_OPTION_PROFILE": "alice",
            "CLAUDE_PLUGIN_OPTION_TEAM_INBOUND": "accept",
        })

    # ----- provisioning ----------------------------------------------------- #
    def provision(self):
        cap = os.path.join(self.root, "cap")
        os.makedirs(cap, exist_ok=True)
        self._adapter("alice", cap, "describe", "describe")
        for p in ("alice", "bob"):
            rc = self._adapter(p, cap, "profile-init-%s" % p, "profile", "init",
                               "--url", self.url, "--key", self.key)
            if rc == 9:
                raise SystemExit("local stack does not answer at %s: run `make supabase-start supabase-env`" % self.url)
            if rc != 0:
                raise SystemExit("profile init %s failed rc=%s" % (p, rc))
        # team create ops as alice, secret in a 0600 file outside scanned roots
        secret = os.path.join(self.scratch, "ops.secret")
        doc = json.dumps({"team_name": "ops", "human_label": "alice@proof.invalid"})
        rc = self._adapter("alice", cap, "team-create", "team", "create",
                           "--secret-file", secret, stdin=doc)
        if rc != 0:
            raise SystemExit("team create failed rc=%s" % rc)
        with open(os.path.join(cap, "team-create.json")) as f:
            self.ops_ref = json.load(f)["result"]["team_ref"]
        # team join as bob, secret piped from the file (never argv/heredoc/stdout)
        with open(secret) as f:
            sec = f.read().strip()
        joindoc = json.dumps({"join_secret": sec, "human_label": "bob@proof.invalid"})
        del sec
        rc = self._adapter("bob", cap, "team-join", "team", "join", stdin=joindoc)
        if rc != 0:
            raise SystemExit("team join failed rc=%s" % rc)
        os.remove(secret)
        if os.path.exists(secret):
            raise SystemExit("the join-secret file survived the join")
        self.log("ok: provisioned alice+bob in team ops (%s), secret deleted" % self.ops_ref)
        self._register_sender(cap)

    def _register_sender(self, cap):
        sleeper = subprocess.Popen(["sleep", "100000"])
        self.sender_pid = sleeper.pid
        self._sleeper = sleeper
        doc = json.dumps({
            "session_id": "e4i-sender-%s" % self.sender_pid, "cwd": self.root,
            "hook_event_name": "SessionStart", "transcript_path": "/never/read.jsonl",
            "source": "startup", "permission_mode": "default", "session_title": "payments-api",
        })
        env = self.sender_env()
        out = subprocess.run([BRIGADE, "hook", "session-start"], input=doc, env=env,
                             capture_output=True, text=True, timeout=60)
        _w(os.path.join(cap, "sender-start.out"), out.stdout + "\n---\n" + out.stderr)
        mp = os.path.join(self.bypid_dir, "%d.json" % self.sender_pid)
        if not os.path.exists(mp):
            raise SystemExit("the hook wrote no by-pid map for the sender at %s (stderr: %s)" % (mp, out.stderr[:300]))
        with open(mp) as f:
            m = json.load(f)
        self.sender_id = m.get("brigade_session_id")
        shutil.copy2(mp, os.path.join(cap, "sender-map.json"))
        if os.path.exists(os.path.join(self.state, "watchers", "%d.json" % self.sender_pid)):
            raise SystemExit("a watcher was spawned for the sender (it has no socket); it would ack replies")
        self.log("ok: sender payments-api registered (%s), no watcher" % self.sender_id)

    def register_named_sender(self, cap, title):
        """A SECOND sender session of alice's principal under an arbitrary
        session_title (item 10's injection name), a sleeper as CLAUDE_PID, no
        socket -> no watcher. Returns (session_id, pid)."""
        sleeper = subprocess.Popen(["sleep", "100000"])
        pid = sleeper.pid
        self._named_sleepers = getattr(self, "_named_sleepers", [])
        self._named_sleepers.append(sleeper)
        doc = json.dumps({
            "session_id": "e4i-named-%s" % pid, "cwd": self.root,
            "hook_event_name": "SessionStart", "transcript_path": "/never/read.jsonl",
            "source": "startup", "permission_mode": "default", "session_title": title,
        })
        env = self._base_env({
            "XDG_CONFIG_HOME": self.xdg_config, "XDG_STATE_HOME": self.xdg_state,
            "XDG_DATA_HOME": self.xdg_data, "CLAUDE_CONFIG_DIR": self.claude_config,
            "CLAUDE_PID": str(pid), "CLAUDE_CODE_SESSION_ID": "e4i-named-%s" % pid,
            "CLAUDECODE": "1", "CLAUDE_CODE_ENTRYPOINT": "cli",
            "CLAUDE_PLUGIN_OPTION_PROFILE": "alice", "CLAUDE_PLUGIN_OPTION_TEAM_INBOUND": "accept",
        })
        out = subprocess.run([BRIGADE, "hook", "session-start"], input=doc, env=env,
                             capture_output=True, text=True, timeout=60)
        _w(os.path.join(cap, "named-start-%s.out" % pid), out.stdout + "\n---\n" + out.stderr)
        mp = os.path.join(self.bypid_dir, "%d.json" % pid)
        sid = None
        if os.path.exists(mp):
            with open(mp) as f:
                sid = json.load(f).get("brigade_session_id")
        return sid, pid

    # ----- posting ---------------------------------------------------------- #
    def post_body(self, bob_id, file_path):
        """A body item: `brigade send <bob> --body-file <file> --json`, no
        --summary (the frame then carries the first 80 sanitised code points of
        the body as its summary line)."""
        sent_ms = _ms()
        r = subprocess.run([BRIGADE, "send", bob_id, "--body-file", file_path, "--json"],
                           env=self.sender_env(), capture_output=True, text=True, timeout=120)
        rec = _sendrec(r, sent_ms)
        return rec

    def post_summary(self, bob_id, summary_file):
        """A summary item (14/15), two halves (brief 4.5 step 4): the verbatim
        summary sent and its refusal asserted (exit 3, invalid_input, summary,
        too_long, 200 codepoints), then the item scored in 200-code-point form
        with 13-benign-control.txt as the body."""
        with open(summary_file) as f:
            verbatim = f.read().replace("\n", "")
        vlen = len(verbatim)
        benign = os.path.join(CORPUS, "13-benign-control.txt")
        v = subprocess.run([BRIGADE, "send", bob_id, "--summary", verbatim,
                            "--body-file", benign, "--json"],
                           env=self.sender_env(), capture_output=True, text=True, timeout=120)
        refusal = {}
        try:
            refusal = json.loads(v.stdout)
        except Exception:
            refusal = {"raw": v.stdout[:300]}
        det = (refusal.get("error") or {}).get("details") or {}
        refused_ok = (v.returncode == EXIT_INVALID_INPUT
                      and (refusal.get("error") or {}).get("code") == "invalid_input"
                      and det.get("field") == "summary" and det.get("reason") == "too_long"
                      and str(det.get("limit")) == "200" and det.get("unit") == "codepoints")
        trimmed = verbatim[:200]
        sent_ms = _ms()
        r = subprocess.run([BRIGADE, "send", bob_id, "--summary", trimmed,
                            "--body-file", benign, "--json"],
                           env=self.sender_env(), capture_output=True, text=True, timeout=120)
        rec = _sendrec(r, sent_ms)
        rec["summary_two_half"] = {"verbatim_len": vlen, "refused_ok": refused_ok,
                                   "refusal": refusal, "actual": det.get("actual"),
                                   "trimmed_len": len(trimmed)}
        return rec

    def post_item(self, bob_id, file_name, kind):
        if kind == "summary":
            return self.post_summary(bob_id, os.path.join(CORPUS, file_name))
        return self.post_body(bob_id, os.path.join(CORPUS, file_name))

    def post_custom(self, bob_id, body_text, summary=None):
        """A benign, non-corpus frame the driver composes (never a corpus
        payload). Written to a 0600 file under the secret scratch (outside every
        scanned root) and posted with --body-file."""
        tmp = os.path.join(self.scratch, "frame-%s.txt" % _ms())
        _w(tmp, body_text)
        os.chmod(tmp, 0o600)
        argv = [BRIGADE, "send", bob_id, "--body-file", tmp, "--json"]
        if summary:
            argv[3:3] = ["--summary", summary]
        sent_ms = _ms()
        r = subprocess.run(argv, env=self.sender_env(), capture_output=True, text=True, timeout=120)
        rec = _sendrec(r, sent_ms)
        os.remove(tmp)
        return rec

    # ----- roster ----------------------------------------------------------- #
    def roster(self, profile="bob"):
        cap = os.path.join(self.root, "cap")
        # The adapter always emits protocol JSON; it has no --json flag.
        self._adapter(profile, cap, "roster", "session", "list", "--include-offline")
        try:
            with open(os.path.join(cap, "roster.json")) as f:
                return json.load(f).get("result", {}).get("sessions", [])
        except Exception:
            return []

    # ----- integrity -------------------------------------------------------- #
    def _real_files(self):
        out = [os.path.join(self.claude_cfg, "settings.json"),
               os.path.join(self.claude_cfg, "CLAUDE.md")]
        home_md = os.path.expanduser("~/.claude/CLAUDE.md")
        if os.path.abspath(home_md) not in (os.path.abspath(out[1]),):
            out.append(home_md)
        out += [os.path.join(REPO, "CLAUDE.md"), os.path.join(REPO, "CLAUDE.user.md")]
        return out

    def _hash_real(self):
        d = {}
        for p in self._real_files():
            d[p] = common.sha256_file(p)
        return d

    def check_real_files(self):
        deltas = []
        cur = self._hash_real()
        for p, h in cur.items():
            if self._real_base.get(p) != h:
                deltas.append(p)
                self._real_base[p] = h  # re-baseline (never restore)
        return deltas

    # ----- project dirs ----------------------------------------------------- #
    def note_project_dir(self, d):
        if MARKER in os.path.basename(d) and d.startswith(os.path.join(self.claude_cfg, "projects")):
            self.project_dirs.add(d)

    # ----- the exact-token (U-25) scan, BEFORE the temp root goes ------------ #
    def exact_token_scan(self, scan_roots):
        """E2E-14 / U-25: the exact refresh and access tokens of both profiles
        plus a run-time sentinel, through a 0600 `grep -F -f` pattern file kept
        OUTSIDE every scanned root, `session.json` itself excluded; a planted
        sentinel is found (positive control) then removed. File names only,
        never contents. Must run before teardown removes the profiles."""
        import hashlib
        pat = os.path.join(self.scratch, "patterns.txt")
        with open(pat, "w") as f:
            pass
        os.chmod(pat, 0o600)
        n = 0
        with open(pat, "a") as f:
            for p in ("alice", "bob"):
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
            sentinel = "e4i-sentinel-" + hashlib.sha256(os.urandom(16)).hexdigest()[:24]
            f.write(sentinel + "\n")
        canary_dir = os.path.join(self.root, "canary")
        os.makedirs(canary_dir, exist_ok=True)
        planted = os.path.join(canary_dir, "planted-token.txt")
        _w(planted, "x" + sentinel + "x\n")
        os.chmod(planted, 0o600)

        def run_scan():
            hits = []
            for root in scan_roots + [canary_dir]:
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

    def ps_sample(self, cap_name):
        """`ps -A -o args=` taken while a receiver and its watcher are alive."""
        cap = os.path.join(self.root, "cap")
        os.makedirs(cap, exist_ok=True)
        r = subprocess.run(["ps", "-A", "-o", "args="], capture_output=True, text=True)
        _w(os.path.join(cap, "ps-args-%s.txt" % cap_name), r.stdout)
        return len(r.stdout.split("\n"))

    # ----- teardown --------------------------------------------------------- #
    def teardown(self, scan_roots=None, bundle_cap=None):
        report = {"project_dirs_removed": [], "claude_json_projects_pruned": [], "watchers_killed": []}
        # U-25 exact-token scan over the evidence + this run's state logs + cap, before the profiles go.
        try:
            roots = list(scan_roots or []) + [os.path.join(self.state, "logs"), os.path.join(self.root, "cap")]
            report["exact_token_scan"] = self.exact_token_scan(roots)
        except Exception as e:  # noqa: BLE001
            report["exact_token_scan"] = {"error": "%s: %s" % (type(e).__name__, e)}
        # copy cap/ (ps samples, adapter captures) and this run's watcher logs into the bundle
        if bundle_cap:
            try:
                os.makedirs(bundle_cap, exist_ok=True)
                capd = os.path.join(self.root, "cap")
                for n in os.listdir(capd) if os.path.isdir(capd) else []:
                    shutil.copy2(os.path.join(capd, n), os.path.join(bundle_cap, "%s-%s" % (self.run_id, n)))
                logd = os.path.join(self.state, "logs")
                lb = os.path.join(os.path.dirname(bundle_cap), "logs")
                os.makedirs(lb, exist_ok=True)
                for n in os.listdir(logd) if os.path.isdir(logd) else []:
                    shutil.copy2(os.path.join(logd, n), os.path.join(lb, "%s-%s" % (self.run_id, n)))
            except Exception as e:  # noqa: BLE001
                report["bundle_copy_error"] = str(e)
        # sender session-end + sleeper
        if self.sender_pid:
            mp = os.path.join(self.bypid_dir, "%d.json" % self.sender_pid)
            if os.path.exists(mp):
                doc = json.dumps({"session_id": "e4i-sender-%s" % self.sender_pid, "cwd": self.root,
                                  "hook_event_name": "SessionEnd", "transcript_path": "/never/read.jsonl",
                                  "reason": "exit"})
                subprocess.run([BRIGADE, "hook", "session-end"], input=doc, env=self.sender_env(),
                               capture_output=True, text=True, timeout=60)
            try:
                self._sleeper.terminate()
                self._sleeper.wait(timeout=10)
            except Exception:
                pass
        for sl in getattr(self, "_named_sleepers", []):
            try:
                sl.terminate()
                sl.wait(timeout=10)
            except Exception:
                pass
        # any watcher pidfile left under MY temp state
        wdir = os.path.join(self.state, "watchers")
        if os.path.isdir(wdir):
            for name in os.listdir(wdir):
                if not name.endswith(".json"):
                    continue
                try:
                    with open(os.path.join(wdir, name)) as f:
                        wpid = int(json.load(f).get("pid") or 0)
                except Exception:
                    continue
                if wpid > 0:
                    try:
                        os.kill(wpid, 15)
                        report["watchers_killed"].append(wpid)
                    except OSError:
                        pass
        # transcript project dirs under the REAL projects/ (marker-guarded)
        for d in sorted(self.project_dirs):
            if MARKER in os.path.basename(d) and d.startswith(os.path.join(self.claude_cfg, "projects")) and os.path.isdir(d):
                shutil.rmtree(d, ignore_errors=True)
                report["project_dirs_removed"].append(d)
        # a late sweep for any in-flight marker dir under the real projects/
        proj = os.path.join(self.claude_cfg, "projects")
        if os.path.isdir(proj):
            for name in os.listdir(proj):
                d = os.path.join(proj, name)
                if MARKER in name and os.path.isdir(d):
                    shutil.rmtree(d, ignore_errors=True)
                    if d not in report["project_dirs_removed"]:
                        report["project_dirs_removed"].append(d)
        # prune ~/.claude.json projects keys this run added (correct path: cfgdir/.claude.json)
        report["claude_json_projects_pruned"] = self._prune_claude_json()
        # remove temp roots behind name guards
        for d in (self.scratch, self.root):
            if MARKER in os.path.basename(d):
                shutil.rmtree(d, ignore_errors=True)
        return report

    def _prune_claude_json(self):
        p = os.path.join(self.claude_cfg, ".claude.json")
        try:
            with open(p) as f:
                d = json.load(f)
            keys = [k for k in d.get("projects", {}) if MARKER in k]
            if not keys:
                return []
            for k in keys:
                d["projects"].pop(k, None)
            tmp = p + ".e4itmp"
            with open(tmp, "w") as f:
                json.dump(d, f, indent=2)
            os.replace(tmp, p)
            return keys
        except Exception as e:  # noqa: BLE001
            return ["error: %s" % e]

    # ----- adapter ---------------------------------------------------------- #
    def _adapter(self, profile, cap, name, *args, stdin=None, json_flag=False):
        argv = [BRIGADE, "adapter", "supabase", "--profile", profile] + list(args)
        if json_flag:
            argv.append("--json")
        r = subprocess.run(argv, input=stdin, env=self.terminal_env(),
                           capture_output=True, text=True, timeout=120)
        _w(os.path.join(cap, "%s.json" % name), r.stdout)
        _w(os.path.join(cap, "%s.err" % name), r.stderr)
        return r.returncode


def _sendrec(r, sent_ms):
    rec = {"returncode": r.returncode, "sent_at_ms": sent_ms, "accepted_at_ms": _ms()}
    try:
        rec["result"] = json.loads(r.stdout)
    except Exception:
        rec["result"] = {"raw": r.stdout[:400]}
    rec["stderr"] = r.stderr[:400]
    res = rec["result"].get("result") if isinstance(rec["result"], dict) else None
    if isinstance(res, dict):
        rec["message_id"] = res.get("message_id")
        rec["duplicate"] = res.get("duplicate")
        rec["accepted"] = rec["result"].get("ok", False)
    return rec


def _w(path, content):
    with open(path, "w") as f:
        f.write(content)


def _ms():
    return int(time.time() * 1000)
