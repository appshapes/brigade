package commands

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"

	json "encoding/json/v2"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/protocol"
)

// This file is the repo-file team surface of P7-5 (the brief's §2 and
// §3): `team create` writes the committed `.brigade.json` and the local
// credential; `team join` is the member's one parameterless command; the
// per-checkout pin records the human's consent and the join secret gates
// every cross-team re-point. The frozen adapter protocol is driven as
// internal plumbing: the team key is the `--profile` value the child
// sees, and nothing on the wire changes.

// orphanMaxAge is how old a tmp-* credential directory must be before
// create and list prune it — long enough that a live create can never be
// swept, short enough that a crashed one does not linger for weeks.
const orphanMaxAge = time.Hour

// createOptions is `team create`'s parsed flag set.
type createOptions struct {
	url, key, name, label, adapter, secretFile string
	force                                      bool
}

// teamCreate implements the rebuilt `brigade team create` (brief §2):
// one command in the admin's terminal, cwd anywhere inside the checkout.
func (inv Invocation) teamCreate(raw rawArgs) error {
	opts, err := parseCreateFlags(raw.Rest)
	if err == nil && raw.Adapter != "" {
		opts.adapter = raw.Adapter
	}
	if err != nil {
		return err
	}
	top, err := inv.requireToplevel("team create")
	if err != nil {
		return err
	}
	if err := checkSecretFileOutside(opts.secretFile, top); err != nil {
		return err
	}
	filePath := filepath.Join(top, teamfile.FileName)
	if _, statErr := inv.lstat(filePath); statErr == nil && !opts.force {
		return &protocol.Error{
			Code:    protocol.CodeConflict,
			Message: "this project already has a " + teamfile.FileName + "; pass --force to replace it",
			Details: map[string]string{"reason": "team_file_exists"},
		}
	}
	req, err := inv.createRequest(opts)
	if err != nil {
		return err
	}
	answer, tmpKey, t, err := inv.driveCreate(opts, req)
	if err != nil {
		return err
	}
	return inv.finishCreate(opts, top, filePath, answer, tmpKey, t)
}

