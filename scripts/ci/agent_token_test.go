//go:build !windows

// The token an agent holds in a workflow (work item 438, ADR 0033).
//
// `anthropics/claude-code-action` takes a `github_token`. Handed none, it exchanges the job's identity
// (`id-token: write`) for a token of the Claude GitHub App, which the session's `gh` then holds: contents,
// pull requests and issues at write on this repository, whatever the job's `permissions:` block says (measured
// on 2026-11-24, run 45476572377 of the work item's throwaway workflow). The session's git is another matter:
// it keeps the checkout's own credential, the job's token (shown on the fixer, run 45502056649). Until this work item
// review-pull-request.yml, release-notes.yml and send-release-notes.yml all ran their agents that way, under
// headers that named `contents: read` as the boundary.
//
// checkAgentTokens reads every workflow, comment lines apart, and holds:
//   - every step that uses the action is pinned by one full commit SHA, the same in every workflow;
//   - such a step is handed the job's own token, `github_token: ${{ github.token }}`, in a job whose
//     `permissions:` block grants `contents: read`, nothing at write and no `id-token`, and that never names
//     the owner's token: nothing an agent holds there can write to the repository;
//   - or its job is one agentAppTokenJobs names with its reason, and then the job has that one agent step,
//     grants the identity, hands no token, asks the exchange for exactly the wish the entry names and grants
//     its own token exactly what the entry names: the entry says what is true. The reviewer's wish,
//     `contents: read` and `issues: read`, is what keeps its `gh` from merging, and its job's own
//     `contents: read` is what keeps its git from pushing;
//   - no other job grants `id-token`, the one permission a workflow needs to get the App's token, in a block,
//     in the inline form or by `write-all`, and no workflow grants it to all its jobs at once.
//
// Like the other gates of this package the check runs twice: on the repository, where a report is a failure,
// and on mutated copies, where silence is the bug.
package ci_test

