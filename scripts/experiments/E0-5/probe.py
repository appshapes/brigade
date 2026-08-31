#!/usr/bin/env python3
"""E0-5 baseline probe: does an expect-driven interactive session in THIS harness
(a) act on a TYPED prompt and (b) act on a message posted to its inbox socket?

Both halves have to be true before any /clear result means anything: a
post-boundary delivery failure is unattributable unless delivery is known to
work before the boundary. Nothing is typed after the typed-prompt half.
"""

import json
import os
import shutil
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import common  # noqa: E402
import g2  # noqa: E402

HERE = common.HERE
PLUGIN = os.path.join(HERE, "plugin_c")

tag = "probe-%s" % time.strftime("%H%M%S")
results = os.path.join(HERE, "results2", tag)
state = os.path.join(HERE, "state2", tag)
os.makedirs(results, exist_ok=True)
os.makedirs(state, mode=0o700, exist_ok=True)
project = "/tmp/e05-projects/%s" % tag
os.makedirs(project, exist_ok=True)

marker = "/tmp/e05p-%s-typed" % tag
marker2 = "/tmp/e05p-%s-socket" % tag
go = os.path.join(state, "go")
for m in (marker, marker2):
    try:
        os.unlink(m)
    except OSError:
        pass

tpl = open(os.path.join(HERE, "probe.exp")).read()
exp = (tpl.replace("{UNSETS}", " ".join(common.env_unset_args()))
          .replace("{PLUGIN}", PLUGIN).replace("{MARKER2}", marker2)
          .replace("{MARKER}", marker).replace("{GO}", go)
          .replace("{RESULTS}", results).replace("{LIB}", os.path.join(HERE, "expectlib.tcl"))
          .replace("{READY1}", os.path.join(state, "ready-1.json")))
p = os.path.join(results, "probe.exp")
open(p, "w").write(exp)

env = common.child_env({"BRIGADE_STATE_DIR": state, "BRIGADE_E05_HOME": HERE,
                        "BRIGADE_E05_TAG": tag, "BRIGADE_E05_SPAWN": "0"})
proc = subprocess.Popen(["expect", "-f", p], cwd=project, env=env,
                        stdout=open(os.path.join(results, "raw.log"), "wb"),
                        stderr=subprocess.STDOUT)

t0 = time.time()
typed_ok = g2.waitfor(marker, 400)
print("TYPED prompt acted on: %s after %.1f s" % (typed_ok, time.time() - t0))

g2.waitfor(go, 400)
coords = os.path.join(state, "nested-env-1.json")
body = ("Using the Bash tool, run exactly this one command and then stop:\n\n"
        "    touch %s\n\nNothing else." % marker2)
t1 = time.time()
r = subprocess.run([sys.executable, os.path.join(HERE, "post.py"), "--coords", coords,
                    "--body", body, "--log", os.path.join(state, "posts.ndjson"),
                    "--label", "probe-socket"], capture_output=True, text=True)
print("post:", r.stdout.strip() or r.stderr.strip())
sock_ok = g2.waitfor(marker2, 200)
print("SOCKET post acted on: %s after %.1f s" % (sock_ok, time.time() - t1))

try:
    proc.wait(timeout=200)
except subprocess.TimeoutExpired:
    proc.kill()

print("prompt hook records:", json.dumps(g2.read_ndjson(os.path.join(state, "prompt.ndjson")),
                                         indent=1)[:2000])
print("startup:", [r["payload_source"] for r in
                   g2.read_ndjson(os.path.join(state, "startup.ndjson"))])
for m in (marker, marker2):
    try:
        os.unlink(m)
    except OSError:
        pass
shutil.rmtree(project, ignore_errors=True)