// parseCreateFlags parses `team create`'s flags; --secret-file is
// mandatory (owner ruling 1) and never absolutized (the archived
// DECISIVE finding: a relative path deposits a live secret inside the
// repository, the one place it must never exist).
func parseCreateFlags(args []string) (*createOptions, error) {
	var o createOptions
	fs := flag.NewFlagSet("team create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.url, "url", "", "")
	fs.StringVar(&o.key, "key", "", "")
	fs.StringVar(&o.name, "name", "", "")
	fs.StringVar(&o.label, "label", "", "")
	fs.StringVar(&o.adapter, "adapter", config.BundledAdapterName, "")
	fs.StringVar(&o.secretFile, "secret-file", "", "")
	fs.BoolVar(&o.force, "force", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return nil, usage("team create takes --url, --key, --name, --label, --adapter, --secret-file and --force")
	}
	if o.url == "" || o.key == "" {
		return nil, usage("team create needs --url and --key (the backend the team will live in)")
	}
	if o.secretFile == "" {
		return nil, usage("team create needs --secret-file: the join secret goes to that file (0600), never to your terminal")
	}
	if !filepath.IsAbs(o.secretFile) {
		return nil, usage("--secret-file must be an absolute path outside this project (a relative path would land in the repository)")
	}
	return &o, nil
}

// checkSecretFileOutside refuses an absolute --secret-file that resolves
// inside the discovered repository toplevel: the secret must never live
// under the project directory, however it is spelled.
func checkSecretFileOutside(secretFile, top string) error {
	// A `..` component is refused outright (P7-11, verifier finding): Dir
	// and Clean collapse `lnk/..` lexically, while the kernel resolves the
	// symlink first — so `<outside>/lnk/../x.secret` with `lnk` pointing
	// into the checkout would pass both spellings below and land inside.
	if slices.Contains(strings.Split(secretFile, string(filepath.Separator)), "..") {
		return usage("--secret-file must not contain a `..` component; spell the path plainly")
	}
	dir := filepath.Clean(filepath.Dir(secretFile))
	// Both the literal spelling and the symlink-resolved one are checked:
	// a path may be inside the checkout under either form and must not
	// slip through the other (canonicalization can fail on a not-yet-
	// existing directory — the literal check still holds then).
	canonTop, terr := teamfile.Canonicalize(top)
	canonDir, derr := teamfile.Canonicalize(dir)
	if isUnder(dir, filepath.Clean(top)) || (terr == nil && derr == nil && isUnder(canonDir, canonTop)) {
		return usage("--secret-file must not be inside this project: the join secret must never live under the repository")
	}
	return nil
}

// isUnder reports whether dir equals top or sits beneath it.
func isUnder(dir, top string) bool {
	return dir == top || strings.HasPrefix(dir+string(filepath.Separator), top+string(filepath.Separator))
}

// createRequest builds the stdin document from flags or the terminal.
func (inv Invocation) createRequest(opts *createOptions) (*protocol.TeamCreateRequest, error) {
	name, label := opts.name, opts.label
	if name == "" {
		if !inv.Deps.isTerminal(inv.In) {
			return nil, usage("team create needs --name when stdin is not a terminal")
		}
		var err error
		if name, err = inv.promptLine("team name: "); err != nil {
			return nil, err
		}
		if label == "" {
			if label, err = inv.promptLine("your display label (optional): "); err != nil {
				return nil, err
			}
		}
	}
	req := &protocol.TeamCreateRequest{TeamName: name, HumanLabel: label}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return req, nil
}

// driveCreate runs the adapter half of create under a temp key: the
// backend pair (supabase only — the extras are Supabase-specific and the
// fs adapter's `profile init` takes only --force), then `team create`
// with the secret going straight to --secret-file, captured stdio
// throughout.
func (inv Invocation) driveCreate(opts *createOptions, req *protocol.TeamCreateRequest) (*teamCreateAnswer, string, *target, error) {
	tmpKey, err := write.TempKey()
	if err != nil {
		return nil, "", nil, err
	}
	t, err := inv.dialectTarget(opts.adapter, tmpKey)
	if err != nil {
		return nil, "", nil, err
	}
	if _, err := write.PruneOrphans(t.configDir, orphanMaxAge, inv.Deps.now()); err != nil {
		inv.logger().Debug("orphan prune failed", "err", err)
	}
	initFlags := []string{"--force"}
	if opts.adapter == config.BundledAdapterName {
		initFlags = []string{"--url", opts.url, "--key", opts.key}
	}
	if _, err := t.client.Call(context.Background(), "profile", "init", initFlags, nil); err != nil {
		return nil, "", nil, err
	}
	env, err := t.client.Call(context.Background(), "team", "create", []string{"--secret-file", opts.secretFile}, req)
	if err != nil {
		return nil, "", nil, err
	}
	var answer teamCreateAnswer
	if err := json.Unmarshal(env.Result, &answer); err != nil || answer.TeamRef == "" {
		return nil, "", nil, protocolMismatch("team create answered without a team_ref")
	}
	return &answer, tmpKey, t, nil
}

// teamCreateAnswer is the captured `team create --secret-file` result:
// the secret went to the file and is not here.
type teamCreateAnswer struct {
	TeamRef      string `json:"team_ref"`
	TeamName     string `json:"team_name"`
	PrincipalRef string `json:"principal_ref"`
}

// finishCreate promotes the temp key, patches the binding's backend
// members (the harness owns adapter/url/publishable_key — correction 6),
// pins this checkout, and writes the 0644 team file at the toplevel.
func (inv Invocation) finishCreate(opts *createOptions, top, filePath string, answer *teamCreateAnswer, tmpKey string, t *target) error {
	key := teamstore.Key(opts.adapter, opts.url, answer.TeamRef)
	if err := write.EnsureBinding(t.configDir, tmpKey, opts.adapter, opts.url, opts.key,
		answer.TeamRef, answer.TeamName, answer.PrincipalRef); err != nil {
		return err
	}
	if err := write.Promote(t.configDir, tmpKey, key); err != nil {
		return err
	}
	canon, err := teamfile.Canonicalize(top)
	if err != nil {
		return fmt.Errorf("commands: canonicalize checkout: %w", err)
	}
	if err := write.Pin(t.configDir, canon, teamstore.Pin{
		Adapter: opts.adapter, URL: opts.url, PublishableKey: opts.key,
		TeamRef: answer.TeamRef, ConsentedAt: inv.Deps.now().UTC(),
	}); err != nil {
		return err
	}
	doc, err := json.Marshal(map[string]any{
		"version": 1, "adapter": opts.adapter, "url": opts.url,
		"publishable_key": opts.key, "team_ref": answer.TeamRef,
		"team_name": protocol.SanitizeName(answer.TeamName),
	})
	if err != nil {
		return fmt.Errorf("commands: marshal team file: %w", err)
	}
	if err := adapterkit.WriteAtomicMode(filePath, append(doc, '\n'), 0o644); err != nil {
		return err
	}
	return writeLines(inv.Out,
		"created team \""+protocol.SanitizeName(answer.TeamName)+"\" ("+sanitizeID(answer.TeamRef)+")",
		"wrote "+teamfile.FileName+" at the repository toplevel",
		"the join secret is in "+opts.secretFile+" (0600); share it over a password-grade channel only",
		"next: git add "+teamfile.FileName+" && git commit && git push — the file carries only public values",
	)
}

// teamJoin implements the rebuilt parameterless `brigade team join`
// (brief §3): TTY-only on the repo-file path; a non-TTY join reads the
// one stdin document exactly as before and never opens the repo file.
func (inv Invocation) teamJoin(args []string) error {
	label, secretFile, err := parseJoinFlags(args)
	if err != nil {
		return err
	}
	if !inv.Deps.isTerminal(inv.In) && !inv.inSession() {
		return usage("team join without a terminal reads the stdin document form: pipe a TeamJoinRequest and pass --team (the scripted path) — the repo file is read at a terminal or inside a Claude Code session")
	}
	top, err := inv.requireToplevel("team join")
	if err != nil {
		return err
	}
	if secretFile != "" {
		// Create's rules, unchanged: absolute, and never under the
		// checkout in either spelling (P7-11). A join's file exists, so its
		// real location is checked too: a symlink outside the checkout to
		// a file inside it is inside. Nothing else about the file is
		// checked — not its mode, not its owner (owner ruling 4).
		if !filepath.IsAbs(secretFile) {
			return usage("--secret-file must be an absolute path outside this project (a relative path would land in the repository)")
		}
		if err := checkSecretFileOutside(secretFile, top); err != nil {
			return err
		}
		if resolved, rerr := filepath.EvalSymlinks(secretFile); rerr == nil {
			if err := checkSecretFileOutside(resolved, top); err != nil {
				return err
			}
		}
	}
	path, ok := teamfile.Discover(inv.mustGetwd())
	if !ok {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "this project has no " + teamfile.FileName + "; ask the team's administrator for it, or create the team with `brigade team create`",
			Details: map[string]string{"reason": "team_file_missing"},
		}
	}
	f, err := teamfile.Parse(path)
	if err != nil {
		return err
	}
	return inv.joinWithFile(path, f, label, secretFile)
}

