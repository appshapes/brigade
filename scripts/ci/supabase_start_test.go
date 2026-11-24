//go:build !windows

// scripts/ci/supabase-start.sh: the start of the local Supabase stack in ci.yml's `supabase` and `e2e` jobs
// (work item 437). A port of the stack that something else on the runner holds made `supabase start` fail four
// times in about 200 starts; the script reserves the stack's ports early in the job and runs the start at most
// three times, saying each refusal.
//
// The script is run for real. What it starts is a stand-in that fails the way Docker did, with the lines of
// run 45383370769's first attempt, or another way, or not at all, as a file of answers says; `uname`, `sysctl`,
// `sudo`, `ss` and `sleep` are stand-ins too, first on PATH, so that the Linux half runs on any machine, no
// kernel setting is touched and no test waits.
package ci_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

const supabaseStartRel = "scripts/ci/supabase-start.sh"

// What Docker said on the runner, word for word (run 45383370769, attempt 1, the `supabase` job), and the same
// refusal at the API's port, which the stack asks for after the database is up.
const dockerRefusedThePort = `failed to start docker container "supabase_db_brigade": Error response from daemon: failed to set up container networking: driver failed programming external connectivity on endpoint supabase_db_brigade (22ecdbf2f39a86c12a29ac2c9457b6c8aae30f59430b34922d37d82759c2d1e5): failed to bind host port for 0.0.0.0:54322:172.18.0.2:5432/tcp: address already in use`

const dockerRefusedTheAPIPort = `failed to start docker container "supabase_kong_brigade": Error response from daemon: failed to set up container networking: driver failed programming external connectivity on endpoint supabase_kong_brigade (0f6e5a1c): failed to bind host port for 0.0.0.0:54321:172.18.0.5:8000/tcp: address already in use`

// What `ss -H -tanp` prints for a connection that was closed a moment ago, for one that is open, and for one
// outside the stack's ports.
const (
	socketClosed   = `TIME-WAIT 0      0      10.0.0.5:54322 140.82.112.3:443`
	socketOpen     = `ESTAB  0      0      10.0.0.5:54321 20.60.1.1:443  users:(("blacksmithd",pid=912,fd=7))`
	socketElsewhe  = `ESTAB  0      0      10.0.0.5:40000 20.60.1.1:443  users:(("Runner.Worker",pid=77,fd=9))`
	socketListener = `LISTEN 0      4096   0.0.0.0:22 0.0.0.0:*  users:(("sshd",pid=1,fd=3))`
)

// supabaseStartRig is one run's stand-ins and what they recorded.
type supabaseStartRig struct {
	t   *testing.T
	dir string
}

