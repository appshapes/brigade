#!/usr/bin/env python3
"""E3-interactive checks 1 and 2 (plan row P3-8), driven without a keyboard.

WHAT THIS MEASURES
------------------
Check 1  In interactive **Manual** mode, with no permission rules of any kind,
         does asking for the shipped `brigade:team-messaging` skill raise ONE
         Skill-invocation dialog, and do the skill's `brigade` commands then run
         with NO Bash prompt? (E0-8 (b) measured this on Claude Code 2.1.252
         against a probe plugin; this re-measures it on the CURRENT version
         against the SHIPPED plugin.)
Check 2  Does that grant die with the turn -- i.e. does a follow-up asking for
         `brigade sessions` with no skill in play prompt?

WHY A PTY AND NOT `claude -p`
-----------------------------
`-p` has no approval surface, so a would-be prompt becomes an automatic denial.
That settles the GRANT (measured: with only `Skill` pre-approved the skill's
brigade commands run, and a resumed turn without the skill is denied) but it can
say nothing about the DIALOG -- how many appear, what they offer, and whether the
dismissal is scoped to the project directory. Those need a real terminal.

THE DETECTOR IS MECHANICAL, NOT PROSE
-------------------------------------
A `PreToolUse` hook fires once the model has produced tool parameters and BEFORE
the permission decision; `PostToolUse` fires only if the call actually ran. So

    attempt recorded + no exec recorded + the session stops making progress
        = the permission system intervened,
          and with allow/deny/ask all empty that is a PROMPT

Both hooks are supplied through `--settings`, never by editing `plugin/` (whose
file list is asserted by `make plugin-check`). The session log is grepped for the
dialog's own words only as corroboration.

EVERY RUN ALSO RECORDS THE PERMISSION MODE THREE WAYS -- the hook payload's
`permission_mode` on every tool call, the transcript's `permission-mode` records,
and the Brigade by-pid map's `permission_mode` written by the plugin's own
UserPromptSubmit hook. The first P3-8 attempt was voided because the mode was
never checked; it cannot go unchecked here.

ARMS
----
skill       fresh temp project dir: check 1, then check 2 in the same session
skill-repo  same, but cwd = the repository (the checklist's primary location)
baseline    NULL CONTROL: no skill, `brigade sessions` asked for directly.
            It MUST stall. A run of this arm that does not stall means the
            detector cannot go red and every other arm's result is void.
dismiss     two sessions in ONE project dir: the first answers the Skill dialog
            with option 2 ("don't ask again"), the second must then see no
            dialog. This re-measures E0-8's per-project scoping claim.
"""
import argparse
import json
import os
import re
import secrets
import shutil
import subprocess
import sys
import tempfile
import uuid

HERE = os.path.dirname(os.path.realpath(__file__))
REPO = os.path.dirname(os.path.dirname(os.path.dirname(HERE)))  # .../E3-interactive -> experiments -> scripts -> repo
E08 = os.path.join(REPO, "scripts", "experiments", "E0-8")
sys.dont_write_bytecode = True   # do not leave __pycache__ in a committed experiment dir
sys.path.insert(0, E08)
import common  # noqa: E402  (the expect prelude, the env strip and the config guard)

PLUGIN = os.path.join(REPO, "plugin")
HOOKBIN = os.path.join(HERE, "bin")

CHECK1_PROMPT = ("Use the brigade team-messaging skill to list the team's sessions "
                 "and tell me who is online.")
# The checklist's own wording, plus one clause. The plugin's SessionStart context
# line advertises the binary's absolute path, and a model that has not loaded the
# skill reaches for it (measured, both headless and interactively, 2026-09-03).
# Only the BARE form can match `Bash(brigade:*)`, so a run that drifts to the full
# path measures a different command shape than the one under test. The deviation
# from the checklist text is deliberate and is recorded in the writeup.
CHECK2_PROMPT = ("Without using any skill, run the Bash command `brigade sessions` "
                 "directly and paste its output. Use the bare command name "
                 "`brigade`, never a path to the binary.")

ARMS = {
    # arm:        (prompt,         skill_dialog_expected, skill_option, followup)
    "skill":      (CHECK1_PROMPT,  True,  1, True),
    "skill-repo": (CHECK1_PROMPT,  True,  1, True),
    "baseline":   (CHECK2_PROMPT,  False, 1, False),
    "dismiss":    (CHECK1_PROMPT,  True,  2, False),
    "dismiss2":   (CHECK1_PROMPT,  True,  1, False),
}