// parseJoinFlags: the two optional flags are --label and, since P7-11,
// --secret-file (the join secret read from a 0600 file — the only secret
// source inside a session, where stdin is /dev/null; accepted at a
// terminal too). Everything else comes from the project's team file.
func parseJoinFlags(args []string) (label, secretFile string, err error) {
	fs := flag.NewFlagSet("team join", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&label, "label", "", "")
	fs.StringVar(&secretFile, "secret-file", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return "", "", usage("team join takes only --label and --secret-file; everything else comes from the project's " + teamfile.FileName)
	}
	return label, secretFile, nil
}

// joinWithFile runs the consent gate and either the first join (secret)
// or the re-consent path (brief §5 with the review's high fix: the
// secret is required for any pin rewrite that changes adapter, url or
// team_ref; only a publishable-key-only drift re-consents without one).
func (inv Invocation) joinWithFile(path string, f *teamfile.File, label, secretFile string) error {
	t, err := inv.dialectTarget(f.Adapter, teamstore.Key(f.Adapter, f.URL, f.TeamRef))
	if err != nil {
		return err
	}
	canon, err := teamfile.Canonicalize(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("commands: canonicalize checkout: %w", err)
	}
	binding, berr := teamstore.LoadBinding(t.configDir, t.profile)
	if berr == nil && binding.TeamRef == f.TeamRef {
		return inv.reconsent(t, canon, f, binding, secretFile)
	}
	if berr != nil && !bindingMissing(berr) {
		return berr
	}
	return inv.firstJoin(t, canon, f, label, secretFile)
}

// bindingMissing reports "no credential for this team yet": the raw
// fs.ErrNotExist or the kit's profile_missing config refusal — both mean
// the first-join path, not a failure.
func bindingMissing(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	var pe *protocol.Error
	return errors.As(err, &pe) && pe.Details["reason"] == "profile_missing"
}