// newSupabaseStartRig writes the stand-ins. answers has one word a start: `ok`, `held` (Docker's refusal of the
// database's port), `held-api` (of the API's), `words` (the words `address already in use` about something that
// is no port of the stack) or `other` (a failure that is not a port's); the last word answers every later
// start, and the tenth start fails whatever the word. A file named `noss` makes `ss` fail.
func newSupabaseStartRig(t *testing.T, answers ...string) *supabaseStartRig {
	t.Helper()
	g := &supabaseStartRig{t: t, dir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(g.dir, "bin"), 0o700); err != nil {
		t.Fatalf("creating the stand-ins' directory: %v", err)
	}
	d := shellQuote(g.dir)
	for name, body := range map[string]string{
		// The start itself: `supabase start` as the runner's log has it.
		"stack": `#!/bin/sh
d=` + d + `
{ printf 'stack'; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >> "$d/stack.log"
n=$(cat "$d/calls" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > "$d/calls"
# A script that has lost its bound ends here and not never: this is no failure of the port's.
[ "$n" -lt 10 ] || { echo 'the stand-in was started ten times' >&2; exit 99; }
total=$(grep -c '' "$d/answers")
[ "$n" -le "$total" ] || n=$total
case $(sed -n "${n}p" "$d/answers") in
  ok) echo 'Starting database...'; echo 'Started supabase local development setup.' ;;
  held)
    echo 'Starting database...'
    echo 'Stopping containers...'
    echo ` + shellQuote(dockerRefusedThePort) + ` >&2
    echo 'Error: failed to start containers: 519436ab057f9d7a172c7ea3e7bef931b5f0bd9ca08d3dc72b8aa5cab6964568' >&2
    exit 1 ;;
  held-api)
    echo 'Starting database...'
    echo 'Starting containers...'
    echo 'Stopping containers...'
    echo ` + shellQuote(dockerRefusedTheAPIPort) + ` >&2
    exit 1 ;;
  words)
    echo 'Starting database...'
    echo 'psql: error: could not bind IPv4 address "127.0.0.1": address already in use (port 5432)' >&2
    exit 3 ;;
  other)
    echo 'Starting database...'
    echo 'ERROR: relation "brigade.teams" does not exist (SQLSTATE 42P01)' >&2
    exit 7 ;;
esac
`,
		"bin/uname": `#!/bin/sh
cat ` + d + `/uname
`,
		// sysctl: the two values the script reads, and the one it writes, which a file named `refuse` refuses.
		"bin/sysctl": `#!/bin/sh
d=` + d + `
printf 'sysctl %s\n' "$*" >> "$d/sysctl.log"
case "$1 $2" in
  '-n net.ipv4.ip_local_port_range') printf '32768\t60999\n' ;;
  '-n net.ipv4.ip_local_reserved_ports') cat "$d/reserved" ;;
  '-w net.ipv4.ip_local_reserved_ports='*)
    [ ! -e "$d/refuse" ] || { echo 'sysctl: permission denied on key "net.ipv4.ip_local_reserved_ports"' >&2; exit 1; }
    printf '%s\n' "${2#*=}" > "$d/reserved"
    printf '%s\n' "$2" ;;
  *) echo "sysctl: the stand-in does not know $*" >&2; exit 2 ;;
esac
`,
		"bin/sudo": `#!/bin/sh
printf 'sudo %s\n' "$*" >> ` + d + `/sudo.log
[ "$1" != -n ] || shift
exec "$@"
`,
		"bin/ss": `#!/bin/sh
[ ! -e ` + d + `/noss ] || { echo 'ss: cannot open netlink socket' >&2; exit 1; }
cat ` + d + `/sockets
`,
		"bin/sleep": `#!/bin/sh
printf '%s\n' "$1" >> ` + d + `/sleep.log
`,
	} {
		testutil.WriteExecutable(t, filepath.Join(g.dir, name), []byte(body))
	}
	g.write("answers", strings.Join(answers, "\n")+"\n")
	g.write("uname", "Linux\n")
	g.write("reserved", "\n")
	g.write("sockets", "")
	for _, log := range []string{"stack.log", "sysctl.log", "sudo.log", "sleep.log"} {
		g.write(log, "")
	}
	return g
}

func (g *supabaseStartRig) write(name, text string) {
	g.t.Helper()
	if err := os.WriteFile(filepath.Join(g.dir, name), []byte(text), 0o600); err != nil {
		g.t.Fatalf("writing the stand-ins' %s: %v", name, err)
	}
}

func (g *supabaseStartRig) read(name string) string {
	g.t.Helper()
	//nolint:gosec // G304: under t.TempDir().
	data, err := os.ReadFile(filepath.Join(g.dir, name))
	if err != nil {
		g.t.Fatalf("reading the stand-ins' %s: %v", name, err)
	}
	return string(data)
}