BODY = r"""
# A 24x80 pty wraps the canary token across a line, inserting a CR and cursor
# escapes between its halves, and the adjacency the canary depends on is lost
# [measured 2026-09-03: `READY` and `250B` rendered on separate screen lines and
# the first attempt timed out, recovering only on the retry]. A wide pty removes
# the wrap; `stty_init` must be set BEFORE spawn.
set stty_init "rows 50 columns 200"

# Wait, draining, until neither file has grown for `secs` seconds. Returns 1 if
# it settled, 0 if `cap` seconds elapsed first.
proc quiesce {att exec secs cap} {
    set t0 [clock milliseconds]
    set la [ndcount $att]
    set le [ndcount $exec]
    set stable [clock milliseconds]
    while {[clock milliseconds] - $t0 < int($cap * 1000)} {
        nap 1
        set a [ndcount $att]
        set e [ndcount $exec]
        if {$a != $la || $e != $le} {
            set la $a
            set le $e
            set stable [clock milliseconds]
        }
        if {[clock milliseconds] - $stable >= int($secs * 1000)} { return 1 }
    }
    return 0
}

mark spawn arm @ARM@ run @RUN@ sid @SID@
spawn -noecho env @UNSETS@ claude --permission-mode default --session-id @SID@ --plugin-dir "@PLUGIN@" --settings "@SETTINGS@"

onboard
if {![canary @TAIL@ 3]} {
    mark canary_failed
    shutdown
    exit 0
}

# The plugin's UserPromptSubmit hook is the ONLY writer of `permission_mode` in
# the by-pid map, and the SessionEnd hook DELETES the entry -- so it has to be
# read while the session is alive [E3-smoke.md says the same; a post-run read
# returned nothing, measured 2026-09-03]. `env` execs `claude` in place, so the
# spawned pid IS the CLAUDE_PID the map is keyed by.
set cpid [exp_pid]
mark claude_pid pid $cpid
catch {exec cp "@BYPIDDIR@/$cpid.json" "@RESULTS@/bypid-live.json"} cperr
mark bypid_snapshot pid $cpid err "$cperr"

mark arm_prompt_sent
submit "@PROMPT@" "arm prompt"

# ---- THE SKILL TOOL ITSELF GATES IN MANUAL MODE -----------------------------
# [E0-8, 2.1.252: `Use skill "brigade:teammsg"? / Do you want to proceed?`, with
# option 1 (Yes) highlighted.] That gate sits BEFORE the Bash grant under test,
# so the run would otherwise stall on the wrong dialog. `proceed` is a SINGLE
# word: box drawing interleaves cursor escapes and a multi-word regex never
# matches a boxed dialog.
# BOTH dialogs say "Do you want to proceed?" on 2.1.259, so `proceed` alone
# cannot tell them apart [measured: the Bash box reads "This command requires
# approval / Do you want to proceed? / 1. Yes / 2. Yes, and don't ask again
# for: <cmd> * / 3. Yes, and switch to auto mode / 4. No"; the Skill box reads
# "Use skill ...? ... Do you want to proceed? / 1. Yes / 2. Yes, and don't ask
# again for <skill> in <dir> / 3. No"]. Answering the wrong one with a blind
# Enter would APPROVE the very Bash prompt check 1 exists to detect, and the run
# would report a pass in exactly the case that must go red. Two guards:
#   * `approval` is matched FIRST -- expect tries patterns in order, so a buffer
#     holding both words classifies as Bash. That branch presses Escape, which
#     REJECTS and persists nothing, and the stall detector then records the
#     grant failure correctly.
#   * the `proceed` branch presses nothing unless a Skill PreToolUse row exists.
#     PreToolUse fires before the permission decision, so the row is always
#     there by the time the Skill box is painted; a Bash box leaves it at 0.
if {@SKILLARM@} {
    set timeout @SKILLWAIT@
    expect {
        -re {approval} {
            mark bash_dialog_not_skill
            catch {set f [open "@RESULTS@/dialog-buffer.txt" a]; puts $f "--- BASH DIALOG ---"; puts $f $expect_out(buffer); close $f}
            nap 1
            xsend "\033" "reject: this is the Bash dialog, not the Skill dialog"
        }
        -re {proceed} {
            catch {set f [open "@RESULTS@/dialog-buffer.txt" a]; puts $f "--- DIALOG MATCHED ON proceed ---"; puts $f $expect_out(buffer); close $f}
            if {[waitcount "@SKILLATT@" 1 5] < 0} {
                mark wrong_dialog_no_skill_attempt
            } else {
                mark skill_dialog_shown
                nap 1
                if {@SKILLOPT@ == 2} {
                    xsend "\033\[B" "move to option 2 (don't ask again in this directory)"
                    nap 1
                    mark skill_option2_selected
                }
                xsend "\r" "confirm the Skill dialog"
                mark skill_dialog_answered option @SKILLOPT@
            }
        }
        timeout { mark skill_dialog_absent }
        eof { mark skill_dialog_eof }
    }
    set timeout 60
    nap 2
}

set w1 [waitcount "@EXEC@" 1 @T1@]
if {$w1 < 0} {
    mark stall index 1 attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
} else {
    mark exec_seen index 1 waited_ms $w1 attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
}
mark stage1_done w1 $w1

# The follow-up only makes sense if the turn under test finished: a session
# sitting on a permission dialog would receive these keystrokes in the DIALOG.
if {@FOLLOWUP@ && $w1 >= 0} {
    # The turn boundary must be OBSERVED, not assumed. A fixed nap can sample
    # `base` while a stage-1 command is still in flight, and that straggler would
    # then be credited to stage 2 as "the grant outlived the turn".
    set q [quiesce "@ATT@" "@EXEC@" 15 90]
    mark quiesced ok $q attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
    set base [ndcount "@EXEC@"]
    mark followup_sent base $base
    submit "@FOLLOWUPTEXT@" "check 2 prompt"
    set need [expr {$base + 1}]
    set w2 [waitcount "@EXEC@" $need @T2@]
    if {$w2 < 0} {
        mark stall index 2 attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
    } else {
        mark exec_seen index 2 waited_ms $w2 attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
    }
    mark stage2_done w2 $w2
}

nap 4
mark final attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
shutdown
"""