// confirmGate shows the human what the file says before any secret is
// typed — host, name and ref from the file, the adapter named whenever
// it is not the bundled one (review low fix 6).
func (inv Invocation) confirmGate(f *teamfile.File, action string) error {
	line := action + " team \"" + nameLine(f.TeamName) + "\" (" + sanitizeID(f.TeamRef) + ") at " + attrLine(hostOf(f.URL))
	if f.Adapter != config.BundledAdapterName {
		line += " via adapter \"" + attrLine(f.Adapter) + "\""
	}
	if inv.inSession() {
		// No terminal to ask on — stdin is /dev/null — so the invocation
		// is the consent: typed by the person with `!`, or run by the model
		// through the Bash tool (where default mode shows the argv in a
		// permission dialog). The line below is what the transcript
		// records (P7-11). Every membership-grade path still needs the
		// secret file.
		return writeLines(inv.Out, line+" — consented by this invocation")
	}
	ok, err := inv.confirm(line + "? [y/N] ")
	if err != nil {
		return err
	}
	if !ok {
		return &protocol.Error{
			Code:    protocol.CodeUsage,
			Message: "join declined; nothing was changed",
			Details: map[string]string{"reason": "declined"},
		}
	}
	return nil
}

// firstJoin is the member's first join in this checkout: confirm, read
// the secret (no echo), check its embedded team ref against the file
// BEFORE any network (review medium fix 4 — no existence oracle), then
// the frozen stdin document to the adapter, then binding + pin.
func (inv Invocation) firstJoin(t *target, canon string, f *teamfile.File, label, secretFile string) error {
	var secret string
	var err error
	if inv.inSession() {
		// Inside a session the secret file is read AND checked — shape and
		// team — before the consent line is printed, so a refusal leaves
		// no half-record on stdout; at a terminal the gate comes first,
		// then the no-echo read, then the same checks.
		if secret, err = inv.joinSecret(secretFile); err != nil {
			return err
		}
		if err := checkJoinSecret(secret, f); err != nil {
			return err
		}
		if err := inv.confirmGate(f, "join"); err != nil {
			return err
		}
	} else {
		if err := inv.confirmGate(f, "join"); err != nil {
			return err
		}
		if secret, err = inv.joinSecret(secretFile); err != nil {
			return err
		}
		if err := checkJoinSecret(secret, f); err != nil {
			return err
		}
	}
	backend, err := json.Marshal(map[string]string{"url": f.URL, "publishable_key": f.PublishableKey})
	if err != nil {
		return fmt.Errorf("commands: marshal backend: %w", err)
	}
	req := &protocol.TeamJoinRequest{JoinSecret: secret, HumanLabel: label, Backend: backend}
	env, err := t.client.Call(context.Background(), "team", "join", nil, req)
	if err != nil {
		return err
	}
	var answer protocol.TeamJoinResult
	if uerr := json.Unmarshal(env.Result, &answer); uerr != nil || answer.Validate() != nil {
		return protocolMismatch("team join answered a shape this build does not read")
	}
	if err := write.EnsureBinding(t.configDir, t.profile, f.Adapter, f.URL, f.PublishableKey,
		answer.TeamRef, answer.TeamName, answer.PrincipalRef); err != nil {
		return err
	}
	return inv.writePinAndReport(t, canon, f, "joined")
}

// checkJoinSecret is the local half of a join, before any network (review
// medium fix 4 — no existence oracle): the secret parses, and its
// embedded team ref is the file's.
func checkJoinSecret(secret string, f *teamfile.File) error {
	parsed, err := protocol.ParseJoinSecret(secret)
	if err != nil {
		return err
	}
	if parsed.TeamRef() != f.TeamRef {
		return &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "this secret is for a different team than this project's " + teamfile.FileName + " names; nothing was sent",
			Details: map[string]string{"reason": "secret_team_mismatch"},
		}
	}
	return nil
}

