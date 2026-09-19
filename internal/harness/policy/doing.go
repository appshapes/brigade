package policy

import (
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/appshapes/brigade/internal/harness/teamfile"
)

// A Verdict is ScanDoingRules's answer about `brigade doing` (card 25,
// plan 5.2): whether the permission rules in the settings Brigade can
// read would ask or deny the call, exactly allow it, or say nothing.
type Verdict string

// The three verdicts. The hook maps them to the doing mode: Blocked →
// unasked (the verb still publishes — Claude Code's own rule does the
// asking or denying — and no line is ever printed), Allowed → allowed,
// None → quiet.
const (
	VerdictNone    Verdict = "none"
	VerdictBlocked Verdict = "blocked"
	VerdictAllowed Verdict = "allowed"
)

// The two literal commands the model would run (plan 5.1: the sentence
// travels in a quoted heredoc, `--clear` removes it), matched against
// every ask and deny pattern in the closing direction.
var doingCommands = []string{"brigade doing <<'EOF'", "brigade doing --clear"}

// allowSpellings are the allow entries that count, exactly (plan 5.2):
// the loose matcher below is used only in the CLOSING direction, so an
// allow that merely happens to glob the command — `Bash(brig*)`, say — is
// not read as consent to print a line.
var allowSpellings = []string{
	"Bash",
	"Bash(brigade:*)",
	"Bash(brigade *)",
	"Bash(brigade*)",
	"Bash(brigade doing:*)",
	"Bash(brigade doing *)",
	"Bash(brigade doing*)",
}

// DoingRuleFiles names the candidate settings files of the scan, in a
// stable order with duplicates removed: <claudeConfigDir>/settings.json,
// then `.claude/settings.json` and `.claude/settings.local.json` under
// each of dirs and under the repository toplevel of each (the hook passes
// CLAUDE_PROJECT_DIR and its cwd; Claude Code reads the project files
// from the project root, which is the toplevel when the session started
// in a subdirectory). An empty claudeConfigDir contributes no user file —
// ScanDoingRules treats that as Blocked on its own.
func DoingRuleFiles(claudeConfigDir string, dirs []string) []string {
	var files []string
	add := func(p string) {
		if !slices.Contains(files, p) {
			files = append(files, p)
		}
	}
	if claudeConfigDir != "" {
		add(filepath.Join(claudeConfigDir, "settings.json"))
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		roots := []string{dir}
		if top, ok := teamfile.Toplevel(dir); ok {
			roots = append(roots, top)
		}
		for _, root := range roots {
			add(filepath.Join(root, ".claude", "settings.json"))
			add(filepath.Join(root, ".claude", "settings.local.json"))
		}
	}
	return files
}

// ScanDoingRules reads the candidate settings files for the permission
// rules that touch `brigade doing` and answers one Verdict (plan 5.2).
// readFile is injected (nil means os.ReadFile) so tests never touch a real
// config directory; the candidate list itself is not — DoingRuleFiles asks
// teamfile.Toplevel about each of dirs on the real filesystem, so a test
// with an injected reader still names real (or fixture) directories. It
// is NOT a reuse of ScanNative's error semantics: that scan is best effort
// and a broken file is a miss, where this one decides whether a line may
// tell the model to run a command, so every doubt closes it. Blocked
// when: claudeConfigDir is empty (the user file cannot be found, so an ask
// or deny there cannot be ruled out); any candidate exists but cannot be
// read, is over MaxSettingsBytes, or does not parse (json/v2 rejects a
// duplicated member; a `permissions` that is not an object, or an array
// member that is not a string, is a parse failure too); or any ask or
// deny entry in ANY candidate matches — the union over every file,
// because union is the fail-closed operation and this scan cannot model
// the native precedence exactly. Only fs.ErrNotExist means "no rules" for
// that file: a `.claude` that is a regular file (ENOTDIR) or a parent
// directory this process cannot search reads as Blocked, on purpose —
// closed, not a bug. Otherwise Allowed when an allow entry in any file is
// exactly one of allowSpellings; otherwise None. Nothing read from a
// settings file is returned, kept or logged: the answer is one of three
// words.
func ScanDoingRules(claudeConfigDir string, dirs []string, readFile func(string) ([]byte, error)) Verdict {
	if readFile == nil {
		readFile = os.ReadFile
	}
	if claudeConfigDir == "" {
		return VerdictBlocked
	}
	allowed := false
	for _, path := range DoingRuleFiles(claudeConfigDir, dirs) {
		data, err := readFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || len(data) > MaxSettingsBytes {
			return VerdictBlocked
		}
		var doc struct {
			Permissions *struct {
				Allow []string `json:"allow"`
				Ask   []string `json:"ask"`
				Deny  []string `json:"deny"`
			} `json:"permissions"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return VerdictBlocked
		}
		if doc.Permissions == nil {
			continue
		}
		for _, entry := range slices.Concat(doc.Permissions.Ask, doc.Permissions.Deny) {
			if ruleMatchesDoing(entry) {
				return VerdictBlocked
			}
		}
		for _, entry := range doc.Permissions.Allow {
			if slices.Contains(allowSpellings, entry) {
				allowed = true
			}
		}
	}
	if allowed {
		return VerdictAllowed
	}
	return VerdictNone
}

// ruleMatchesDoing reports whether one ask or deny entry covers either of
// doingCommands, reading the entry loosely (the closing direction): its
// tool part — the text before an optional parenthesised pattern —
// glob-matches `Bash` with `*` standing for any text (bare `Bash`, `*`,
// `B*`), and either there is no pattern (or an empty one: every Bash
// call) or the pattern matches the command, where a trailing `:*` is a
// plain prefix (`brigade:*`, `brigade doing:*`) and any other `*` is any
// text (`brigade doing*`, `*`). So `Bash(brigade send*)` does not match
// (ruling 4: the send gate does not cover the new verb), `Read(…)` never
// does, and `Bash(brigade doing --clear)` matches the second command
// exactly.
func ruleMatchesDoing(entry string) bool {
	entry = strings.TrimSpace(entry)
	tool, pattern, hasPattern := entry, "", false
	if i := strings.IndexByte(entry, '('); i >= 0 && strings.HasSuffix(entry, ")") {
		tool, pattern, hasPattern = entry[:i], entry[i+1:len(entry)-1], true
	}
	if !glob(strings.TrimSpace(tool), "Bash") {
		return false
	}
	if !hasPattern || pattern == "" {
		return true
	}
	for _, cmd := range doingCommands {
		if prefix, ok := strings.CutSuffix(pattern, ":*"); ok {
			if strings.HasPrefix(cmd, prefix) {
				return true
			}
			continue
		}
		if glob(pattern, cmd) {
			return true
		}
	}
	return false
}

// glob reports whether s matches pattern, where `*` matches any run of
// characters (including none) and every other character matches itself.
// It is written out rather than borrowed from path.Match so that `?`,
// `[` and `\` in a rule mean nothing special: this matcher only ever
// widens what counts as blocked.
func glob(pattern, s string) bool {
	star, rest, hasStar := strings.Cut(pattern, "*")
	if !hasStar {
		return pattern == s
	}
	if !strings.HasPrefix(s, star) {
		return false
	}
	s = s[len(star):]
	for i := 0; i <= len(s); i++ {
		if glob(rest, s[i:]) {
			return true
		}
	}
	return false
}