# SINGLE words only. Box drawing interleaves cursor escapes through a dialog's
# text, so a multi-word regex never matches one [E0-8's README says so, and a
# multi-word version of this very regex found nothing in a run whose transcript
# proves a prompt was up -- measured 2026-09-03].
# Only tokens THIS version's dialogs supply and the driver's own prompts do not.
# `skill` and `permission` appear in the arm prompt itself, which the pty echoes,
# so they fired on every run whether or not a dialog appeared.
DIALOG_WORDS = ("proceed", "approval", "amend", "cancel")


def settings_file(path, state):
    """allow/deny/ask explicitly empty, plus the two detector hooks. The shipped
    plugin is NOT touched: `make plugin-check` asserts its exact file list."""
    doc = {
        "permissions": {"allow": [], "deny": [], "ask": []},
        "hooks": {
            "PreToolUse": [{"matcher": "*", "hooks": [
                {"type": "command", "command": os.path.join(HOOKBIN, "attempt"), "timeout": 10}]}],
            "PostToolUse": [{"matcher": "*", "hooks": [
                {"type": "command", "command": os.path.join(HOOKBIN, "posttool"), "timeout": 10}]}],
        },
    }
    with open(path, "w") as f:
        json.dump(doc, f, indent=2)
    return path


def find_transcript(sid):
    root = os.path.join(common.cfgdir(), "projects")
    for dirpath, _dirs, files in os.walk(root):
        if sid + ".jsonl" in files:
            return os.path.join(dirpath, sid + ".jsonl")
    return None