// reconsent handles `team join` where a credential for the file's team
// already exists: live membership via the core `session list`
// (correction 4), then the per-field diff against the pin, then either
// the secret-free key-only path (which also rewrites the binding —
// correction 1) or a fresh secret join for a cross-team move.
func (inv Invocation) reconsent(t *target, canon string, f *teamfile.File, binding *adapterkit.Profile, secretFile string) error {
	pin, pinned, err := teamstore.LookupPin(t.configDir, canon)
	if err != nil {
		return err
	}
	if pinned && pin.Matches(f) && binding.PublishableKey == f.PublishableKey {
		return writeLines(inv.Out, "already joined: this checkout is pinned to team \""+f.TeamName+"\" ("+sanitizeID(f.TeamRef)+")")
	}
	if _, err := t.client.ListSessions(context.Background(), false); err != nil {
		// The credential no longer works (revoked server-side, or the
		// key rotated under it): fall back to a normal secret join.
		return inv.firstJoin(t, canon, f, "", secretFile)
	}
	if pinned {
		inv.printDiff(pin, f)
	}
	crossTeam := pinned && (pin.Adapter != f.Adapter || pin.URL != f.URL || pin.TeamRef != f.TeamRef)
	if crossTeam && inv.inSession() {
		// The membership-grade path prints its own consent line, after the
		// secret file has been checked: no half-record on a refusal.
		return inv.firstJoin(t, canon, f, "", secretFile)
	}
	if err := inv.confirmGate(f, "re-consent to"); err != nil {
		return err
	}
	if crossTeam {
		// The high fix: a cross-team re-point is a membership-grade event.
		return inv.firstJoin(t, canon, f, "", secretFile)
	}
	if binding.PublishableKey != f.PublishableKey {
		// Key-only drift: rewrite pin AND binding together, no secret.
		if f.Adapter == config.BundledAdapterName {
			if _, err := t.client.Call(context.Background(), "profile", "init",
				[]string{"--force", "--url", f.URL, "--key", f.PublishableKey}, nil); err != nil {
				return err
			}
		}
		if err := write.PatchBindingBackend(t.configDir, t.profile, f.Adapter, f.URL, f.PublishableKey); err != nil {
			return err
		}
	}
	return inv.writePinAndReport(t, canon, f, "re-consented")
}

// printDiff shows old → new for every drifted pin member.
func (inv Invocation) printDiff(pin *teamstore.Pin, f *teamfile.File) {
	pairs := []struct{ name, old, new string }{
		{"adapter", attrLine(pin.Adapter), attrLine(f.Adapter)},
		{"backend host", attrLine(hostOf(pin.URL)), attrLine(hostOf(f.URL))},
		{"publishable key", attrLine(pin.PublishableKey), attrLine(f.PublishableKey)},
		{"team", sanitizeID(pin.TeamRef), sanitizeID(f.TeamRef)},
	}
	for _, p := range pairs {
		if p.old != p.new {
			_ = writeLines(inv.Out, "  "+p.name+": "+p.old+" → "+p.new)
		}
	}
}

// writePinAndReport records consent and prints the one success line.
func (inv Invocation) writePinAndReport(t *target, canon string, f *teamfile.File, did string) error {
	if err := write.Pin(t.configDir, canon, teamstore.Pin{
		Adapter: f.Adapter, URL: f.URL, PublishableKey: f.PublishableKey,
		TeamRef: f.TeamRef, ConsentedAt: inv.Deps.now().UTC(),
	}); err != nil {
		return err
	}
	name := nameLine(f.TeamName)
	if inv.inSession() {
		head := did + " team \"" + name + "\" (" + sanitizeID(f.TeamRef) + ")"
		pid, perr := config.ClaudePID(inv.Environ)
		if perr == nil {
			// A session that already has a map is attached — to this team
			// (nothing to do) or to another (the prompt hook only registers
			// an UNmapped session, so only a restart moves it).
			if m, merr := config.Session(inv.Environ, t.stateDir); merr == nil {
				if m.TeamRef == f.TeamRef {
					return writeLines(inv.Out, head+"; this session is already attached to it")
				}
				return writeLines(inv.Out, head+"; this session stays on team \""+nameLine(m.TeamName)+"\" until /reload-plugins or a new session")
			}
			// The prompt hook re-registers an unmapped session once a
			// minute (P5-18); without its stamp the very next prompt
			// attaches this one.
			_ = os.Remove(config.RegisterRetryStamp(t.stateDir, pid))
		}
		return writeLines(inv.Out, head+"; this session attaches at your next prompt")
	}
	return writeLines(inv.Out, did+" team \""+name+"\" ("+sanitizeID(f.TeamRef)+"); sessions in this checkout attach on their next start")
}

// joinSecret is the secret of a first join or a cross-team re-point: from
// --secret-file when given — the only source inside a session, where
// stdin is /dev/null and nothing can be typed — else the no-echo read at
// the terminal. The file is the one `team create` wrote and the
// administrator sent; its first line, trimmed, is the secret. Brigade
// checks WHERE the file is (teamJoin: outside the repository) and nothing
// about its mode or owner (owner ruling 4, 2026-09-08): where a member
// keeps it is theirs. The refusal for a session that omits the flag says
// so and never asks for the secret itself.
func (inv Invocation) joinSecret(secretFile string) (string, error) {
	if secretFile == "" {
		if inv.inSession() {
			return "", usage("team join inside a Claude Code session needs --secret-file <path>: save the secret file your administrator sent you outside this project, then run this again — never paste the secret into the chat")
		}
		return inv.readSecret()
	}
	data, err := readSecretFile(secretFile)
	if err != nil {
		return "", secretFileUnreadable(err)
	}
	first, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(first), nil
}