// lines is what a stand-in recorded, a call a line.
func (g *supabaseStartRig) lines(name string) []string {
	g.t.Helper()
	text := strings.TrimSpace(g.read(name))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// run starts the script with the stand-ins first on PATH.
func (g *supabaseStartRig) run(script string, env []string, args ...string) result {
	g.t.Helper()
	full := baseEnv(g.t, g.dir)
	for i, kv := range full {
		if strings.HasPrefix(kv, "PATH=") {
			full[i] = "PATH=" + filepath.Join(g.dir, "bin") + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
		}
	}
	return runScriptAt(g.t, script, g.dir, append(full, env...), args...)
}

// start runs `start` on the stand-in, launched as `/bin/sh <file>` with two arguments, one of two words.
func (g *supabaseStartRig) start(script string, env ...string) result {
	g.t.Helper()
	return g.run(script, env, "start", "/bin/sh", filepath.Join(g.dir, "stack"), "supabase-start", "two words")
}

// supabaseStartCase is one behaviour of `start`: what the stand-in answers, and what the script must do.
type supabaseStartCase struct {
	name    string
	answers []string
	sockets []string
	noSS    bool // `ss` does not answer
	env     []string
	code    int
	starts  int      // how often the command ran
	sleeps  []string // what the script waited, in order
	prints  []string
	never   []string
}

var supabaseStartCases = []supabaseStartCase{
	{
		name: "the stack starts at once", answers: []string{"ok"}, code: 0, starts: 1,
		prints: []string{"supabase-start: attempt 1 of 3: /bin/sh ", "Started supabase local development setup."},
		never:  []string{"::warning::", "::error::", "attempt 2"},
	},
	{
		name: "the port is held once, then the stack starts", answers: []string{"held", "ok"},
		sockets: []string{socketListener, socketOpen, socketClosed, socketElsewhe}, code: 0, starts: 2, sleeps: []string{"35"},
		prints: []string{
			"::warning::supabase-start: attempt 1 of 3: port 54322 of the stack is held by something else on this machine (work item 437): failed to bind host port for 0.0.0.0:54322:172.18.0.2:5432/tcp: address already in use\n",
			// The holder of the port Docker named, and of no other port of the stack.
			"supabase-start: what holds port 54322 now (state, queues, local, peer, process):\nsupabase-start:   " + socketClosed + "\nsupabase-start: waiting 35 seconds",
			"supabase-start: attempt 2 of 3: ",
			"::warning::supabase-start: the stack started at attempt 2 of 3, after a port of it was held at every attempt before (work item 437)",
			dockerRefusedThePort, // the command's own words reach the log, its standard error too
		},
		never: []string{"::error::", "attempt 3", "supabase-start:   " + socketElsewhe, "supabase-start:   " + socketListener},
	},
	{
		name: "the port is held twice, then the stack starts", answers: []string{"held", "held", "ok"},
		sockets: []string{socketClosed}, code: 0, starts: 3, sleeps: []string{"35", "35"},
		prints: []string{
			"::warning::supabase-start: attempt 2 of 3: port 54322 of the stack is held",
			"::warning::supabase-start: the stack started at attempt 3 of 3",
		},
		never: []string{"::error::"},
	},
	{
		name: "the port is held at every attempt", answers: []string{"held"}, code: 1, starts: 3, sleeps: []string{"35", "35"},
		prints: []string{
			"::warning::supabase-start: attempt 3 of 3: port 54322 of the stack is held",
			"supabase-start: nothing holds port 54322 now: its holder is gone",
			"::error::supabase-start: the stack did not start in 3 attempt(s), each refused a port that something else held",
		},
		never: []string{"attempt 4", "the stack started"},
	},
	{
		name: "the API's port is held once", answers: []string{"held-api", "ok"},
		sockets: []string{socketOpen, socketClosed}, code: 0, starts: 2, sleeps: []string{"35"},
		prints: []string{
			"::warning::supabase-start: attempt 1 of 3: port 54321 of the stack is held by something else on this machine (work item 437): failed to bind host port for 0.0.0.0:54321:172.18.0.5:8000/tcp: address already in use\n",
			"supabase-start: what holds port 54321 now (state, queues, local, peer, process):\nsupabase-start:   " + socketOpen + "\nsupabase-start: waiting 35 seconds",
		},
		never: []string{"::error::", "port 54322 of the stack is held"},
	},
	{
		name: "a failure that is not the port's is not tried again", answers: []string{"other", "ok"}, code: 7, starts: 1,
		prints: []string{
			`ERROR: relation "brigade.teams" does not exist (SQLSTATE 42P01)`,
			"::error::supabase-start: attempt 1 of 3 failed with exit status 7 and not on a port of the stack that is held: not tried again",
		},
		never: []string{"::warning::", "attempt 2", "waiting"},
	},
	{
		name: "the same words about what is no port of the stack are not tried again", answers: []string{"words", "ok"}, code: 3, starts: 1,
		prints: []string{"::error::supabase-start: attempt 1 of 3 failed with exit status 3 and not on a port of the stack that is held: not tried again"},
		never:  []string{"::warning::", "attempt 2", "waiting"},
	},
	{
		name: "the port is held, then a failure that is not the port's", answers: []string{"held", "other", "ok"}, code: 7,
		starts: 2, sleeps: []string{"35"},
		prints: []string{"::error::supabase-start: attempt 2 of 3 failed with exit status 7 and not on a port of the stack that is held: not tried again"},
		never:  []string{"attempt 3", "the stack started"},
	},
	{
		name: "one attempt allowed", answers: []string{"held", "ok"}, env: []string{"SUPABASE_START_ATTEMPTS=1"}, code: 1, starts: 1,
		prints: []string{"attempt 1 of 1: port 54322 of the stack is held", "::error::supabase-start: the stack did not start in 1 attempt(s)"},
		never:  []string{"waiting", "attempt 2"},
	},
	{
		name: "another wait", answers: []string{"held", "ok"}, env: []string{"SUPABASE_START_WAIT_SECONDS=2"}, code: 0, starts: 2,
		sleeps: []string{"2"}, prints: []string{"supabase-start: waiting 2 seconds"},
	},
	{
		name: "what is held before the first attempt is said", answers: []string{"ok"},
		sockets: []string{socketListener, socketOpen, socketElsewhe, socketClosed}, code: 0, starts: 1,
		prints: []string{
			"supabase-start: the kernel gives outgoing connections the ports 32768 to 60999; held now, by a connection that is open or was closed within the last minute: 3\n",
			"supabase-start: held now among the stack's ports (",
			"supabase-start:   " + socketOpen + "\nsupabase-start:   " + socketClosed + "\n",
		},
		never: []string{"::warning::", "::error::"},
	},
	{
		name: "nothing of the stack's is held before the first attempt", answers: []string{"ok"},
		sockets: []string{socketListener, socketElsewhe}, code: 0, starts: 1,
		prints: []string{"within the last minute: 1\n", "supabase-start: none of the stack's ports ("},
		never:  []string{"held now among"},
	},
	{
		name: "where the sockets cannot be listed, that is said and nothing is called free", answers: []string{"held", "ok"},
		sockets: []string{socketClosed}, noSS: true, code: 0, starts: 2, sleeps: []string{"35"},
		prints: []string{
			"supabase-start: the kernel gives outgoing connections the ports 32768 to 60999; there is no ss here that answers, so what is held now cannot be seen\n",
			"supabase-start: there is no ss here that answers, so what holds port 54322 cannot be seen\n",
		},
		never: []string{"none of the stack's ports", "within the last minute:", "nothing holds port", "what holds port 54322 now"},
	},
}

// problems runs the case on the script and says what it did otherwise.
func (tc supabaseStartCase) problems(t *testing.T, script string) []string {
	t.Helper()
	g := newSupabaseStartRig(t, tc.answers...)
	if len(tc.sockets) > 0 {
		g.write("sockets", strings.Join(tc.sockets, "\n")+"\n")
	}
	if tc.noSS {
		g.write("noss", "")
	}
	r := g.start(script, tc.env...)
	out := r.stdout + r.stderr
	var bad []string
	if r.code != tc.code {
		bad = append(bad, fmt.Sprintf("exit %d, want %d", r.code, tc.code))
	}
	if starts := g.lines("stack.log"); len(starts) != tc.starts {
		bad = append(bad, fmt.Sprintf("the command ran %d time(s), want %d", len(starts), tc.starts))
	} else {
		for _, line := range starts {
			if line != "stack [supabase-start] [two words]" {
				bad = append(bad, fmt.Sprintf("the command was given %q, want its two arguments whole", line))
			}
		}
	}
	if sleeps := g.lines("sleep.log"); !slices.Equal(sleeps, tc.sleeps) {
		bad = append(bad, fmt.Sprintf("the waits were %q, want %q", sleeps, tc.sleeps))
	}
	for _, want := range tc.prints {
		if !strings.Contains(out, want) {
			bad = append(bad, fmt.Sprintf("missing %q", want))
		}
	}
	for _, never := range tc.never {
		if strings.Contains(out, never) {
			bad = append(bad, fmt.Sprintf("%q is printed", never))
		}
	}
	if r.stderr != "" {
		bad = append(bad, fmt.Sprintf("standard error has %q: every line belongs on standard output, where GitHub reads an annotation", r.stderr))
	}
	if strings.Contains(g.read("stack.log"), "\n") && len(g.lines("stack.log")) >= 10 {
		bad = append(bad, "the stand-in was started ten times: the script has lost its bound")
	}
	if len(bad) > 0 {
		bad = append(bad, "the output:\n"+out)
	}
	return bad
}

func TestTheStackStartsStandUpToAHeldPortAndToNothingElse(t *testing.T) {
	t.Parallel()
	script := filepath.Join(testutil.RepoRoot(t), supabaseStartRel)
	for _, tc := range supabaseStartCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if bad := tc.problems(t, script); len(bad) > 0 {
				t.Error(strings.Join(bad, "\n"))
			}
		})
	}
}