def read_transcript(sid):
    """The decisive record: the permission mode the session was REALLY in, plus
    every tool_use and its result."""
    p = find_transcript(sid)
    out = {"path": p, "permission_modes": [], "modes": [], "tool_uses": [],
           "tool_results": [], "versions": [], "models": [], "context_lines": []}
    if not p:
        return out
    with open(p, errors="replace") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                d = json.loads(line)
            except Exception:
                continue
            t = d.get("type")
            if t == "permission-mode" and d.get("permissionMode"):
                out["permission_modes"].append(d["permissionMode"])
            elif t == "mode" and d.get("mode"):
                out["modes"].append(d["mode"])
            elif t == "assistant":
                if d.get("version"):
                    out["versions"].append(d["version"])
                m = (d.get("message") or {}).get("model")
                if m:
                    out["models"].append(m)
                for c in ((d.get("message") or {}).get("content") or []):
                    if isinstance(c, dict) and c.get("type") == "tool_use":
                        out["tool_uses"].append({"name": c.get("name"),
                                                 "input": str(c.get("input"))[:200]})
            elif t == "user":
                for c in ((d.get("message") or {}).get("content") or []):
                    if isinstance(c, dict) and c.get("type") == "tool_result":
                        out["tool_results"].append({"is_error": c.get("is_error"),
                                                    "content": str(c.get("content"))[:220]})
            elif t == "attachment":
                a = d.get("attachment") or {}
                if str(a.get("type", "")).startswith("hook"):
                    out["context_lines"].append({"hook": a.get("hookName"),
                                                 "content": str(a.get("content"))[:300]})
    for k in ("permission_modes", "modes", "versions", "models"):
        seen, uniq = set(), []
        for v in out[k]:
            if v not in seen:
                seen.add(v)
                uniq.append(v)
        out[k] = uniq
    return out


class ReportOnlyGuard:
    """Hash the files a run must not change, and REPORT drift. Never restore.

    E0-8's guard restores each protected path from a start-of-run snapshot. That
    is wrong here: these sessions run in Manual mode with allow/deny/ask empty and
    the only key ever pressed at a dialog is one Enter in the skill window, so no
    nested session can write these paths -- every restore it could perform would
    therefore be reverting somebody ELSE's edit. Two of this repository's own
    sessions can edit CLAUDE.md with no prompt while a matrix is in flight, and a
    silent revert would land in the working tree and be swept into the next
    commit. Reporting drift keeps the safety and drops the hazard.

    It also reads the REAL `~/.claude.json`. E0-8's guard looks for it inside the
    config dir (`~/.claude/.claude.json`), which does not exist, so its drift
    report was a vacuous clean bill.
    """

    def __init__(self, results):
        self.results = results
        self.paths = [os.path.join(common.cfgdir(), "settings.json"),
                      os.path.join(REPO, "CLAUDE.md"),
                      os.path.expanduser("~/.claude/CLAUDE.md")]
        self.dotclaude = os.path.expanduser("~/.claude.json")
        self.pre = {p: common.sha256_file(p) for p in self.paths}
        self.projects_pre = self._projects()

    def _projects(self):
        try:
            with open(self.dotclaude) as f:
                return sorted(json.load(f).get("projects", {}).keys())
        except Exception:
            return None

    def verify(self):
        rows = [{"path": p, "pre": self.pre[p], "post": common.sha256_file(p),
                 "changed": common.sha256_file(p) != self.pre[p]} for p in self.paths]
        post = self._projects()
        added = sorted(set(post or []) - set(self.projects_pre or [])) if post is not None else None
        return {"files": rows,
                "ok": all(not r["changed"] for r in rows),
                "restored_anything": False,
                "dot_claude_json": {"path": self.dotclaude,
                                    "readable": post is not None,
                                    "projects_added": added}}


BYPID_DIR = os.path.join(
    os.path.expanduser(os.environ.get("XDG_STATE_HOME", "~/.local/state")),
    "brigade", "sessions", "by-pid")


def read_bypid(results):
    """The plugin's own UserPromptSubmit hook is the only writer of
    `permission_mode` in the by-pid map -- an independent witness of the mode.
    Read from the snapshot the expect script took MID-RUN: the SessionEnd hook
    deletes the live entry, so a post-run read always returns nothing."""
    try:
        with open(os.path.join(results, "bypid-live.json")) as f:
            d = json.load(f)
        return {k: d.get(k) for k in
                ("permission_mode", "session_name", "team_name", "profile",
                 "brigade_session_id", "inbound")}
    except Exception:
        return None