// maxSecretFileBytes bounds what joinSecret reads: a secret file is one
// short line, and anything past this is not one.
const maxSecretFileBytes = 64 << 10

// readSecretFile reads path, following symlinks, up to maxSecretFileBytes.
func readSecretFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the member's own --secret-file, named on their own argv
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxSecretFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSecretFileBytes {
		return nil, errSecretFileTooLarge
	}
	return data, nil
}

// errSecretFileTooLarge is readSecretFile's refusal of a file that is not
// a secret file; secretFileUnreadable names it.
var errSecretFileTooLarge = errors.New("secret file too large")

// secretFileUnreadable maps a --secret-file read failure to a fixed
// message: a missing file, a file too large to be a secret file, and any
// other I/O failure (a directory, a permission denied). The file's
// contents are never part of any message.
func secretFileUnreadable(err error) error {
	if errors.Is(err, errSecretFileTooLarge) {
		return &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "the file named by --secret-file is not a secret file (larger than 64 KiB)",
			Details: map[string]string{"reason": "secret_file_too_large"},
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the file named by --secret-file does not exist",
			Details: map[string]string{"reason": "secret_file_missing"},
		}
	}
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: "the file named by --secret-file could not be read",
		Details: map[string]string{"reason": "secret_file_unreadable"},
	}
}

// storeDir is the config directory a store READER uses: the shell's outside
// a session; inside one, the map's when the session is attached, else the
// start facts' (an in-session create or join just happened, or SessionStart
// failed), else the XDG default. Writers (create, join) go through
// dialectTarget, which insists on the start facts.
func (inv Invocation) storeDir() (string, error) {
	configDir, err := config.BrigadeConfigDir(inv.Environ)
	if err != nil || !inv.inSession() {
		return configDir, err
	}
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return "", err
	}
	if m, merr := config.Session(inv.Environ, stateDir); merr == nil {
		return m.ConfigDir, nil
	}
	if facts, ferr := inv.startFacts(stateDir); ferr == nil {
		return facts.ConfigDir, nil
	}
	return configDir, nil
}

// startFacts reads what SessionStart recorded for this process (P7-11):
// above all the store the hooks use, which the config_dir option names
// and which never reaches the Bash tool's environment. A session without
// the file cannot know where to write and refuses rather than guess.
func (inv Invocation) startFacts(stateDir string) (*sessionmap.StartFacts, error) {
	pid, err := config.ClaudePID(inv.Environ)
	if err != nil {
		return nil, err
	}
	facts, err := (sessionmap.Store{StateDir: stateDir}).ReadStart(pid)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "this session has no start facts (the SessionStart hook did not run, or the plugin was enabled mid-session), so the store to write is unknown; run /reload-plugins, then run this again",
			Details: map[string]string{"reason": config.ReasonNotRegistered},
		}
	}
	return facts, err
}

// dialectTarget builds a terminal target for an adapter NAME (the repo
// file's dialect or --adapter): resolution is strictly user-side —
// adapters.json by name, the bundled adapter for "supabase" — through
// the existing option-spec parser; an unknown name refuses here, at join
// time, never execution (review low fix 6).
func (inv Invocation) dialectTarget(adapterName, key string) (*target, error) {
	configDir, err := config.BrigadeConfigDir(inv.Environ)
	if err != nil {
		return nil, err
	}
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return nil, err
	}
	if inv.inSession() {
		// Inside a session the environment cannot say where the store is
		// (BRIGADE_* is ignored and the config_dir option never reaches the
		// Bash tool): the start facts SessionStart wrote carry the resolved
		// directory, joined or not (P7-11).
		facts, ferr := inv.startFacts(stateDir)
		if ferr != nil {
			return nil, ferr
		}
		configDir = facts.ConfigDir
	}
	ad, err := config.ResolveAdapter(config.Options{}, configDir, adapterName)
	if err != nil {
		return nil, err
	}
	t := &target{profile: key, configDir: configDir, stateDir: stateDir, adapter: ad, defaultAdapter: ad}
	t.client = inv.client(t)
	return t, nil
}

// requireToplevel answers the enclosing repository toplevel or refuses:
// outside any checkout there is nowhere a repo file could ever be
// discovered (correction 7).
func (inv Invocation) requireToplevel(cmd string) (string, error) {
	top, ok := teamfile.Toplevel(inv.mustGetwd())
	if !ok {
		return "", &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: cmd + " runs inside a project checkout: the team file lives at the repository toplevel, and there is none here",
			Details: map[string]string{"reason": "no_repository"},
		}
	}
	return top, nil
}