// The script's own bound: its last attempt begins more than 65 seconds after the first failed. A connection
// that was closed keeps its port for 60 seconds (TIME-WAIT) and for up to five more until the kernel's timer
// for it fires, and one that was closed before the first attempt is then gone by the last.
func TestTheLastAttemptComesAfterAClosedConnectionGaveItsPortBack(t *testing.T) {
	t.Parallel()
	g := newSupabaseStartRig(t, "held")
	if r := g.start(filepath.Join(testutil.RepoRoot(t), supabaseStartRel)); r.code != 1 {
		t.Fatalf("exit %d, want 1:\n%s%s", r.code, r.stdout, r.stderr)
	}
	total := 0
	for _, s := range g.lines("sleep.log") {
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
			t.Fatalf("the script waited %q, which is no number of seconds", s)
		}
		total += n
	}
	if total <= 65 {
		t.Errorf("the script waits %d seconds between its first and its last attempt, want more than 65", total)
	}
	if total > 120 {
		t.Errorf("the script waits %d seconds in all: ci.yml's sums count 70 seconds of waits in the start's worst case, so do them again", total)
	}
}

func TestTheStackStartRefusesWhatItCannotRun(t *testing.T) {
	t.Parallel()
	script := filepath.Join(testutil.RepoRoot(t), supabaseStartRel)
	for name, tc := range map[string]struct {
		env  []string
		args []string
		want string
	}{
		"no verb":                    {nil, nil, "usage: scripts/ci/supabase-start.sh reserve | start <command>"},
		"another verb":               {nil, []string{"stop"}, "usage: scripts/ci/supabase-start.sh reserve | start <command>"},
		"start with no command":      {nil, []string{"start"}, "usage: scripts/ci/supabase-start.sh start <command>"},
		"reserve with a word":        {nil, []string{"reserve", "54322"}, "usage: scripts/ci/supabase-start.sh reserve"},
		"no attempts":                {[]string{"SUPABASE_START_ATTEMPTS=0"}, []string{"start", "true"}, "SUPABASE_START_ATTEMPTS is '0'"},
		"no attempts, in two digits": {[]string{"SUPABASE_START_ATTEMPTS=00"}, []string{"start", "true"}, "SUPABASE_START_ATTEMPTS is '00'"},
		"attempts that are words":    {[]string{"SUPABASE_START_ATTEMPTS=many"}, []string{"start", "true"}, "SUPABASE_START_ATTEMPTS is 'many'"},
		"a wait that is words":       {[]string{"SUPABASE_START_WAIT_SECONDS=1m"}, []string{"start", "true"}, "SUPABASE_START_WAIT_SECONDS is '1m'"},
	} {
		g := newSupabaseStartRig(t, "ok")
		r := g.run(script, tc.env, tc.args...)
		if r.code != 1 || !strings.Contains(r.stdout, "::error::supabase-start: "+tc.want) {
			t.Errorf("%s: exit %d, want 1 and %q:\n%s%s", name, r.code, tc.want, r.stdout, r.stderr)
		}
		if len(g.lines("stack.log"))+len(g.lines("sysctl.log")) != 0 {
			t.Errorf("%s: something ran before the arguments were read", name)
		}
	}
}