def one_run(arm, run_idx, results_root, args, proj_override=None):
    spec_arm = arm
    prompt, skillarm, skillopt, followup = ARMS[spec_arm]
    tag = "%s-run%d" % (arm, run_idx)
    results = os.path.join(results_root, tag)
    os.makedirs(results, exist_ok=True)
    state = os.path.join(results, "state")
    shutil.rmtree(state, ignore_errors=True)
    os.makedirs(state)

    if proj_override:
        proj = proj_override
        os.makedirs(proj, exist_ok=True)
    elif arm == "skill-repo":
        proj = REPO
    else:
        # A GENUINELY fresh directory, outside the repository. A directory under
        # the repo tree is not one Claude Code "has never been started in" in the
        # sense check 1 needs, and a session rooted there could also write into
        # the working tree another session is using.
        proj = tempfile.mkdtemp(prefix="brigade-e3-%s-%d-" % (arm, run_idx))

    env, stripped = common.nested_env({"BRIGADE_E3_STATE": state})
    tail = secrets.token_hex(2).upper()
    sid = str(uuid.uuid4())
    setf = settings_file(os.path.join(results, "settings.json"), state)

    body = (BODY
            .replace("@ARM@", arm).replace("@RUN@", str(run_idx))
            .replace("@SID@", sid)
            .replace("@UNSETS@", " ".join("-u " + v for v in stripped))
            .replace("@PLUGIN@", PLUGIN)
            .replace("@SETTINGS@", setf)
            .replace("@TAIL@", tail)
            .replace("@PROMPT@", prompt)
            .replace("@FOLLOWUPTEXT@", CHECK2_PROMPT)
            .replace("@EXEC@", os.path.join(state, "exec.ndjson"))
            .replace("@ATT@", os.path.join(state, "attempts.ndjson"))
            .replace("@BYPIDDIR@", BYPID_DIR)
            .replace("@SKILLATT@", os.path.join(state, "skill-attempt.ndjson"))
            .replace("@RESULTS@", results)
            .replace("@SKILLARM@", "1" if skillarm else "0")
            .replace("@SKILLOPT@", str(skillopt))
            .replace("@SKILLWAIT@", str(args.skillwait))
            .replace("@FOLLOWUP@", "1" if followup else "0")
            .replace("@T1@", str(args.t1)).replace("@T2@", str(args.t2)))
    exp = common.write_expect(results, body)
    how = common.run_expect(exp, proj, env, args.hard_timeout, results)

    marks = common.read_ndjson(os.path.join(results, "marks.ndjson"))
    atts = common.read_ndjson(os.path.join(state, "attempts.ndjson"))
    execs = common.read_ndjson(os.path.join(state, "exec.ndjson"))
    skill_execs = common.read_ndjson(os.path.join(state, "skill-exec.ndjson"))

    log = ""
    lp = os.path.join(results, "session.log")
    if os.path.exists(lp):
        log = open(lp, errors="replace").read()
    dialog_hits = {w: len(re.findall(w, log, re.I)) for w in DIALOG_WORDS}
    # "Use skill" is the Skill box's own first line and survives the TUI intact.
    # The Bash box's "This command requires approval" does NOT: the words wrap and
    # interleave with cursor escapes, so only the single word can be counted --
    # which is why the expect script discriminates on the single word too. In a
    # skill arm an `approval` count of 1 is check 2's EXPECTED Bash dialog; what
    # matters for check 1 is that none appeared before the skill answer, and that
    # is settled by the marks and by dialog-buffer.txt, not by these counts.
    dialog_hits["USE_SKILL_BOXES"] = len(re.findall(r"Use skill", log))

    by_event = {}
    for m in marks:
        by_event.setdefault(m["event"], []).append(m)
    stalls = [int(m["index"]) for m in by_event.get("stall", [])]
    seen = {int(m["index"]): int(m["waited_ms"]) for m in by_event.get("exec_seen", [])}

    tr = read_transcript(sid)
    pid = ""
    for m in by_event.get("claude_pid", []):
        pid = str(m.get("pid") or "")
    if not pid:
        for a in atts:
            if a.get("claude_pid"):
                pid = a["claude_pid"]
                break
    hook_modes = sorted({a.get("permission_mode") for a in atts if a.get("permission_mode")})
    brig_atts = [a for a in atts if a.get("brigade_form") == "bare"]
    fullpath_atts = [a for a in atts if a.get("brigade_form") == "fullpath"]
    fullpath_execs = common.read_ndjson(os.path.join(state, "fullpath-exec.ndjson"))
    skill_atts = [a for a in atts if a.get("tool") == "Skill"]

    # ---- STAGE SCOPING -------------------------------------------------------
    # Rows carry `ms`, and the marks carry the moment the follow-up was typed, so
    # each turn's tool calls can be separated by time instead of by a line count.
    t_follow = None
    for m in by_event.get("followup_sent", []):
        t_follow = int(m["ms"])
    def after(rows):
        return [r for r in rows if t_follow and int(r.get("ms", 0)) > t_follow]
    s2_atts, s2_execs = after(atts), after(execs)
    s2_bare = [a for a in s2_atts if a.get("brigade_form") == "bare"]
    s2_skill = [a for a in s2_atts if a.get("tool") == "Skill"]
    s2_fullpath = [a for a in s2_atts if a.get("brigade_form") == "fullpath"]

    v = {
        "arm": arm, "run": run_idx, "sid": sid, "cwd": proj,
        "expect_process": how,
        "canary_ok": "canary_ok" in by_event,
        "claude_pid": pid,
        # --- the mode, three independent ways -------------------------------
        "mode_from_hook_payload": hook_modes,
        "mode_from_transcript": tr["permission_modes"],
        "mode_from_bypid_map": (read_bypid(results) or {}).get("permission_mode"),
        # --- the Skill gate --------------------------------------------------
        "skill_dialog_shown": "skill_dialog_shown" in by_event,
        # Set when the expect script identified a BASH box where the Skill box
        # was expected, or matched `proceed` with no Skill attempt on record.
        "answered_a_bash_dialog": ("bash_dialog_not_skill" in by_event
                                   or "wrong_dialog_no_skill_attempt" in by_event),
        "skill_dialog_answered": "skill_dialog_answered" in by_event,
        "skill_option2_selected": "skill_option2_selected" in by_event,
        "skill_tool_attempts": len(skill_atts),
        "skill_tool_executions": len(skill_execs),
        "skill_names": [a.get("skill_name") for a in skill_atts],
        # --- the Bash grant --------------------------------------------------
        "brigade_attempts": len(brig_atts),
        "brigade_executions": len(execs),
        "brigade_attempt_cmds": [a.get("cmd_head") for a in brig_atts],
        "brigade_exec_cmds": [e.get("cmd_head") for e in execs],
        # The skill forbids the full path outright; the context line advertises it.
        "fullpath_attempts": [a.get("cmd_head") for a in fullpath_atts],
        "fullpath_executions": len(fullpath_execs),
        "non_brigade_bash_attempts": [a.get("cmd_head") for a in atts
                                      if a.get("tool") == "Bash"
                                      and not a.get("brigade_form")],
        "other_tool_attempts": sorted({a.get("tool") for a in atts
                                       if a.get("tool") not in ("Bash", "Skill")}),
        "first_stall_index": stalls[0] if stalls else None,
        "exec_wait_ms": seen,
        "followup_ran": "followup_sent" in by_event,
        "quiesced_before_followup": bool(by_event.get("quiesced")
                                         and by_event["quiesced"][-1].get("ok") == "1"),
        # A stall is only evidence of a PROMPT if a bare `brigade` call was
        # actually attempted and did not run. Without that conjunct (which
        # E0-8's ancestor had) a turn where the model called nothing at all --
        # or answered in prose -- would be scored "prompted".
        "check2_prompted": bool(2 in stalls and len(s2_bare) > len(s2_execs)),
        "check2_unprompted": bool(len(s2_execs) >= 1),
        "check2_bare_attempts": len(s2_bare),
        "check2_skill_attempts": len(s2_skill),
        "check2_fullpath_attempts": len(s2_fullpath),
        "check2_stage2_executions": [e.get("cmd_head") for e in s2_execs],
        # The check says nothing if the model re-invoked the skill (the grant
        # would then apply legitimately) or never tried the bare command.
        "check2_inconclusive_skill_reinvoked": bool(len(s2_skill) > 0),
        "check2_inconclusive_no_bare_attempt": bool(t_follow and not s2_bare),
        "dialog_words_in_log": dialog_hits,
        "transcript": {k: tr[k] for k in
                       ("path", "permission_modes", "modes", "versions", "models",
                        "tool_uses", "tool_results", "context_lines")},
        "bypid": read_bypid(results),
    }
    # The headline judgements, stated as data rather than prose.
    v["stall_indexes"] = stalls
    # Check 1 is about STAGE 1 only: a stall at index 2 is check 2's EXPECTED
    # result. It requires the SKILL tool to have actually run (a mechanical row
    # written by bin/posttool, not prose) and the skill to have been the shipped
    # one -- so a run that answered some other dialog cannot report a pass.
    v["check1_pass"] = bool(v["skill_dialog_shown"]
                            and v["skill_tool_executions"] >= 1
                            and "brigade:team-messaging" in v["skill_names"]
                            and v["brigade_executions"] >= 1
                            and 1 not in stalls
                            and not v["answered_a_bash_dialog"])
    v["check2_pass"] = bool(v["followup_ran"] and v["check2_prompted"]
                            and not v["check2_inconclusive_skill_reinvoked"]
                            and not v["check2_inconclusive_no_bare_attempt"])
    # The null control must show a bare attempt that did NOT run.
    v["baseline_stalled"] = bool(arm == "baseline" and v["first_stall_index"] == 1
                                 and len(brig_atts) > len(execs) and len(brig_atts) >= 1)
    v["mode_is_manual"] = ("auto" not in v["mode_from_hook_payload"]
                           and "auto" not in v["mode_from_transcript"]
                           and bool(v["mode_from_hook_payload"] or v["mode_from_transcript"]))
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(v, f, indent=2)
    return v


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--arms", default="baseline,skill,skill-repo,dismiss")
    ap.add_argument("--runs", type=int, default=1)
    ap.add_argument("--results", default=os.path.join(REPO, ".ignored", "e3-interactive"))
    ap.add_argument("--tag", default=None)
    ap.add_argument("--t1", type=int, default=120)
    ap.add_argument("--t2", type=int, default=90)
    ap.add_argument("--skillwait", type=int, default=90)
    ap.add_argument("--hard-timeout", type=int, default=900)
    args = ap.parse_args()

    tag = args.tag or common.stamp("e3")
    root = os.path.join(args.results, tag)
    os.makedirs(root, exist_ok=True)
    guard = ReportOnlyGuard(root)
    outer = common.outer_identity()

    rows = []
    for arm in args.arms.split(","):
        if arm == "dismiss":
            # ONE project directory, two sessions: option 2, then a re-open.
            shared = tempfile.mkdtemp(prefix="brigade-e3-dismiss-")
            # 1: answer with option 2 in a fresh directory.
            # 2: re-open THE SAME directory -- the dialog must be gone.
            # 3: a DIFFERENT fresh directory -- the dialog must come back. Without
            #    run 3 the pair shows only that the dismissal persists somewhere,
            #    not that it is scoped to the project directory.
            for sub, idx, override in (("dismiss", 1, shared),
                                       ("dismiss2", 2, shared),
                                       ("dismiss2", 3, None)):
                v = one_run(sub, idx, root, args, proj_override=override)
                rows.append(v)
                print(json.dumps({k: v[k] for k in
                                  ("arm", "run", "canary_ok", "mode_is_manual",
                                   "skill_dialog_shown", "skill_option2_selected",
                                   "brigade_attempts", "brigade_executions",
                                   "first_stall_index")}))
                sys.stdout.flush()
            continue
        for i in range(1, args.runs + 1):
            v = one_run(arm, i, root, args)
            rows.append(v)
            print(json.dumps({k: v[k] for k in
                              ("arm", "run", "canary_ok", "mode_is_manual",
                               "mode_from_hook_payload", "skill_dialog_shown",
                               "brigade_attempts", "brigade_executions",
                               "first_stall_index", "check2_prompted",
                               "check2_unprompted")}))
            sys.stdout.flush()

    summary = {
        "tag": tag,
        "claude_version": subprocess.run(["claude", "--version"], capture_output=True,
                                         text=True).stdout.strip(),
        "permission_mode_requested": "default (Manual)",
        "settings": "allow/deny/ask all empty; PreToolUse+PostToolUse detector hooks",
        "plugin": PLUGIN,
        "runs": rows,
        "outer_session": outer,
        "config_protection": guard.verify(),
    }
    with open(os.path.join(root, "summary.json"), "w") as f:
        json.dump(summary, f, indent=2)
    print("results: " + root)


if __name__ == "__main__":
    main()