import (
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// agentJobToken is the line that hands an agent step the job's own token.
const agentJobToken = "          github_token: ${{ github.token }}"

// agentAppTokenJob is one job whose agent's `gh` holds the Claude GitHub App's token in place of the job's
// own: why; the lines of the `additional_permissions` block its one agent step carries, letter for letter;
// and the job's own `permissions:` block, whole. The action lays the wish's lines over its own wish of
// `contents`, `pull_requests` and `issues` at write, and Anthropic's exchange mints the token for the result.
// The job's own block is what the agent's git presents, so it is held as closely as the wish.
type agentAppTokenJob struct {
	reason string
	wish   []string
	grants map[string]string
}

// agentAppTokenJobs names each such job, as `<workflow>/<job>`. A new entry is added on purpose, and ADR 0033
// says what one costs.
var agentAppTokenJobs = map[string]agentAppTokenJob{
	// The fixer and a person's `@claude`: the agent's `gh` holds the App's token, so what it writes on a pull
	// request, its progress comment first, is `claude[bot]`'s; the wish keeps the action's three at write and
	// adds the read of its own run's logs. Its `git push` does NOT go with that token: git presents the
	// checkout's credential, the job's own token, which is why that job, alone among the jobs that run an
	// agent, grants `contents: write`, and
	// the runs the push raises are held until `approve-fixer-runs` approves them (shown on pull request #130:
	// run 45502056649, refused at `contents: read`; run 45507085498, pushed at `write`). Whether the fixer
	// needs the App's token at write at all is a question this work item left to the owner.
	"claude.yml/agent": {
		"its `gh` and its comments on a pull request are `claude[bot]`'s",
		[]string{"actions: read"},
		map[string]string{"contents": "write", "pull-requests": "write", "issues": "write", "id-token": "write", "actions": "read"},
	},
	// The reviewer: its review has to approve, which this repository's settings forbid a job's token, and to
	// start auto-merge.yml and claude.yml's fixer, which an event raised with a job's token never does. Its
	// token is asked for at `contents: read`, so its `gh` cannot merge, and at `issues: read`: the wish that was
	// measured (run 45476572377). Its job's own `contents: read` is what its git presents: no push.
	"review-pull-request.yml/review": {
		"submits the review that auto-merge.yml and the fixer wait for",
		[]string{"contents: read", "issues: read"},
		map[string]string{"contents": "read", "pull-requests": "write", "id-token": "write", "actions": "read"},
	},
}

// agentIdentityJobs names each job that grants `id-token` and runs no agent, with the reason. There is none:
// the one there was, the throwaway workflow of work item 438 that made the measurement above, is gone.
var agentIdentityJobs = map[string]string{}

var (
	// agentStep finds a step that runs the action, or the lower-level action beside it, and what it is pinned to.
	agentStep = regexp.MustCompile(`uses:\s*anthropics/claude-code[a-z-]*@(\S+)`)
	agentSHA  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// agentJobHead is a job's first line: a key two spaces in, under `jobs:`.
	agentJobHead = regexp.MustCompile(`(?m)^  ([A-Za-z0-9_-]+):$`)
	// agentPermission is one line of a job's `permissions:` block.
	agentPermission = regexp.MustCompile(`^      ([a-z-]+):\s*([a-z-]+)$`)
	// agentIdentity finds a grant of `id-token` however it is written: a line of a block, a key of the inline
	// form, or `write-all`, which grants everything.
	agentIdentity = regexp.MustCompile(`(?m)^\s*id-token\s*:|^\s*permissions:.*\bid-token\b|^\s*permissions:\s*write-all\b`)
	agentAnyToken = regexp.MustCompile(`(?m)^\s*github_token:.*$`)
)

// agentCode is a workflow's text as a runner reads it: no comment line, and no comment after a line's own
// words, so that a job's first line and a step's input are compared whole.
func agentCode(text string) string {
	lines := strings.Split(uncommented(text), "\n")
	for i, line := range lines {
		if at := strings.Index(line, " #"); at >= 0 {
			line = line[:at]
		}
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

// agentJobs cuts a workflow's code into what stands before `jobs:`, what stands between `jobs:` and the first
// job this check can read, which must be nothing, and its jobs by name.
func agentJobs(text string) (head, unread string, names []string, bodies map[string]string) {
	code := agentCode(text)
	head, jobs, found := strings.Cut(code, "\njobs:\n")
	if !found {
		return code, "", nil, nil
	}
	bodies = map[string]string{}
	heads := agentJobHead.FindAllStringSubmatchIndex(jobs, -1)
	if len(heads) == 0 {
		return head, jobs, nil, nil
	}
	unread = strings.TrimSpace(jobs[:heads[0][0]])
	for i, at := range heads {
		end := len(jobs)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		name := jobs[at[2]:at[3]]
		names = append(names, name)
		bodies[name] = jobs[at[1]:end]
	}
	return head, unread, names, bodies
}

// agentBlock reads the lines of a block that begins after opener, a whole line of body: every line at depth
// or deeper, blank lines apart, as YAML continues a block across them. It reports whether the opener is there.
func agentBlock(body, opener string, depth int) (lines []string, found bool) {
	_, rest, found := strings.Cut(body, "\n"+opener+"\n")
	if !found {
		return nil, false
	}
	for _, line := range strings.Split(rest, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, strings.Repeat(" ", depth)) {
			break
		}
		lines = append(lines, line)
	}
	return lines, true
}

// checkAgentTokens reports every agent step under root that holds a token its job does not bound, every job
// that could get the App's token and is not named for it, a pin that is not one full SHA, and every entry of
// the two tables that names nothing or says what is not so.
func checkAgentTokens(r reporter, root string) {
	r.Helper()
	entries, err := os.ReadDir(filepath.Join(root, workflowsDirRel))
	if err != nil {
		r.Errorf("reading %s: %v", workflowsDirRel, err)
		return
	}
	seen := map[string]bool{}
	pins := map[string][]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		rel := path.Join(workflowsDirRel, name)
		text, ok := readText(r, root, rel)
		if !ok {
			continue
		}
		head, unread, names, bodies := agentJobs(text)
		if unread != "" || (len(names) == 0 && strings.Contains(agentCode(text), "\njobs:")) {
			r.Errorf("%s has text under `jobs:` that this check cannot read as a job: %q", rel, strings.SplitN(unread, "\n", 2)[0])
		}
		if agentIdentity.MatchString(head) {
			r.Errorf("%s grants `id-token` to all its jobs at once: grant it in the one job that needs it, and name that job in agent_token_test.go", rel)
		}
		if steps := agentStep.FindAllString(head, -1); len(steps) != 0 {
			r.Errorf("%s uses the action outside a job this check can read", rel)
		}
		for _, job := range names {
			body, key := bodies[job], name+"/"+job
			seen[key] = true
			steps := agentStep.FindAllStringSubmatch(body, -1)
			for _, step := range steps {
				if !agentSHA.MatchString(step[1]) {
					r.Errorf("%s: the job `%s` pins the action to %q, which is no full commit SHA: a tag can be moved under an agent (automation.md)", rel, job, step[1])
				}
				pins[step[1]] = append(pins[step[1]], key)
			}
			identity := agentIdentity.MatchString(body)
			if len(steps) == 0 {
				if _, named := agentIdentityJobs[key]; identity && !named {
					r.Errorf("%s: the job `%s` grants `id-token` and runs no agent: with it a step can get a token of the Claude GitHub App, "+
						"which the job's `permissions:` do not bound. Drop it, or name the job in agentIdentityJobs with its reason", rel, job)
				} else if named && !identity {
					r.Errorf("agentIdentityJobs names %s, which grants no `id-token`: drop the entry", key)
				}
				continue
			}
			if _, named := agentIdentityJobs[key]; named {
				r.Errorf("agentIdentityJobs names %s as a job with no agent, and it runs one: name it in agentAppTokenJobs or hand its steps the job's token", key)
			}
			if entry, named := agentAppTokenJobs[key]; named {
				if len(steps) != 1 {
					r.Errorf("%s: the job `%s` holds the Claude GitHub App's token and has %d agent steps, want one: a second step would get that token as the action asks for it, at write", rel, job, len(steps))
				}
				// The block whole, blank lines apart: a line more would be a wish nobody measured, and a later
				// line for the same permission is the one the action takes.
				wish, found := agentBlock(body, "          additional_permissions: |", 12)
				for i := range wish {
					wish[i] = strings.TrimSpace(wish[i])
				}
				if !found || !slices.Equal(wish, entry.wish) || strings.Count(body, "\n          additional_permissions:") != 1 {
					r.Errorf("%s: the job `%s` holds the Claude GitHub App's token and its `additional_permissions` are %q, want exactly %q: "+
						"what that token can do is set by this wish and by nothing else in the file (work item 438, ADR 0033)", rel, job, wish, entry.wish)
				}
				if !identity {
					r.Errorf("agentAppTokenJobs names %s as a job whose agent holds the App's token, and the job grants no `id-token`, so the action can mint none: drop the entry", key)
				}
				// The job's own block, whole: it is what the agent's git presents.
				granted := map[string]string{}
				block, found := agentBlock(body, "    permissions:", 6)
				for _, line := range block {
					if m := agentPermission.FindStringSubmatch(line); m != nil {
						granted[m[1]] = m[2]
					} else {
						r.Errorf("%s: the job `%s`'s `permissions:` block has a line this check cannot read: %q", rel, job, strings.TrimSpace(line))
					}
				}
				if !found || !maps.Equal(granted, entry.grants) {
					r.Errorf("%s: the job `%s` grants its own token %v, want exactly %v: the agent's git presents that token, so the reviewer's "+
						"`contents: read` is what keeps it from pushing, and nothing is added to either block without the table saying so", rel, job, granted, entry.grants)
				}
				if handed := agentAnyToken.FindString(body); handed != "" {
					r.Errorf("agentAppTokenJobs names %s as a job whose agent holds the App's token, and a step of it is handed one (%s): drop the entry", key, strings.TrimSpace(handed))
				}
				continue
			}
			// Every other agent holds the job's own token, and the job's token can write nothing.
			if handed := strings.Count(body+"\n", agentJobToken+"\n"); handed != len(steps) {
				r.Errorf("%s: the job `%s` has %d agent step(s) and %d of them are handed the job's own token (`%s`): an agent step "+
					"without it is given a token of the Claude GitHub App, which the job's `permissions:` do not bound (work item 438). "+
					"A job whose agent must hold that token is named in agentAppTokenJobs with its reason",
					rel, job, len(steps), handed, strings.TrimSpace(agentJobToken))
			}
			for _, handed := range agentAnyToken.FindAllString(body, -1) {
				if handed != agentJobToken {
					r.Errorf("%s: the job `%s` hands an agent step a token that is not the job's own (%s)", rel, job, strings.TrimSpace(handed))
				}
			}
			if identity {
				r.Errorf("%s: the job `%s` runs an agent and grants `id-token`: no identity to exchange for the App's token belongs in a job whose agents hold the job's token", rel, job)
			}
			if strings.Contains(body, "GH_ACTIONS_TOKEN") {
				r.Errorf("%s: the job `%s` runs an agent and names GH_ACTIONS_TOKEN: the owner's token belongs in a job that runs none", rel, job)
			}
			block, found := agentBlock(body, "    permissions:", 6)
			if !found {
				r.Errorf("%s: the job `%s` runs an agent and does not say its `permissions:` as a block of its own: what the agent's token can do must be read in the job", rel, job)
				continue
			}
			granted := map[string]string{}
			for _, line := range block {
				m := agentPermission.FindStringSubmatch(line)
				if m == nil {
					r.Errorf("%s: the job `%s` runs an agent and its `permissions:` block has a line this check cannot read: %q", rel, job, strings.TrimSpace(line))
					continue
				}
				granted[m[1]] = m[2]
			}
			if granted["contents"] != "read" {
				r.Errorf("%s: the job `%s` runs an agent and its permissions do not grant `contents: read` (they say %q)", rel, job, granted["contents"])
			}
			for _, permission := range slices.Sorted(maps.Keys(granted)) {
				if granted[permission] != "read" {
					r.Errorf("%s: the job `%s` runs an agent and grants `%s: %s`: nothing an agent holds may write; a write belongs in a job that runs no agent", rel, job, permission, granted[permission])
				}
			}
		}
	}
	if len(pins) > 1 {
		var said []string
		for _, pin := range slices.Sorted(maps.Keys(pins)) {
			said = append(said, pin+" ("+strings.Join(pins[pin], ", ")+")")
		}
		r.Errorf("the action is pinned to %d commits, want one in every workflow: %s", len(pins), strings.Join(said, "; "))
	}
	if len(pins) == 0 {
		r.Errorf("no workflow under %s uses the action: this check reads them wrongly", workflowsDirRel)
	}
	named := slices.Collect(maps.Keys(agentAppTokenJobs))
	named = append(named, slices.Collect(maps.Keys(agentIdentityJobs))...)
	slices.Sort(named)
	for _, key := range named {
		if !seen[key] {
			r.Errorf("a table of agent_token_test.go names %s, which is no job of a workflow under %s", key, workflowsDirRel)
		}
	}
}

func TestNoAgentHoldsATokenItsJobDoesNotBound(t *testing.T) {
	t.Parallel()
	checkAgentTokens(t, testutil.RepoRoot(t))
}

// ---------------------------------------------------------------------------------------------------------
// mutations: the check, shown failing

const (
	agentNotesRel    = workflowsDirRel + "/release-notes.yml"
	agentEmailRel    = workflowsDirRel + "/send-release-notes.yml"
	agentHubRel      = workflowsDirRel + "/claude.yml"
	agentReviewRel   = workflowsDirRel + "/review-pull-request.yml"
	agentMonthlyRel  = workflowsDirRel + "/update-documentation.yml"
	agentOtherRel    = workflowsDirRel + "/keepalive.yml" // a workflow with no agent
	agentTokenInStep = "          github_token: ${{ github.token }}\n          allowed_bots: \"claude,github-actions\"\n"
)

var agentTokenMutations = []struct {
	name    string
	mutate  mutation
	because string
}{
	{
		"a step of the release notes is handed no token",
		replaceInFile(agentNotesRel, agentTokenInStep, "          allowed_bots: \"claude,github-actions\"\n"),
		"an agent step without the input gets no token at all once the job grants no identity, and the App's token if someone adds it back",
	},
	{
		"the email's job grants the identity again",
		replaceInFile(agentEmailRel, "      actions: read             # this workflow's own run history", "      id-token: write\n      actions: read             # this workflow's own run history"),
		"with `id-token` a step that drops the input gets the App's token",
	},
	{
		"the notes' agents get a token that can write",
		replaceInFile(agentNotesRel, "      contents: read            # the boundary", "      contents: write           # the boundary"),
		"the job's token is what the agents hold, so the job may not write",
	},
	{
		"the notes' agents can write issues",
		replaceInFile(agentNotesRel, "      contents: read            # the boundary: nothing in this job, agent or shell, can write to the repository\n",
			"      contents: read            # the boundary: nothing in this job, agent or shell, can write to the repository\n      issues: write\n"),
		"a write of any kind in a job that runs an agent is the agent's",
	},
	{
		"an agent is handed the owner's token",
		replaceInFile(agentEmailRel, "          github_token: ${{ github.token }}\n", "          github_token: ${{ secrets.GH_ACTIONS_TOKEN }}\n"),
		"the owner's classic token reaches every repository of his account",
	},
	{
		"a monthly job's agent is handed no token",
		replaceInFile(agentMonthlyRel, "          github_token: ${{ github.token }}\n", ""),
		"the scheduled jobs are held to the same rule as the two release-notes workflows",
	},
	{
		"one workflow moves the pin alone",
		replaceAllInFile(agentEmailRel, "claude-code-action@", "claude-code-action@0000000000000000000000000000000000000000 # was "),
		"two pins are two actions: what was read at one says nothing of the other",
	},
	{
		"the action by its tag",
		replacePinInFile(agentHubRel, "claude-code-action@v1"),
		"a tag can be moved under an agent",
	},
	{
		"a job with no agent grants the identity",
		appendToFile(agentOtherRel, "\n  another:\n    runs-on: ubuntu-latest\n    permissions:\n      id-token: write\n    steps:\n      - run: 'true'\n"),
		"any step of that job can get the App's token with one request",
	},
	{
		"a new job runs the action with nothing handed",
		appendToFile(agentOtherRel, "\n  another:\n    runs-on: ubuntu-latest\n    permissions:\n      contents: read\n      id-token: write\n    steps:\n      - uses: anthropics/claude-code-action@"+
			strings.Repeat("a", 40)+"\n"),
		"a new agent job is the case the rule is for",
	},
	{
		"the lower-level action is an agent too",
		appendToFile(agentOtherRel, "\n  another:\n    runs-on: ubuntu-latest\n    permissions:\n      contents: write\n    steps:\n      - uses: anthropics/claude-code-base-action@"+
			strings.Repeat("a", 40)+"\n        with:\n          github_token: ${{ github.token }}\n"),
		"the action beside it runs the same model with the token it is given",
	},
	{
		"the fixer's job no longer grants the identity",
		replaceInFile(agentHubRel, "      id-token: write          # the identity the action exchanges for the Claude App's token (above)\n", ""),
		"an entry of agentAppTokenJobs that is no longer so would excuse the next job of that name",
	},
	{
		"the reviewer is handed the job's token and its entry stays",
		replaceInFile(agentReviewRel, "          allowed_bots: \"claude,github-actions\"\n", "          github_token: ${{ github.token }}\n          allowed_bots: \"claude,github-actions\"\n"),
		"a named job whose step is handed a token is not what its entry says",
	},
	{
		"the reviewer no longer asks for read-only contents",
		replaceInFile(agentReviewRel, "            contents: read\n            issues: read\n", "            issues: read\n"),
		"without the line the reviewer's token has contents at write: it can push and merge",
	},
	{
		"the reviewer asks for nothing",
		replaceInFile(agentReviewRel, "          additional_permissions: |\n            contents: read\n            issues: read\n", ""),
		"with no wish the action mints contents, pull requests and issues at write",
	},
	{
		"the reviewer's wish gains a line nobody measured",
		replaceInFile(agentReviewRel, "            contents: read\n            issues: read\n", "            contents: read\n            issues: read\n            workflows: write\n"),
		"the action's page lists `workflows: write` among what can be asked for",
	},
	{
		"the fixer asks for more",
		replaceInFile(agentHubRel, "          additional_permissions: |\n            actions: read\n", "          additional_permissions: |\n            actions: write\n"),
		"the fixer's wish is named too, so that a wider one is a change someone sees",
	},
	{
		"the reviewer's own token can write contents",
		replaceInFile(agentReviewRel, "      contents: read           # the checkout, and the boundary of the agent's git: no push\n", "      contents: write          # the checkout\n"),
		"the reviewer's git presents the job's own token: with contents at write it can push",
	},
	{
		"the fixer's own token gains a permission",
		replaceInFile(agentHubRel, "      actions: read            # `additional_permissions: actions: read`", "      actions: write           # `additional_permissions: actions: read`"),
		"the fixer's git presents the job's own token too: what it may do is named whole",
	},
	{
		"a second agent step in the reviewer's job",
		addAgentStepBefore(agentReviewRel, "      - name: Review\n"),
		"the wish is one step's: a step beside it gets the App's token at write",
	},
	{
		"the reviewer's wish is overridden after a blank line",
		replaceInFile(agentReviewRel, "            contents: read\n            issues: read\n", "            contents: read\n            issues: read\n\n            contents: write\n"),
		"YAML continues the block across the blank line, and the action takes the later line",
	},
	{
		"the notes' agents can write issues, after a blank line",
		replaceInFile(agentNotesRel, "      contents: read            # the boundary: nothing in this job, agent or shell, can write to the repository\n",
			"      contents: read            # the boundary: nothing in this job, agent or shell, can write to the repository\n\n      issues: write\n"),
		"a blank line does not end a block",
	},
	{
		"a job whose first line carries a comment grants the identity",
		appendToFile(agentOtherRel, "\n  another:                # a comment after the job's name, as ci.yml's `fast` has\n    runs-on: ubuntu-latest\n    permissions:\n      id-token: write\n    steps:\n      - run: 'true'\n"),
		"a job this check does not read is a job it does not hold",
	},
	{
		"the identity in the inline form",
		appendToFile(agentOtherRel, "\n  another:\n    runs-on: ubuntu-latest\n    permissions: {contents: read, id-token: write}\n    steps:\n      - run: 'true'\n"),
		"three workflows here already write their permissions that way",
	},
	{
		"everything granted at once",
		appendToFile(agentOtherRel, "\n  another:\n    runs-on: ubuntu-latest\n    permissions: write-all\n    steps:\n      - run: 'true'\n"),
		"`write-all` grants `id-token` with the rest",
	},
	{
		"a workflow grants the identity to all its jobs",
		replaceInFile(agentOtherRel, "permissions: {contents: read}", "permissions:\n  contents: read\n  id-token: write"),
		"a grant above the jobs reaches every one of them, named or not",
	},
	{
		"an agent's job says no permissions of its own",
		replaceInFile(agentEmailRel, "    permissions:\n      contents: read            # the checkout and the releases.", "    env:\n      UNUSED: read              # the checkout and the releases."),
		"without a block of its own the job takes the repository's default, which this file cannot read",
	},
}

// addAgentStepBefore puts a step that runs the action, at the workflow's own pin and with nothing handed,
// before the step that begins with anchor.
func addAgentStepBefore(rel, anchor string) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		file := filepath.Join(root, rel)
		//nolint:gosec // G304: file is filepath.Join(t.TempDir(), <constant from the table above>).
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		pin := regexp.MustCompile(`anthropics/claude-code-action@[0-9a-f]{40}`).FindString(string(data))
		if pin == "" || strings.Count(string(data), anchor) != 1 {
			t.Fatalf("%s names no pinned action, or not the anchor %q once, so this mutation would be vacuous", rel, anchor)
		}
		//nolint:gosec // G703: the target is under t.TempDir().
		if err := os.WriteFile(file, []byte(strings.Replace(string(data), anchor, "      - uses: "+pin+"\n"+anchor, 1)), 0o600); err != nil {
			t.Fatalf("writing %s: %v", file, err)
		}
	}
}