// stackPorts reads the ports supabase/config.toml names, as the script must: every uncommented `port =` and
// `<name>_port =` line.
func stackPorts(t *testing.T) []string {
	t.Helper()
	//nolint:gosec // G304: a fixed path under the repository's root.
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "supabase", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var ports []string
	for _, m := range regexp.MustCompile(`(?m)^(?:[a-z_]+_)?port\s*=\s*([0-9]+)`).FindAllStringSubmatch(string(data), -1) {
		ports = append(ports, m[1])
	}
	slices.SortFunc(ports, func(a, b string) int {
		if len(a) != len(b) {
			return len(a) - len(b)
		}
		return strings.Compare(a, b)
	})
	ports = slices.Compact(ports)
	if len(ports) < 4 || !slices.Contains(ports, "54321") || !slices.Contains(ports, "54322") {
		t.Fatalf("supabase/config.toml names the ports %q: the API's 54321 and the database's 54322 are not both among them, so this test reads it wrongly", ports)
	}
	return ports
}

func TestReserveTakesTheStacksPortsOutOfTheKernelsRange(t *testing.T) {
	t.Parallel()
	script := filepath.Join(testutil.RepoRoot(t), supabaseStartRel)
	ports := strings.Join(stackPorts(t), ",")

	t.Run("on the runner", func(t *testing.T) {
		t.Parallel()
		g := newSupabaseStartRig(t, "ok")
		g.write("sockets", strings.Join([]string{socketListener, socketElsewhe, socketClosed}, "\n")+"\n")
		r := g.run(script, nil, "reserve")
		if r.code != 0 || r.stderr != "" {
			t.Fatalf("exit %d, want 0 and nothing on standard error:\n%s%s", r.code, r.stdout, r.stderr)
		}
		if got, want := strings.TrimSpace(g.read("reserved")), ports; got != want {
			t.Errorf("reserved %q, want every port of supabase/config.toml: %q", got, want)
		}
		if want := "sudo -n sysctl -w net.ipv4.ip_local_reserved_ports=" + ports; !slices.Contains(g.lines("sudo.log"), want) {
			t.Errorf("the setting was not written as root, without a prompt (%q):\n%s", want, g.read("sudo.log"))
		}
		for _, want := range []string{
			"supabase-start: the kernel gives outgoing connections the ports 32768 to 60999; held now, by a connection that is open or was closed within the last minute: 2\n",
			"supabase-start:   " + socketClosed + "\n",
			"supabase-start: reserved: no connection made from here on takes one of " + ports + "\n",
		} {
			if !strings.Contains(r.stdout, want) {
				t.Errorf("missing %q in:\n%s", want, r.stdout)
			}
		}
		if strings.Contains(r.stdout, "::warning::") || strings.Contains(r.stdout, "::error::") {
			t.Errorf("an annotation on a reservation that was made:\n%s", r.stdout)
		}
		if len(g.lines("stack.log")) != 0 {
			t.Error("reserve started something")
		}
	})

	t.Run("what was reserved before is kept", func(t *testing.T) {
		t.Parallel()
		g := newSupabaseStartRig(t, "ok")
		g.write("reserved", "8080,9000-9010\n")
		if r := g.run(script, nil, "reserve"); r.code != 0 {
			t.Fatalf("exit %d:\n%s%s", r.code, r.stdout, r.stderr)
		}
		if got, want := strings.TrimSpace(g.read("reserved")), "8080,9000-9010,"+ports; got != want {
			t.Errorf("reserved %q, want %q", got, want)
		}
	})

	t.Run("a refusal is a warning and the job goes on", func(t *testing.T) {
		t.Parallel()
		g := newSupabaseStartRig(t, "ok")
		g.write("refuse", "")
		r := g.run(script, nil, "reserve")
		if r.code != 0 {
			t.Fatalf("exit %d, want 0: the start stands up to the fault without the reservation\n%s%s", r.code, r.stdout, r.stderr)
		}
		if want := "::warning::supabase-start: could not set net.ipv4.ip_local_reserved_ports=" + ports + ": the stack's ports are not reserved"; !strings.Contains(r.stdout, want) {
			t.Errorf("missing %q in:\n%s", want, r.stdout)
		}
		if strings.Contains(r.stdout, "supabase-start: reserved:") {
			t.Errorf("a reservation that was refused is said to be made:\n%s", r.stdout)
		}
	})

	t.Run("where there is no such setting", func(t *testing.T) {
		t.Parallel()
		g := newSupabaseStartRig(t, "ok")
		g.write("uname", "Darwin\n")
		r := g.run(script, nil, "reserve")
		if r.code != 0 || !strings.Contains(r.stdout, "::warning::supabase-start: this is not Linux with sysctl: the stack's ports "+ports+" are not reserved") {
			t.Errorf("exit %d, want 0 and the warning:\n%s%s", r.code, r.stdout, r.stderr)
		}
		if len(g.lines("sysctl.log"))+len(g.lines("sudo.log")) != 0 {
			t.Errorf("a kernel setting was read or written on a machine that is not Linux:\n%s%s", g.read("sysctl.log"), g.read("sudo.log"))
		}
	})
}

