"""E0-8 shared driver machinery.

Three things every run in this experiment needs, in one place:

  * ISOLATION -- the nested session's environment is stripped BY PREFIX
    (everything starting with CLAUDE, plus CLAUDECODE and AI_AGENT), keeping only
    CLAUDE_CONFIG_DIR, and the nested session's own identity is then asserted
    different from the outer harness session's. The eight-name allow-list the
    plan carries is known short, so nothing here enumerates names to strip.

  * CONFIG PROTECTION -- SHA-256 of the config dir's settings.json, the repo
    CLAUDE.md and ~/.claude/CLAUDE.md, taken before and re-verified after every
    run, restored from a snapshot on change and REPORTED either way.
    ~/.claude.json is never restored wholesale (the outer session writes it
    concurrently); only its `projects` key set and a couple of scalars are
    watched, and any drift is reported for a surgical revert.

  * EXPECT -- every wait is built out of `expect`, never `sleep`, because expect
    only drains the pty while an `expect` command is running and a TUI that fills
    the 64 KB pty buffer BLOCKS ON WRITE and stops processing input entirely.
"""
import hashlib
import json
import os
import shutil
import subprocess
import time

HERE = os.path.dirname(os.path.realpath(__file__))
PLUGIN = os.path.join(HERE, "plugin")
REPO = "/Users/rjae/Development/appshapes/brigade"


# --------------------------------------------------------------------------- #
# isolation
# --------------------------------------------------------------------------- #
def nested_env(extra=None):
    """Strip by PREFIX; keep only CLAUDE_CONFIG_DIR."""
    out, stripped = {}, []
    for k, v in os.environ.items():
        if k == "CLAUDE_CONFIG_DIR":
            out[k] = v
        elif k.startswith("CLAUDE") or k in ("CLAUDECODE", "AI_AGENT"):
            stripped.append(k)
        else:
            out[k] = v
    if extra:
        out.update(extra)
    return out, sorted(stripped)


def outer_identity():
    tok = os.environ.get("CLAUDE_CODE_MESSAGING_TOKEN", "")
    return {
        "session": os.environ.get("CLAUDE_CODE_SESSION_ID", ""),
        "socket": os.environ.get("CLAUDE_CODE_MESSAGING_SOCKET", ""),
        "pid": os.environ.get("CLAUDE_PID", ""),
        "token_sha12": hashlib.sha256(tok.encode()).hexdigest()[:12] if tok else "",
    }


def isolation_verdict(hook_rows, outer):
    """hook_rows: the SessionStart records the nested session wrote."""
    rows = []
    for h in hook_rows:
        rows.append({
            "claude_pid": h.get("claude_pid"),
            "session": h.get("session"),
            "socket": h.get("socket"),
            "token_sha12": h.get("token_sha12"),
            "pid_differs": bool(h.get("claude_pid")) and h.get("claude_pid") != outer["pid"],
            "session_differs": bool(h.get("session")) and h.get("session") != outer["session"],
            "socket_differs": bool(h.get("socket")) and h.get("socket") != outer["socket"],
            "token_differs": bool(h.get("token_sha12")) and h.get("token_sha12") != outer["token_sha12"],
        })
    ok = bool(rows) and all(r["pid_differs"] and r["session_differs"]
                            and r["socket_differs"] and r["token_differs"] for r in rows)
    return {"outer": outer, "nested": rows, "ok": ok}


# --------------------------------------------------------------------------- #
# config protection
# --------------------------------------------------------------------------- #
def sha256_file(p):
    try:
        with open(p, "rb") as f:
            return hashlib.sha256(f.read()).hexdigest()
    except OSError:
        return ""


def cfgdir():
    return os.environ.get("CLAUDE_CONFIG_DIR") or os.path.expanduser("~/.claude")


PROTECTED = None


def protected_paths():
    return [os.path.join(cfgdir(), "settings.json"),
            os.path.join(REPO, "CLAUDE.md"),
            os.path.expanduser("~/.claude/CLAUDE.md")]