// replacePinInFile puts replacement where the workflow names the action and its pin, once.
func replacePinInFile(rel, replacement string) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		file := filepath.Join(root, rel)
		//nolint:gosec // G304: file is filepath.Join(t.TempDir(), <constant from the table above>).
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		pin := regexp.MustCompile(`claude-code-action@[0-9a-f]{40}`).FindString(string(data))
		if pin == "" {
			t.Fatalf("%s names no pinned action, so this mutation would be vacuous", rel)
		}
		//nolint:gosec // G703: the target is under t.TempDir().
		if err := os.WriteFile(file, []byte(strings.Replace(string(data), pin, replacement, 1)), 0o600); err != nil {
			t.Fatalf("writing %s: %v", file, err)
		}
	}
}

// TestAgentTokenCheckPassesOnAnUnmutatedCopy is the positive control of the mutation cases.
func TestAgentTokenCheckPassesOnAnUnmutatedCopy(t *testing.T) {
	t.Parallel()
	var rec recorder
	checkAgentTokens(&rec, copyWorkflows(t))
	if len(rec.msgs) != 0 {
		t.Errorf("the check reported %d problem(s) on an unmutated copy: %s", len(rec.msgs), strings.Join(rec.msgs, "; "))
	}
}

func TestAgentTokenCheckFailsOnAMutatedCopy(t *testing.T) {
	t.Parallel()
	for _, m := range agentTokenMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			root := copyWorkflows(t)
			m.mutate(t, root)
			var rec recorder
			checkAgentTokens(&rec, root)
			if len(rec.msgs) == 0 {
				t.Fatalf("the mutation changed nothing the check can see, so the check is vacuous (%s)", m.because)
			}
			t.Logf("caught: %s", strings.Join(rec.msgs, "; "))
		})
	}
}