// --- the small seams -----------------------------------------------------

// mustGetwd is the working directory through the seam.
func (inv Invocation) mustGetwd() string {
	if inv.Deps.Getwd != nil {
		if d, err := inv.Deps.Getwd(); err == nil {
			return d
		}
	}
	d, err := osGetwd()
	if err != nil {
		return "."
	}
	return d
}

// lstat is os.Lstat through a seam-free helper (create's existing-file
// probe needs no injection: the txtar tests use real directories).
func (inv Invocation) lstat(path string) (fs.FileInfo, error) {
	return osLstat(path)
}

// readSecret reads the join secret without echo, through the seam.
func (inv Invocation) readSecret() (string, error) {
	if inv.Deps.ReadSecret != nil {
		s, err := inv.Deps.ReadSecret()
		return strings.TrimSpace(s), err
	}
	return readSecretTerminal(inv.Err)
}

// confirm prints prompt and reads a y/N answer, through the seam.
func (inv Invocation) confirm(prompt string) (bool, error) {
	if inv.Deps.Confirm != nil {
		return inv.Deps.Confirm(prompt)
	}
	if _, err := io.WriteString(inv.Err, prompt); err != nil {
		return false, err
	}
	line, err := readLine(inv.In)
	if err != nil {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// promptLine prints prompt on stderr and reads one echoed line.
func (inv Invocation) promptLine(prompt string) (string, error) {
	if inv.Deps.PromptLine != nil {
		return inv.Deps.PromptLine(prompt)
	}
	if _, err := io.WriteString(inv.Err, prompt); err != nil {
		return "", err
	}
	return readLine(inv.In)
}

// readLine reads one newline-terminated line from r, byte-wise (no
// buffering that could swallow a later reader's input).
func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return strings.TrimSuffix(b.String(), "\r"), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			if errors.Is(err, io.EOF) && b.Len() > 0 {
				return b.String(), nil
			}
			return "", err
		}
	}
}

// hostOf shows a URL as its host for human lines.
func hostOf(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		host := raw[i+3:]
		if j := strings.IndexByte(host, '/'); j >= 0 {
			host = host[:j]
		}
		return host
	}
	return raw
}

// protocolMismatch is the failure of an adapter answer this build cannot
// read on the captured create/join path.
func protocolMismatch(message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeProtocolMismatch,
		Message: message,
		Details: map[string]string{"reason": "bad_result"},
	}
}

// teamList implements `brigade team list`: the store rendered humanly —
// team, host, principal, pinned checkouts — plus the orphan prune.
func (inv Invocation) teamList() error {
	configDir, err := inv.storeDir()
	if err != nil {
		return err
	}
	if removed, err := write.PruneOrphans(configDir, orphanMaxAge, inv.Deps.now()); err == nil && len(removed) > 0 {
		inv.logger().Debug("pruned orphaned create directories", "count", len(removed))
	}
	entries, keys, err := listBindings(configDir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return writeLines(inv.Out, "no teams joined on this machine; run `brigade team join` inside a project")
	}
	pins, _ := readAllPins(configDir)
	lines := make([]string, 0, len(entries))
	for i, b := range entries {
		var checkouts []string
		for dir, p := range pins {
			if p.TeamRef == b.TeamRef && p.URL == b.URL {
				checkouts = append(checkouts, dir)
			}
		}
		line := columns(
			"team \""+protocol.SanitizeName(b.TeamName)+"\"",
			sanitizeID(b.TeamRef),
			hostOf(b.URL),
			"principal="+idLine(b.PrincipalRef),
			"key="+keys[i],
		)
		if len(checkouts) > 0 {
			line += "  checkouts: " + strings.Join(checkouts, ", ")
		}
		lines = append(lines, line)
	}
	return writeLines(inv.Out, lines...)
}