class Guard:
    def __init__(self, results):
        self.results = results
        self.snapdir = os.path.join(results, "config-snapshot")
        os.makedirs(self.snapdir, exist_ok=True)
        self.paths = protected_paths()
        self.pre = {}
        for i, p in enumerate(self.paths):
            self.pre[p] = sha256_file(p)
            if os.path.exists(p):
                shutil.copy2(p, os.path.join(self.snapdir, "%d.snap" % i))
        self.dotclaude = os.path.join(cfgdir(), ".claude.json")
        self.dc_pre = self._dc()

    def _dc(self):
        """A read-only fingerprint of ~/.claude.json. Every top-level key is
        digested so any drift is REPORTED; nothing here is ever restored, because
        the outer session writes this file concurrently."""
        try:
            with open(self.dotclaude) as f:
                d = json.load(f)
            keys = {}
            for k, v in d.items():
                if k == "projects":
                    continue
                keys[k] = hashlib.sha256(
                    json.dumps(v, sort_keys=True, default=str).encode()).hexdigest()[:12]
            return {"projects": sorted(d.get("projects", {}).keys()),
                    "top_level": keys,
                    "fullscreenAutoDisabled": d.get("fullscreenAutoDisabled", "<absent>"),
                    "numStartups": d.get("numStartups")}
        except Exception:
            return {"unreadable": True}

    def verify(self):
        rows = []
        for i, p in enumerate(self.paths):
            post = sha256_file(p)
            changed = post != self.pre[p]
            restored = False
            snap = os.path.join(self.snapdir, "%d.snap" % i)
            if changed and os.path.exists(snap):
                shutil.copy2(snap, p)
                restored = sha256_file(p) == self.pre[p]
            rows.append({"path": p, "pre": self.pre[p], "post": post,
                         "changed": changed, "restored": restored})
        dc_post = self._dc()
        added = sorted(set(dc_post.get("projects", [])) - set(self.dc_pre.get("projects", [])))
        pre_tl = self.dc_pre.get("top_level", {})
        post_tl = dc_post.get("top_level", {})
        drift = sorted(k for k in set(pre_tl) | set(post_tl)
                       if pre_tl.get(k) != post_tl.get(k))
        return {
            "files": rows,
            "ok": all(not r["changed"] for r in rows),
            "dot_claude_json": {
                "path": self.dotclaude,
                "never_restored_wholesale": True,
                "projects_added": added,
                "top_level_keys_that_changed": drift,
                "fullscreenAutoDisabled_pre": self.dc_pre.get("fullscreenAutoDisabled"),
                "fullscreenAutoDisabled_post": dc_post.get("fullscreenAutoDisabled"),
                "numStartups_pre": self.dc_pre.get("numStartups"),
                "numStartups_post": dc_post.get("numStartups"),
            },
        }


def revert_projects(added):
    """Surgically drop project keys this experiment introduced. Read-modify-write
    is racy against the outer session, so it is done once, late, and reported."""
    if not added:
        return {"reverted": [], "note": "nothing added"}
    p = os.path.join(cfgdir(), ".claude.json")
    try:
        with open(p) as f:
            d = json.load(f)
        gone = []
        for k in added:
            if k in d.get("projects", {}):
                d["projects"].pop(k)
                gone.append(k)
        tmp = p + ".e08tmp"
        with open(tmp, "w") as f:
            json.dump(d, f, indent=2)
        os.replace(tmp, p)
        return {"reverted": gone, "note": "surgical: only these project keys removed"}
    except Exception as e:
        return {"reverted": [], "error": "%s: %s" % (type(e).__name__, e)}


# --------------------------------------------------------------------------- #
# ndjson
# --------------------------------------------------------------------------- #
def read_ndjson(path):
    out = []
    if os.path.exists(path):
        with open(path) as f:
            for line in f:
                line = line.strip()
                if line:
                    try:
                        out.append(json.loads(line))
                    except Exception:
                        pass
    return out