// supabaseStartMutations: one change to the script each, and the case of supabaseStartCases that must then
// fail. A case that still holds on the changed script tests nothing of what the change broke.
var supabaseStartMutations = []struct {
	name, old, replacement, fails string
}{
	{"the bound is gone", `if [ "$attempt" -ge "$attempts" ]; then`, `if false; then`, "the port is held at every attempt"},
	{"every failure is tried again", `if [ -z "$refused" ]; then`, `if false; then`, "a failure that is not the port's is not tried again"},
	{"nothing is tried again", `refused=$(grep -F 'address already in use'`, `refused=$(grep -F 'no such words'`, "the port is held once, then the stack starts"},
	{"no wait between two attempts", `    sleep "$wait_seconds"` + "\n", "", "the port is held twice, then the stack starts"},
	{"a refusal is not said", `    warn "attempt $attempt of $attempts: port $port of the stack is held`, `    : "attempt $attempt of $attempts: port $port of the stack is held`, "the port is held once, then the stack starts"},
	{"a late start is not said", `        warn "the stack started at attempt`, `        : "the stack started at attempt`, "the port is held twice, then the stack starts"},
	{"the holder is not looked for", `      held=$(holders "$all" "$port")` + "\n      if", "      held=\n      if", "the port is held once, then the stack starts"},
	{"every port of the stack's is the refused one's holder", `      held=$(holders "$all" "$port")`, `      held=$(holders "$all" "$(stack_ports | tr '\n' ' ')")`, "the port is held once, then the stack starts"},
	{"the words alone are a refusal", ` | grep -E ":$candidate([^0-9]|\$)"`, ``, "the same words about what is no port of the stack are not tried again"},
	{"the first port of the stack is named, not Docker's", `        port=$candidate`, `        port=$(stack_ports | head -n 1)`, "the API's port is held once"},
	{"sockets that cannot be listed read as none", "ss -H -tan 2>/dev/null\n}", "ss -H -tan 2>/dev/null || true\n}", "where the sockets cannot be listed, that is said and nothing is called free"},
	{"the command's status is lost", `      exit "$status"`, `      exit 1`, "a failure that is not the port's is not tried again"},
	{"a failed start passes", `    if [ "$status" -eq 0 ]; then`, `    if true; then`, "the port is held at every attempt"},
	{"the command's words are swallowed", `| tee "$work/output"`, `> "$work/output"`, "the port is held once, then the stack starts"},
	{"the arguments are split", `"$@" 2>&1; printf`, `$* 2>&1; printf`, "the stack starts at once"},
	{"every socket is a holder", `if (part[n] in want) print }'`, `print }'`, "the port is held once, then the stack starts"},
	{"a port outside the kernel's range counts", `if (port >= low && port <= high) seen[port] = 1`, `seen[port] = 1`, "nothing of the stack's is held before the first attempt"},
}