// listBindings loads every team.json under teams/, skipping tmp-* dirs.
func listBindings(configDir string) ([]*adapterkit.Profile, []string, error) {
	dirs, err := osReadDir(filepath.Join(configDir, "teams"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("commands: list teams: %w", err)
	}
	var out []*adapterkit.Profile
	var keys []string
	for _, d := range dirs {
		if !d.IsDir() || strings.HasPrefix(d.Name(), "tmp-") {
			continue
		}
		p, err := adapterkit.LoadProfile(configDir, d.Name())
		if err != nil || p.TeamRef == "" {
			continue
		}
		out = append(out, p)
		keys = append(keys, d.Name())
	}
	return out, keys, nil
}

// readAllPins loads the pin map for `team list`'s checkout column.
func readAllPins(configDir string) (map[string]teamstore.Pin, error) {
	data, err := adapterkit.ReadStrict(teamstore.PinsPath(configDir))
	if err != nil {
		return nil, err
	}
	var pins teamstore.PinsFile
	if err := json.Unmarshal(data, &pins); err != nil {
		return nil, err
	}
	return pins.Projects, nil
}

// passThroughProfileVerb drives the adapter's frozen profile verbs —
// status, reset, revoke-credentials — under the resolved team key: the
// --profile bridge wins when given (P7-7 deletes it), else the pin
// chain of resolveTeamKey.
func (inv Invocation) passThroughProfileVerb(verb string, raw rawArgs) error {
	key, err := inv.resolveTeamKey(raw.Team)
	if err != nil {
		return err
	}
	// The resolved key IS the target; the pass-through must not resolve
	// the --team flag a second time.
	raw.Team = ""
	t, terr := inv.terminalTarget(key, true)
	if terr != nil {
		return terr
	}
	return inv.spawnThrough(t, "profile", verb, raw.Rest, nil)
}

// resolveTeamKey is the terminal resolution chain (P7-5): an explicit
// --team beats everything and loses to nothing; else the cwd's pin
// governs in a checkout; else the sole local team; an ambiguous store
// REFUSES — the harness never guesses a team. An empty store answers
// the legacy "default" so a machine with no teams behaves as before
// (the adapter's own unconfigured answer does the talking).
func (inv Invocation) resolveTeamKey(teamFlag string) (string, error) {
	configDir, err := inv.storeDir()
	if err != nil {
		return "", err
	}
	return resolveTeamKeyIn(configDir, teamFlag, inv.mustGetwd())
}

// resolveTeamKeyIn is resolveTeamKey against explicit inputs, the unit
// the precedence tests pin (written against this bridge-free chain, so
// P7-7's bridge deletion cannot orphan them).
func resolveTeamKeyIn(configDir, teamFlag, cwd string) (string, error) {
	bindings, keys, err := listBindings(configDir)
	if err != nil {
		return "", err
	}
	if teamFlag != "" {
		var matches []string
		for i, b := range bindings {
			if b.TeamRef == teamFlag || b.TeamName == teamFlag {
				matches = append(matches, keys[i])
			}
		}
		switch len(matches) {
		case 1:
			return matches[0], nil
		case 0:
			return "", &protocol.Error{
				Code:    protocol.CodeConfig,
				Message: "no local team matches --team; `brigade team list` shows what this machine has joined",
				Details: map[string]string{"reason": "team_unknown"},
			}
		default:
			return "", &protocol.Error{
				Code:    protocol.CodeConfig,
				Message: "more than one local team matches --team; use the team_ref instead of the name",
				Details: map[string]string{"reason": "team_ambiguous"},
			}
		}
	}
	if top, ok := teamfile.Toplevel(cwd); ok {
		dir := top
		if path, found := teamfile.Discover(cwd); found {
			dir = filepath.Dir(path)
		}
		if canon, cerr := teamfile.Canonicalize(dir); cerr == nil {
			if pin, found, perr := teamstore.LookupPin(configDir, canon); perr == nil && found {
				return teamstore.Key(pin.Adapter, pin.URL, pin.TeamRef), nil
			}
		}
	}
	switch len(keys) {
	case 0:
		return adapterkit.DefaultProfileName, nil
	case 1:
		return keys[0], nil
	}
	return "", &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: "this machine has joined several teams and this directory pins none; pass --team <ref-or-name>",
		Details: map[string]string{"reason": "team_ambiguous"},
	}
}

// The os seams the tests never need to fake: the repo-file commands run
// against real directories in every test (txtar and unit alike).
var (
	osGetwd   = os.Getwd
	osLstat   = os.Lstat
	osReadDir = os.ReadDir
)

// readSecretTerminal is the production secret reader: the no-echo prompt
// on stderr, the secret from the real terminal, never argv and never
// echoed — the same discipline the adapter's own prompt uses.
func readSecretTerminal(errW io.Writer) (string, error) {
	if _, err := io.WriteString(errW, "join secret (not echoed): "); err != nil {
		return "", err
	}
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, _ = io.WriteString(errW, "\n")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// bindingAdapterName answers the dialect a team key's binding names, ""
// (the bundled adapter) when there is no binding to ask.
func bindingAdapterName(configDir, key string) string {
	p, err := adapterkit.LoadProfile(configDir, key)
	if err != nil {
		return ""
	}
	return p.Adapter
}