# --------------------------------------------------------------------------- #
# expect
# --------------------------------------------------------------------------- #
EXPECT_PRELUDE = r"""
# ---- E0-8 expect prelude ---------------------------------------------------
# EVERY wait below is built from `expect`. expect only reads the pty while an
# `expect` command runs; a `sleep`-based wait never drains, the redrawing TUI
# fills the 64 KB pty buffer, `claude` BLOCKS ON WRITE and silently stops
# processing input. A session in that state looks alive and is frozen.
set timeout 60
log_file -a "@RESULTS@/session.log"
set marklog "@RESULTS@/marks.ndjson"
set sendlog "@RESULTS@/pty-writes.ndjson"

proc mark {event args} {
    global marklog
    set ms [clock milliseconds]
    set rec "{\"ms\": $ms, \"event\": \"$event\""
    foreach {k v} $args { append rec ", \"$k\": \"$v\"" }
    append rec "}"
    set f [open $marklog a]; puts $f $rec; close $f
    send_user "\n\[\[E08 $event ms=$ms\]\]\n"
}

# Every byte that reaches the pty goes through here and nowhere else.
proc xsend {what note} {
    global sendlog
    set ms [clock milliseconds]
    set f [open $sendlog a]
    puts $f "{\"ms\": $ms, \"note\": \"$note\", \"bytes\": [string length $what]}"
    close $f
    send -- $what
}

# Drain the pty for N seconds without ever writing to it.
proc nap {secs} {
    global timeout
    set old $timeout
    set timeout 1
    set end [expr {[clock milliseconds] + int($secs * 1000)}]
    while {[clock milliseconds] < $end} {
        expect {
            -re {(.|\n)+} { }
            timeout { }
            eof { break }
        }
    }
    set timeout $old
}

# Count non-blank lines in an NDJSON file (0 when it does not exist yet).
proc ndcount {path} {
    if {![file exists $path]} { return 0 }
    set f [open $path r]
    set data [read $f]
    close $f
    set n 0
    foreach line [split $data "\n"] { if {[string trim $line] ne ""} { incr n } }
    return $n
}

# Wait, draining, until an NDJSON file reaches N lines. Returns ms waited, or -1.
proc waitcount {path n secs} {
    set t0 [clock milliseconds]
    set end [expr {$t0 + int($secs * 1000)}]
    while {[clock milliseconds] < $end} {
        if {[ndcount $path] >= $n} { return [expr {[clock milliseconds] - $t0}] }
        nap 1
    }
    if {[ndcount $path] >= $n} { return [expr {[clock milliseconds] - $t0}] }
    return -1
}

# ---- SUBMIT ----------------------------------------------------------------
# A trailing "\r" in the SAME send as a long prompt does NOT submit. Claude Code
# treats a fast burst of bytes as a PASTE, and the CR that arrives at the end of
# the burst is absorbed into the pasted text as a newline instead of submitting.
# [Measured: a 194-byte prompt sent as one `send "...\r"` sat in the input box;
# three of them accumulated there and were only submitted much later.] So the
# text and the Enter are always separate writes with a drain between them.
proc submit {text note} {
    xsend $text "$note (text)"
    nap 2
    xsend "\r" "$note (enter)"
    nap 1
}

# ---- ONBOARDING ------------------------------------------------------------
# FOUR different first-run prompts have now been seen (the trust dialog; a
# fullscreen-renderer write; a "Claude in Chrome extension detected" prompt --
# observed again here, offering "Esc to keep browser tools off"; and the
# fullscreen-renderer question). So this tolerates an ARBITRARY prompt rather
# than only the trust one: anything unrecognised gets an Escape, which cancels a
# dialog without answering it. Box drawing interleaves cursor escapes, so only
# SINGLE-word regexes ever match.
# The trust dialog's highlighted default is "No, exit" -- a bare Enter QUITS --
# so the selection is moved DOWN first and only then confirmed.
proc onboard {} {
    global timeout
    set old $timeout
    set timeout 12
    set handled 0
    for {set i 0} {$i < 6} {incr i} {
        set done 0
        expect {
            -re {trust} {
                mark onboard_trust
                expect -re {exit}
                nap 1
                xsend "\033\[B" "trust: move off the default No, exit"
                nap 1
                xsend "\r" "trust: confirm Yes"
                incr handled
            }
            -re {Chrome} {
                mark onboard_chrome
                nap 1
                xsend "\033" "chrome prompt: Esc keeps browser tools off"
                incr handled
            }
            -re {shortcuts} { mark onboard_ready ; set done 1 }
            timeout { mark onboard_quiet ; set done 1 }
            eof { mark onboard_eof ; set done 1 }
        }
        if {$done} { break }
    }
    set timeout $old
    return $handled
}

# ---- CANARY ----------------------------------------------------------------
# The proof that the session is really accepting typed input. The token is SPLIT
# so it cannot appear as the terminal's echo of the prompt: the prompt asks for
# the letters READY followed by a tail, and never contains the concatenation.
# If an unexpected prompt ate the keystroke, the canary simply never comes back
# -- which is exactly the failure this guards against -- and it is retried after
# an escape.
proc canary {tail tries} {
    for {set i 0} {$i < $tries} {incr i} {
        if {$i > 0} {
            mark canary_recover attempt $i
            xsend "\033" "recovery: escape a stray prompt"
            nap 2
        }
        submit "Reply with the five capital letters R,E,A,D,Y immediately followed by the four characters $tail as one unbroken nine-character token, on a line of its own, and nothing else. Do not use any tool." "canary prompt"
        set ok 0
        expect {
            -re "READY$tail" { mark canary_ok attempt $i ; set ok 1 }
            timeout { mark canary_timeout attempt $i }
            eof { mark canary_eof ; return 0 }
        }
        if {$ok} { nap 3 ; return 1 }
    }
    return 0
}

proc shutdown {} {
    mark shutdown_begin
    # Escape first: if a permission prompt is up, keystrokes go to the DIALOG,
    # and "/exit" would be typed into it instead of the input box. Escape also
    # REJECTS the pending prompt, which persists nothing.
    xsend "\033" "reject any pending permission prompt"
    nap 2
    xsend "\033" "second escape"
    nap 1
    submit "/exit" "exit"
    expect {
        eof { mark eof }
        timeout { mark exit_timeout }
    }
}
"""


def write_expect(results, body):
    script = EXPECT_PRELUDE.replace("@RESULTS@", results) + "\n" + body
    p = os.path.join(results, "drive.exp")
    with open(p, "w") as f:
        f.write(script)
    return p


def run_expect(path, cwd, env, timeout, results, name="expect.raw"):
    with open(os.path.join(results, name), "wb") as out:
        try:
            subprocess.run(["expect", "-f", path], cwd=cwd, env=env,
                           stdout=out, stderr=subprocess.STDOUT, timeout=timeout)
            return "exited"
        except subprocess.TimeoutExpired:
            return "killed-by-driver-timeout"


def new_results(tag):
    d = os.path.join(HERE, "results", tag)
    os.makedirs(d, exist_ok=True)
    return d


def stamp(prefix):
    return "%s-%s" % (prefix, time.strftime("%Y%m%d-%H%M%S"))