func TestEveryRuleOfTheStackStartIsHeldByACase(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	//nolint:gosec // G304: fixed paths under the repository's root.
	original, err := os.ReadFile(filepath.Join(root, supabaseStartRel))
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G304: as above.
	config, err := os.ReadFile(filepath.Join(root, "supabase", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range supabaseStartMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			if strings.Count(string(original), m.old) != 1 {
				t.Fatalf("the script carries the anchor %q %d time(s), so this mutation would be vacuous or ambiguous", m.old, strings.Count(string(original), m.old))
			}
			// A copy of the two files where the script looks for them: itself, and the configuration two
			// directories above it.
			tree := t.TempDir()
			script := filepath.Join(tree, supabaseStartRel)
			for path, data := range map[string][]byte{
				script: []byte(strings.Replace(string(original), m.old, m.replacement, 1)),
				filepath.Join(tree, "supabase", "config.toml"): config,
			} {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			at := slices.IndexFunc(supabaseStartCases, func(tc supabaseStartCase) bool { return tc.name == m.fails })
			if at < 0 {
				t.Fatalf("no case is named %q", m.fails)
			}
			if bad := supabaseStartCases[at].problems(t, script); len(bad) == 0 {
				t.Fatalf("the case %q still holds on the mutated script, so it does not test what the mutation broke", m.fails)
			}
		})
	}
}

// TestTheStackJobsReserveThePortsAndStartThroughTheScript: in ci.yml, each job that starts the stack reserves
// its ports straight after the checkout, before the steps that download the most, and starts it through the
// script; and nothing in the file starts the stack past it.
func TestTheStackJobsReserveThePortsAndStartThroughTheScript(t *testing.T) {
	t.Parallel()
	//nolint:gosec // G304: a fixed path under the repository's root.
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	const (
		reserve = "\n      - run: scripts/ci/supabase-start.sh reserve"
		start   = "scripts/ci/supabase-start.sh start make supabase-start supabase=supabase"
	)
	for _, job := range []string{"supabase", "e2e"} {
		_, body, ok := strings.Cut(workflow, "\n  "+job+":\n")
		if !ok {
			t.Errorf("ci.yml has no job %s", job)
			continue
		}
		if next := regexp.MustCompile(`\n  [a-z][a-z0-9-]*:\n`).FindStringIndex(body); next != nil {
			body = body[:next[0]]
		}
		order := []string{"\n      - uses: actions/checkout@", reserve, "\n      - uses: actions/setup-go@", "\n      - uses: supabase/setup-cli@", start}
		last := -1
		for _, step := range order {
			if strings.Count(body, step) != 1 {
				t.Errorf("the job %s has %q %d time(s), want once", job, strings.TrimSpace(step), strings.Count(body, step))
				continue
			}
			at := strings.Index(body, step)
			if at < last {
				t.Errorf("the job %s: %q does not come after the step before it (checkout, reserve, setup-go, setup-cli, start)", job, strings.TrimSpace(step))
			}
			last = at
		}
	}
	for n, line := range strings.Split(workflow, "\n") {
		code, _, _ := strings.Cut(line, " #")
		if strings.HasPrefix(strings.TrimSpace(line), "#") || !strings.Contains(code, "make supabase-start") {
			continue
		}
		if !strings.Contains(code, start) {
			t.Errorf("ci.yml:%d starts the stack past the script: %s", n+1, strings.TrimSpace(line))
		}
	}
}
