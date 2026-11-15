// move.go is a running session's part in its team's move to another
// backend (work item 327, ADR 0008; the follow itself is work item 273,
// ADR 0003, in internal/harness/teammove).
//
// A session's start follows a move in its own run, which has 55 seconds. A
// prompt hook has 4.5 under a 5 second timeout, and a follow asks one
// backend, joins another and may wait for a lock. So a session that is
// already running follows in two steps, at two of its prompts:
//
//  1. The follow, off the hook's budget. A prompt that finds the team file
//     of the session's checkout naming another backend for the session's
//     team starts a detached child, `brigade hook follow`, and returns at
//     once. The child does what a start's follow does and nothing more: it
//     asks the backend the checkout joined, compares the record with the
//     file member by member, joins or reuses the membership, writes the
//     binding and the pin. It has no session: no inbox socket, no messaging
//     token, no map. At most one is started a minute for a checkout,
//     whichever of its sessions is prompted.
//  2. The re-attachment, in the hook. A later prompt finds the checkout's
//     pin matching the team file while the session's map still names the
//     backend the team left. It says the follow's line, registers the
//     session anew where the team is, writes the map and starts the
//     watcher, whose guard retires the old one; that one closes the old
//     session on its way out. The registration ends a second short of the
//     hook's budget, so that a session that moves is always told.
//
// The checkout is the one the session registered in, which its map names,
// and never another that a prompt happens to come from (moveSession). A
// session's start in the same process (`/clear`) keeps a session where a
// prompt would keep it: while messages wait for it, and when the new
// backend cannot be reached (connect, stay).
//
// What makes a session follow does not change: the word of the backend its
// member joined the team on, never the file's. The file only decides that a
// question is asked.
package hook

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teammove"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
)

const (
	// followBudget bounds the child's whole follow, as startBudget bounds
	// the follow of a session's start: teammove.Follow puts no deadline on
	// its own calls. It is Deps.FollowBudget's production value.
	followBudget = 55 * time.Second
	// followInterval is the least time between two follows started for one
	// checkout. A follow asks the backend the checkout joined one question,
	// and a team file that names a backend nobody recorded would otherwise
	// have it asked at every prompt.
	followInterval = time.Minute
	// moveLogLimit is the size at which the child's log is set aside for a
	// fresh one, one generation kept.
	moveLogLimit = 1 << 20
	// moveRegisterSlack is what the registration of a move leaves of the
	// prompt hook's budget. A registration that succeeds writes the map, and
	// what follows it (the watcher, the lines) has to end before the hook's
	// timeout: Claude Code discards everything a prompt hook printed when
	// it reaches its timeout, and a session whose id changed with nothing
	// said is what the two steps exist to prevent. So the registration ends
	// a second short of the budget, and one that is answered later than
	// that is given up as one that failed.
	moveRegisterSlack = time.Second
	// followedMarkVersion is the version of the mark a follow leaves.
	followedMarkVersion = 1
	// movedNoticeVersion is the version of the notice a move leaves.
	movedNoticeVersion = 1
)

// The lines a running session hears about its team's move, each once in a
// conversation (tell). The first two echo nothing from the team file, which
// nobody has consented to when they are said.
const (
	// movedLine is said when the file comes to name another backend for the
	// session's team and a follow was started.
	movedLine = "Brigade: the .brigade.json of the checkout this session was started in now names another backend for this session's team. Brigade is asking the backend this session is on whether the team's administrator recorded that move there: if so, this session follows at one of its next prompts and says so; if not, it stays where it is, and `brigade team join` reviews the change."
	// movedRetryLine is movedLine for a session that is attached to nothing:
	// its registration retry started the follow (followMove), which may join
	// the new backend before the session's next prompt.
	movedRetryLine = "Brigade: this project's .brigade.json names another backend for the team you joined here. Brigade is asking the backend you joined it on whether the team's administrator recorded that move there: if so, this session connects there at one of its next prompts and says so; if not, it stays unconnected, and `brigade team join` reviews the change."
	// movedStaysLine is said instead when the member's options keep a
	// session from following.
	movedStaysLine = "Brigade: the .brigade.json of the checkout this session was started in now names another backend for this session's team. This session does not follow a move by itself (the option follow_team_move is off, or the session names its adapter itself): it stays where it is until you join the team there with `brigade team join`, and moves at its next prompt after that."
	// movedEachOtherLine is said when the follow found that the backend the
	// file names itself records that the team moved back to the backend the
	// session is on (work item 341): the session follows neither.
	movedEachOtherLine = "Brigade: the .brigade.json of the checkout this session was started in names another backend for this session's team, and that backend and the one this session is on each record that the team moved to the other. This session follows neither and stays where it is. The team's administrator clears the record that is out of date: `brigade team move --show --from <host>` reads one, and `brigade team move --clear --from <host>` clears it."
	// movedRefusedLine is said, once on a machine for one refusal
	// (refusalUntold), when the backend the file names refused the join a
	// follow made there under the membership this machine holds (work item
	// 341): the session stays, and its member is told the command that
	// helps.
	movedRefusedLine = "Brigade: the .brigade.json of the checkout this session was started in names another backend for this session's team, and that backend refuses the membership this machine holds there. This session stays where it is. To join the team there, run `brigade team join --secret-file <path>` with the file of the team's current join secret, which its administrator has; this session moves at its next prompt after that."
	// moveUnreachedLine is said when the checkout is joined to the backend
	// the team went to and the session could not be registered there.
	moveUnreachedLine = "Brigade: this session could not be registered on the backend its team moved to. It stays where it is and tries again at a later prompt."
)

// The words the team-moved stamp holds, one for each line above and below
// that the conversation has heard.
const (
	toldAsked     = "asked"
	toldStays     = "stays"
	toldFollowed  = "followed"
	toldWaits     = "waits"
	toldUnreached = "unreached"
	toldEachOther = "each-other"
)

// moveWaitsLine is said when the checkout followed and the session cannot
// yet: messages that are held for it on the backend it is leaving stay
// there, so it stays with them until they are released.
func moveWaitsLine(n int) string {
	what, it := strconv.Itoa(n)+" team messages are", "them"
	if n == 1 {
		what, it = "1 team message is", "it"
	}
	return "Brigade: this session stays on the backend it is on for now: " + what + " held for it there, and a held message does not move with a session. Run `brigade inbox` in your own terminal to read " + it + ", then `brigade inbox release` to deliver " + it + "; this session moves at its next prompt after that."
}

// rejoinedLineOf is the line of a session that moves because its member
// joined the team's new backend in the session's checkout with a command of
// their own: nothing was followed, so the follow's line would not be true.
// The file's values are consented to by then, and sanitised all the same.
// checkout is how the line names the checkout (followedLineOf).
func rejoinedLineOf(checkout string, tf *teamfile.File) string {
	where := attr(teammove.Host(tf.URL))
	if tf.Certificate != "" {
		where += ", trusting the server certificate " + certLine(tf.Certificate)
	}
	return "Brigade: " + checkout + " is joined to team \"" + attr(tf.TeamName) + "\" at " + where + " now, and this session moves there with it."
}

// checkoutWords names the session's own checkout, canon, in a line a prompt
// says: "this checkout" when the prompt comes from it, and what it is to
// the session when the prompt comes from anywhere else. A prompt's
// directory follows the model, and the lines of a move are said from
// wherever the session works.
func checkoutWords(in input, canon string) string {
	if path, found := teamfile.Discover(in.Cwd); found {
		if from, err := teamfile.Canonicalize(filepath.Dir(path)); err == nil && from == canon {
			return thisCheckout
		}
	}
	return startedCheckout
}

// A followedMark is what a follow that ran outside a session's start leaves
// for the next hook of the checkout (config.MoveFollowedMark): the team key
// it followed to, and whether this machine joined there with the move's
// ticket. Nothing of the ticket, of the label or of a backend's answer is
// in it.
type followedMark struct {
	Version    int       `json:"version"`
	TeamKey    string    `json:"team_key"`
	Joined     bool      `json:"joined"`
	FollowedAt time.Time `json:"followed_at"`
	// Reason is set in the mark of a follow that did not follow
	// (config.MoveNotFollowedMark): one of teammove's Reason words.
	Reason string `json:"reason,omitzero"`
}

// readFollowedMark reads the mark at path under the strict private-file
// rules. It answers false for a file that is not there, cannot be trusted,
// or is of a follow to another backend than teamKey names.
func readFollowedMark(path, teamKey string) (followedMark, bool) {
	data, err := adapterkit.ReadStrict(path)
	if err != nil {
		return followedMark{}, false
	}
	var mark followedMark
	if err := json.Unmarshal(data, &mark); err != nil || mark.Version != followedMarkVersion || mark.TeamKey != teamKey {
		return followedMark{}, false
	}
	return mark, true
}

// dropFollowedMark removes the checkout's mark: its line was said, or the
// follow it speaks of is no longer the checkout's.
func (r *run) dropFollowedMark(f facts, opts config.Options, canon string) {
	path := config.MoveFollowedMark(f.stateDir, opts.ConfigDir, canon)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		r.log.Debug("the follow's mark was not removed", log.Err(err))
	}
}

// tellFollowed says the line of a follow that ran outside a session's
// start, at the first start or registration retry of a session in the
// checkout after it: the session that asked, or, when that one ended
// between the follow's two steps, the next to start there. It says the line
// once, the mark being removed with it, and only for the pin the mark
// speaks of. A prompt that moves a running session says the line itself
// (reattach).
func (r *run) tellFollowed(f facts, opts config.Options, tf *teamfile.File, canon string, pin *teamstore.Pin) {
	if r.moving {
		return
	}
	mark, marked := readFollowedMark(config.MoveFollowedMark(f.stateDir, opts.ConfigDir, canon), teamstore.Key(tf.Adapter, tf.URL, tf.TeamRef))
	if !marked || pin.MovedFrom == "" {
		return
	}
	r.sayFollowed(followedLine(tf, &teamstore.Pin{URL: pin.MovedFrom}, mark.Joined, r.commandName(f)), canon)
	r.dropFollowedMark(f, opts, canon)
	r.forgetTold(f)
}

// sayFollowed says line, the line of a follow of the checkout canon, and
// keeps it: the start may yet keep a running session and drop everything
// its resolution said (keepUnresolved), and this line is said all the same.
func (r *run) sayFollowed(line, canon string) {
	r.say(line)
	r.saidFollowed = true
	r.followLine, r.followCheckout = line, canon
}

// conversation is the name a conversation has in the team-moved stamp: its
// native id, or a dash for a document that carries none a file may hold.
func conversation(in input) string {
	if sessionmap.CheckNativeID(in.SessionID) != nil {
		return "-"
	}
	return in.SessionID
}

// told is the words this conversation has heard about its team's move. A
// stamp that is not there, cannot be trusted, or names another conversation
// is no words: a session that died left it, or a /clear began a
// conversation that has heard nothing.
func (r *run) told(f facts, in input) []string {
	data, err := adapterkit.ReadStrict(config.TeamMovedStamp(f.stateDir, f.pid))
	if err != nil {
		return nil
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 || fields[0] != conversation(in) {
		return nil
	}
	return fields[1:]
}

// tell says line once in a conversation. The word is recorded before the
// line is said, and a line that cannot be recorded is not said: a state
// directory that cannot be written costs the session a line and never
// earns it one at every prompt.
func (r *run) tell(f facts, in input, word, line string) bool {
	if slices.Contains(r.told(f, in), word) || !r.fits(line) || !r.note(f, in, word) {
		return false
	}
	r.say(line)
	return true
}

// note records that this conversation has heard word's line, and reports
// whether the stamp now holds it. tell records through it before it says a
// line; a start that said the follow's line on its own way (followMove,
// tellFollowed) records it here, so that a prompt of the same conversation
// does not say it again.
func (r *run) note(f facts, in input, word string) bool {
	words := r.told(f, in)
	if slices.Contains(words, word) {
		return true
	}
	path := config.TeamMovedStamp(f.stateDir, f.pid)
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		r.log.Debug("state directory not created; no line about the move", log.Err(err))
		return false
	}
	stamp := strings.Join(append([]string{conversation(in)}, append(words, word)...), " ") + "\n"
	if err := adapterkit.WriteAtomic(path, []byte(stamp)); err != nil {
		r.log.Debug("prompt: the team-moved stamp was not written; no line about the move", log.Err(err))
		return false
	}
	return true
}

// forgetTold removes the team-moved stamp: the move it spoke of is over
// for this session.
func (r *run) forgetTold(f facts) {
	if err := os.Remove(config.TeamMovedStamp(f.stateDir, f.pid)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		r.log.Debug("the team-moved stamp was not removed", log.Err(err))
	}
}

// moveLogPath is the one log every follow child of this machine appends
// to: ${stateDir}/logs/team-move.log.
func moveLogPath(stateDir string) string {
	return filepath.Join(stateDir, "logs", "team-move.log")
}

// startFollow starts the follow of the checkout's move as a detached
// child, through the spawner the watcher is started with, and returns
// without waiting for it. It reports whether a follow was started for the
// checkout now or within followInterval. The attempt's stamp is written
// BEFORE the child is started, and no child is started without it: the
// bound on how often the old backend is asked must hold even for a hook
// that is stopped a moment later. A child that could not be started asked
// nobody, so its stamp is taken away again: the next prompt tries, and no
// prompt in between says that a follow is under way. The child gets an
// environment built from scratch — the adapter children's allow-list, so
// neither the inbox socket nor the messaging token nor any variable that
// names a session — and its arguments, none of them a secret.
func (r *run) startFollow(ctx context.Context, f facts, opts config.Options, canon string) bool {
	stamp := config.MoveAttemptStamp(f.stateDir, opts.ConfigDir, canon)
	now := r.deps.Now()
	if data, err := adapterkit.ReadStrict(stamp); err == nil {
		if last, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data))); perr == nil {
			// A stamp from the future is a clock that was set back: it bounds
			// nothing, and is written again below.
			if since := now.Sub(last); since >= 0 && since < followInterval {
				r.log.Debug("a follow of this checkout's move was started within the last minute; none is started now")
				return true
			}
		}
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(stamp)); err != nil {
		r.log.Debug("state directory not created; no follow is started", log.Err(err))
		return false
	}
	if err := adapterkit.WriteAtomic(stamp, []byte(now.Format(time.RFC3339Nano)+"\n")); err != nil {
		r.log.Debug("the follow's attempt stamp was not written; no follow is started", log.Err(err))
		return false
	}
	logPath := moveLogPath(f.stateDir)
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > moveLogLimit {
		_ = os.Rename(logPath, logPath+".1")
	}
	dir := f.home
	if dir == "" || !filepath.IsAbs(dir) {
		dir = f.stateDir
	}
	pid, err := r.deps.Spawner.Spawn(ctx, SpawnSpec{
		Args: []string{"hook", SubFollow, "--checkout", canon, "--config-dir", opts.ConfigDir, "--state-dir", f.stateDir,
			"--key", strconv.Itoa(f.pid), "--log-level", r.logLevel},
		Env:     adapterkit.ChildEnv(r.environ),
		Dir:     dir,
		LogPath: logPath,
	})
	if err != nil {
		// No child runs, so nobody was asked and there is nothing to bound:
		// the stamp goes, or the prompts of the next minute would each find
		// it and say that the old backend is being asked.
		if rerr := os.Remove(stamp); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			r.log.Debug("the follow's attempt stamp was not removed", log.Err(rerr))
		}
		r.log.Warn("the follow of the team's move was not started", log.Err(err))
		return false
	}
	r.log.Info("started the follow of the team's move; a later prompt finds its outcome", slog.Int("follow_pid", pid))
	return true
}

// follow is `brigade hook follow`, the child startFollow detaches: the
// follow of one checkout's move, with no session. It reads the team file
// and the pin itself, from the checkout and the store its arguments name,
// so nothing the prompt hook read is taken on trust, and it decides nothing
// the start's follow does not decide: teammove.Follow asks the backend the
// pin names and follows only on its record. Before it writes the pin it
// leaves the mark that lets the next hook of the checkout say so, and it
// removes the registration-retry stamp of the session that started it, so
// that a session attached to nothing registers at its very next prompt. It
// prints nothing: both of its streams are the log file. Run inside a
// session it refuses: the option that turns the follow off is read by a
// session's hooks, which are what start this.
func (r *run) follow() int {
	a := r.args
	if config.InSession(r.environ) {
		cmd, _ := cli.Lookup("hook")
		return cli.Usage(cmd, nil, r.streams, "hook follow is started by a session's prompt hook and is not run inside a session")
	}
	tf, err := teamfile.Parse(filepath.Join(a.checkout, teamfile.FileName))
	if err != nil {
		r.log.Info("follow: the checkout's team file is not usable; nothing is followed", log.Err(err))
		return 0
	}
	canon, err := teamfile.Canonicalize(a.checkout)
	if err != nil || canon != a.checkout {
		r.log.Warn("follow: the checkout is not named by its canonical path; nothing is followed")
		return 0
	}
	pin, pinned, err := teamstore.LookupPin(a.configDir, canon)
	switch {
	case err != nil:
		r.log.Warn("follow: pin store", log.Err(err))
		return 0
	case !pinned || !teammove.Candidate(pin, tf):
		r.log.Info("follow: the team file names no other backend for the team this checkout joined; nothing is followed")
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.deps.FollowBudget)
	defer cancel()
	out, err := teammove.Follow(ctx, teammove.Inputs{
		ConfigDir: a.configDir, Checkout: canon, File: tf, Pin: pin,
		Client: r.moveClient(a.configDir, a.stateDir),
		Now:    r.deps.Now, Log: r.log,
		// The mark is down before the pin is, so a prompt that finds the
		// pin finds how the checkout got there.
		BeforePin: func(joined bool) {
			r.leaveFollowedMark(a, canon, teamstore.Key(tf.Adapter, tf.URL, tf.TeamRef), joined)
		},
	})
	// Why it did not follow is this follow's to say or to take back, where a
	// session has a line for it: the next prompt of the checkout reads the
	// mark.
	r.markNotFollowed(a.stateDir, a.configDir, canon, teamstore.Key(tf.Adapter, tf.URL, tf.TeamRef), out.Reason)
	if !out.Followed {
		attrs := []any{slog.String("reason", out.Reason)}
		if err != nil {
			attrs = append(attrs, log.Err(err))
		}
		r.log.Info("the team file names another backend for this team, and the move was not followed", attrs...)
		return 0
	}
	r.log.Info("followed the team's recorded move", slog.Bool("joined", out.Joined))
	if a.key > 0 {
		_ = os.Remove(config.RegisterRetryStamp(a.stateDir, a.key))
	}
	return 0
}

// markNotFollowed keeps the checkout's mark of why its last follow did not
// follow (config.MoveNotFollowedMark) true to the follow that just ended.
// It is written for a reason the next prompt has a line of its own for:
// the backend the team moved to not answering, refusing the membership
// this machine holds there, or naming the backend the checkout joined as
// that one names it. It is left as it is by a follow that learned nothing
// about that backend, because it could not ask: one that did not get the
// lock, one whose store failed, one whose own backend did not answer.
// Until that a follow that never reached the server removed what the last
// one to reach it had left, and the next prompt said that the team file
// does not match, of a server that was only down. Every other outcome
// removes it, a follow that followed included. Best effort: without it a
// prompt says what it always said, that the team file does not match.
func (r *run) markNotFollowed(stateDir, configDir, canon, teamKey, reason string) {
	path := config.MoveNotFollowedMark(stateDir, configDir, canon)
	if learnedNothing(reason) {
		return
	}
	switch reason {
	case teammove.ReasonNewBackendFailed, teammove.ReasonNewBackendRefused, teammove.ReasonEachOther:
	default:
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			r.log.Debug("the mark of a follow that did not follow was not removed", log.Err(err))
		}
		return
	}
	data, err := json.Marshal(followedMark{Version: followedMarkVersion, TeamKey: teamKey, FollowedAt: r.deps.Now().UTC(), Reason: reason})
	if err == nil {
		if err = adapterkit.MkdirPrivate(filepath.Dir(path)); err == nil {
			err = adapterkit.WriteAtomic(path, append(data, '\n'))
		}
	}
	if err != nil {
		r.log.Debug("the mark of a follow that did not follow was not written", log.Err(err))
	}
}

// learnedNothing reports whether a follow that ended with reason could not
// ask about the backend the team file names: it did not get the lock, its
// store failed, or the backend the member joined did not answer it. Such a
// follow has no word of its own on that backend. The checkout's mark stays
// as the last follow that asked left it (markNotFollowed), and a session's
// start says what that one left, as a prompt does (followMove).
func learnedNothing(reason string) bool {
	switch reason {
	case teammove.ReasonBusy, teammove.ReasonStore, teammove.ReasonOldBackendFailed:
		return true
	}
	return false
}

// dropNotFollowedMark removes the checkout's mark of why its last follow
// did not follow: the checkout has nothing to follow now.
func (r *run) dropNotFollowedMark(f facts, opts config.Options, canon string) {
	path := config.MoveNotFollowedMark(f.stateDir, opts.ConfigDir, canon)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		r.log.Debug("the mark of a follow that did not follow was not removed", log.Err(err))
	}
}

// lastNotFollowed is why the last follow of the checkout did not follow,
// where it left a mark for the backend the team file names now
// (markNotFollowed), and "" otherwise.
func (r *run) lastNotFollowed(f facts, opts config.Options, canon string, tf *teamfile.File) string {
	mark, marked := readFollowedMark(config.MoveNotFollowedMark(f.stateDir, opts.ConfigDir, canon), teamstore.Key(tf.Adapter, tf.URL, tf.TeamRef))
	if !marked {
		return ""
	}
	return mark.Reason
}

// leftByLastFollow is what a hook that cannot ask for itself says of the
// backend the team file names, from what the last follow of the checkout
// that asked left: "" where it left nothing, and the caller says that the
// team file does not match. It is the one reading for a prompt, whose
// follow runs beside it and has nobody to tell, and for a session's start
// whose own follow learned nothing (learnedNothing); until both read it
// here a start said that the team file does not match over a mark that
// the prompt after it then said (work item 341). Two backends that name
// each other come first. Then a refusal of the membership this machine
// holds that the member has not been told of, which is in the member's
// store and not in the mark, and is told once (refusalUntold). Then
// whatever else the mark says: that the backend did not answer, or that it
// refuses the membership. The mark is read and never changed here.
func (r *run) leftByLastFollow(f facts, opts config.Options, canon string, tf *teamfile.File) string {
	last := r.lastNotFollowed(f, opts, canon, tf)
	if last != teammove.ReasonEachOther && r.refusalUntold(f, opts, teamstore.Key(tf.Adapter, tf.URL, tf.TeamRef)) {
		return teammove.ReasonRejoinRefused
	}
	return last
}

// refusalUntold reports whether the member has yet to be told that the
// backend teamKey names refused the join a follow made there under the
// membership this machine holds (teamstore.RejoinRefusal), and records that
// they now are: the caller says the line. A member is told of a refusal
// once, whichever session or checkout of theirs comes to it first, and not
// at every start: the refusals since a join there last succeeded are one
// refusal. The record is in the member's store, where a follow that ran
// beside a prompt left it with nobody to tell; what was told is in the
// state directory. A state directory that cannot be written costs the
// member the line and never earns it at every start.
func (r *run) refusalUntold(f facts, opts config.Options, teamKey string) bool {
	refusal, err := teamstore.LoadRejoinRefusal(opts.ConfigDir, teamKey)
	if err != nil {
		return false
	}
	which := refusal.FirstRefusedAt.UTC().Format(time.RFC3339Nano)
	path := config.MoveRefusalToldMark(f.stateDir, opts.ConfigDir, teamKey)
	if told, rerr := adapterkit.ReadStrict(path); rerr == nil && strings.TrimSpace(string(told)) == which {
		return false
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		r.log.Debug("state directory not created; no line about the refused join", log.Err(err))
		return false
	}
	if err := adapterkit.WriteAtomic(path, []byte(which+"\n")); err != nil {
		r.log.Debug("the mark of a refused join that was told was not written; no line about it", log.Err(err))
		return false
	}
	return true
}

// leaveFollowedMark writes the mark of a follow that is about to write
// the pin. A mark an earlier follow to the same backend left untold keeps
// its word that the machine joined there with the ticket: that follow
// joined and was stopped before its pin, this one found the membership,
// and the member has still to hear of the join. Best effort: without the
// mark the session that asked still says the follow's line when it moves,
// as one that found the machine joined already.
func (r *run) leaveFollowedMark(a hookArgs, canon, teamKey string, joined bool) {
	path := config.MoveFollowedMark(a.stateDir, a.configDir, canon)
	if earlier, ok := readFollowedMark(path, teamKey); ok && earlier.Joined {
		joined = true
	}
	data, err := json.Marshal(followedMark{Version: followedMarkVersion, TeamKey: teamKey, Joined: joined, FollowedAt: r.deps.Now().UTC()})
	if err == nil {
		if err = adapterkit.MkdirPrivate(filepath.Dir(path)); err == nil {
			err = adapterkit.WriteAtomic(path, append(data, '\n'))
		}
	}
	if err != nil {
		r.log.Warn("follow: the mark for the next hook of this checkout was not written", log.Err(err))
	}
}

// moveSession is the prompt hook's part for a session that is attached:
// when the team file of the checkout it registered in names another backend
// for the session's team, it starts the follow (step 1) or, once that
// checkout's pin matches the file, registers the session anew there (step
// 2). It returns the session's new map when this prompt moved it, and nil
// when the session is where it was. A file that is gone, does not parse,
// names another team or names the backend the session is on is nothing to
// act on. The file is read at every prompt of an attached session and costs
// about 27 microseconds (measured 2026-11-13 on this repository's own team
// file, from three directories down), which is why nothing is kept to avoid
// it.
//
// The checkout is the session's own: the one its map names
// (sessionmap.ByPID.Checkout), whose team file and pin its registration
// was resolved from. Nothing about a move is read from the directory a
// prompt names. That directory follows the model (a `cd`, a worktree,
// another repository), and both ways of reading it were wrong. A session
// that moved on whichever checkout its last prompt came from went back and
// forth between two checkouts of one team on two backends, a new session
// each time. And a session that looked at its own checkout only when a
// prompt came from there never followed while it worked anywhere else,
// which is where a session started in a main checkout and working in a
// worktree spends its life. So the file and the pin that are read are the
// map's checkout's at every prompt, wherever the prompt comes from, and
// the registration of a move is resolved from that checkout too (reattach).
// A map written before the member existed names none: see adoptCheckout.
//
// Grok Build discards what a prompt hook prints, so a session there could
// be told neither that it followed nor the id it has afterwards: it stays
// where it is until it starts again.
func (r *run) moveSession(ctx context.Context, f facts, in input, m *sessionmap.ByPID) *sessionmap.ByPID {
	if f.host == config.HostGrok || m.TeamRef == "" || m.TeamKey == "" || m.BrigadeSessionID == "" {
		return nil
	}
	checkout := m.Checkout
	if checkout == "" {
		// A map from before it named a checkout: the one this prompt comes
		// from is looked at, and taken below when it is joined to the
		// session's team.
		path, found := teamfile.Discover(in.Cwd)
		if !found {
			return nil
		}
		canon, err := teamfile.Canonicalize(filepath.Dir(path))
		if err != nil {
			return nil
		}
		checkout = canon
	}
	tf, err := teamfile.Parse(filepath.Join(checkout, teamfile.FileName))
	if err != nil || tf.TeamRef != m.TeamRef {
		return nil
	}
	here := teamstore.Key(tf.Adapter, tf.URL, tf.TeamRef) == m.TeamKey
	if here && m.Checkout != "" {
		// The common prompt: the file names the backend the session is on.
		return nil
	}
	opts, err := r.options()
	if err != nil {
		r.log.Warn("prompt: options unreadable; the team's move is left alone", log.Err(err))
		return nil
	}
	pin, pinned, err := teamstore.LookupPin(opts.ConfigDir, checkout)
	if err != nil || !pinned {
		return nil
	}
	if m.Checkout == "" && !r.adoptCheckout(f, m, checkout, pin) {
		return nil
	}
	if here {
		return nil
	}
	if pin.Matches(tf) {
		return r.reattach(ctx, f, in, m, opts, tf, checkout, pin)
	}
	if !teammove.Candidate(pin, tf) {
		return nil
	}
	if !followAllowed(opts) {
		if opts.FollowTeamMoveWarning != "" && !slices.Contains(r.told(f, in), toldStays) && r.fits(opts.FollowTeamMoveWarning) {
			r.say(opts.FollowTeamMoveWarning)
		}
		r.tell(f, in, toldStays, movedStaysLine)
		return nil
	}
	if r.lastNotFollowed(f, opts, checkout, tf) == teammove.ReasonEachOther {
		// The last follow of the checkout found the two backends naming
		// each other: the session stays, and says why once.
		r.tell(f, in, toldEachOther, movedEachOtherLine)
	}
	if r.fits(movedRefusedLine) && r.refusalUntold(f, opts, teamstore.Key(tf.Adapter, tf.URL, tf.TeamRef)) {
		// A follow found that the backend the file names refuses this
		// machine's membership and the join made under it: the session
		// stays, and its member is told what helps, once.
		r.say(movedRefusedLine)
	}
	if r.startFollow(ctx, f, opts, checkout) {
		r.tell(f, in, toldAsked, movedLine)
	}
	return nil
}

// adoptCheckout gives a map that names no checkout the one this prompt
// comes from. Such a map was written before the member existed (a session
// that was running when its plugin was updated). The first prompt that
// comes from a checkout joined on this machine to the session's team gives
// it that one, written to the map before anything is done on its strength,
// and from then on the session is that checkout's wherever its prompts come
// from. A map that cannot be written moves nobody: a session whose checkout
// is not on record could be moved again by its next prompt from another
// one.
func (r *run) adoptCheckout(f facts, m *sessionmap.ByPID, canon string, pin *teamstore.Pin) bool {
	if pin.TeamRef != m.TeamRef {
		return false
	}
	m.Checkout, m.UpdatedAt = canon, r.deps.Now()
	if err := (sessionmap.Store{StateDir: f.stateDir}).WriteByPID(m); err != nil {
		m.Checkout = ""
		r.log.Warn("prompt: the session's checkout was not recorded; the team's move is left alone", log.Err(err))
		return false
	}
	r.log.Info("prompt: recorded the checkout of a session whose map named none")
	return true
}

// arrivalLine says how the checkout came to be joined to the backend its
// team file names, for a session m that is still on the one the team left:
// the follow's line when the pin says it moved from the backend the session
// is on, with how the machine joined when a follow left word of it, and the
// line of the member's own join otherwise. checkout is how the line names
// the checkout (followedLineOf).
func (r *run) arrivalLine(f facts, opts config.Options, tf *teamfile.File, canon string, pin *teamstore.Pin, m *sessionmap.ByPID, checkout string) string {
	return arrivalLineFrom(opts.ConfigDir, tf, pin, m.TeamKey, r.joinedByFollow(f, opts, tf, canon), r.commandName(f), checkout)
}

// joinedByFollow reports whether a follow left word, for the checkout, that
// this machine joined the backend the team file names with the move's
// ticket.
func (r *run) joinedByFollow(f facts, opts config.Options, tf *teamfile.File, canon string) bool {
	mark, marked := readFollowedMark(config.MoveFollowedMark(f.stateDir, opts.ConfigDir, canon), teamstore.Key(tf.Adapter, tf.URL, tf.TeamRef))
	return marked && mark.Joined
}

// arrivalLineFrom is arrivalLine for a session that was on the backend the
// team key from names: the follow's line when the pin moved from that
// backend, the line of the member's own join otherwise.
func arrivalLineFrom(configDir string, tf *teamfile.File, pin *teamstore.Pin, from string, joined bool, command, checkout string) string {
	old, err := teamstore.LoadBinding(configDir, from)
	if err != nil || pin.MovedFrom == "" || pin.MovedFrom != old.URL {
		return rejoinedLineOf(checkout, tf)
	}
	return followedLineOf(checkout, tf, &teamstore.Pin{URL: pin.MovedFrom}, joined, command)
}

// A movedNotice is what the prompt that moves a session leaves before it
// registers it (config.MovedNotice): the session and the backend the move
// is from, and what this conversation is still owed of how the checkout
// arrived. It holds no line and nothing of the new backend: a prompt that
// finds it says the lines from the map and the store as they then are.
type movedNotice struct {
	Version int `json:"version"`
	// Was is the id of the session that moves, and From the team key of the
	// backend it is on: a map that still names either was not moved.
	Was  string `json:"was"`
	From string `json:"from"`
	// Arrival is true when the conversation has not heard how the checkout
	// came to be on the new backend; Joined is the follow's word, at the
	// time, that this machine joined there with the move's ticket.
	Arrival bool `json:"arrival,omitzero"`
	Joined  bool `json:"joined,omitzero"`
}

// leaveMovedNotice writes the notice of a move that is about to be made,
// and reports whether it is down. A move whose notice cannot be written is
// not made: were its hook stopped, nothing would tell the session.
func (r *run) leaveMovedNotice(f facts, n movedNotice) bool {
	path := config.MovedNotice(f.stateDir, f.pid)
	data, err := json.Marshal(n)
	if err == nil {
		if err = adapterkit.MkdirPrivate(filepath.Dir(path)); err == nil {
			err = adapterkit.WriteAtomic(path, append(data, '\n'))
		}
	}
	if err != nil {
		r.log.Warn("prompt: the notice of this session's move was not written; the session is not moved", log.Err(err))
		return false
	}
	return true
}

// dropMovedNotice removes the notice of a move: its lines were said and
// the hook is ending, or the move was not made.
func (r *run) dropMovedNotice(f facts) {
	if err := os.Remove(config.MovedNotice(f.stateDir, f.pid)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		r.log.Debug("the notice of a move was not removed", log.Err(err))
	}
}

// sayMoved says the lines of a move whose hook did not: a notice is there
// that a prompt hook left before it registered the session, and the map
// names another session on another backend than the notice does, so the
// move was made and the hook was stopped before it ended. Claude Code
// discards what a hook printed when it stops it, so nothing was said: this
// prompt says how the checkout arrived, when the conversation was owed
// that, and the context line of the session as it is now, with the host's
// own line after it. It also does what that hook does after the map is
// written and so may not have done: the roads to the session are filed for
// the session as it is now, and the state entry of the session the move
// left ends (endLeft). A notice over a map that still names the session it
// was left for is of a registration that was not recorded: the session is
// where it was, and the notice goes. A notice that cannot be read goes too.
func (r *run) sayMoved(ctx context.Context, f facts, in input, m *sessionmap.ByPID) {
	path := config.MovedNotice(f.stateDir, f.pid)
	data, err := adapterkit.ReadStrict(path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var n movedNotice
	if err != nil || json.Unmarshal(data, &n) != nil || n.Version != movedNoticeVersion || n.Was == "" || adapterkit.CheckProfileName(n.From) != nil {
		r.log.Warn("prompt: the notice of a move is not usable; removed")
		r.dropMovedNotice(f)
		return
	}
	if n.Was == m.BrigadeSessionID || n.From == m.TeamKey {
		r.log.Info("prompt: a move of this session was begun and not made; the session is where it was")
		r.dropMovedNotice(f)
		return
	}
	r.log.Info("prompt: the hook that moved this session was stopped before it said so; saying it now")
	// The notice stays until this hook ends, as the one that left it meant
	// it to: were this hook stopped too, the next would say the lines.
	r.movedSaid = true
	// The roads to the session first, which that hook files after the map
	// and so may not have filed (finish): the resume hint of the
	// conversation the map names and, on Muse Code and Codex CLI, the pointer
	// a command in the model's shell finds the session by. A pointer that
	// still names the session the move left is refused beside a map that
	// names another (config.Session), and no hook but a session's start
	// writes one: without this every `brigade` command of the session was
	// refused until it started again. Written again harmlessly when that
	// hook got that far.
	store, now := sessionmap.Store{StateDir: f.stateDir}, r.deps.Now()
	r.writeByNative(store, m.ClaudeSessionID, m, now)
	r.writeByHost(f, store, m.ClaudeSessionID, m, now)
	if n.Arrival {
		if line, canon, opts, ok := r.arrivalAfter(f, in, m, n); ok && r.fits(line) {
			r.say(line)
			r.dropFollowedMark(f, opts, canon)
		}
	}
	r.say(startLine(m.SessionName, m.BrigadeSessionID, m.TeamName, m.Inbound))
	switch {
	case f.muse():
		r.say(armLine(r.commandName(f)))
	case f.codex():
		r.say(codexLine(r.commandName(f)))
	}
	// What the hook that moved the session left undone when it was stopped:
	// what the conversation was told of the move is over, no retry is owed,
	// and the doing line is asked for again, the new registration having
	// none (leftBehind).
	r.forgetTold(f)
	_ = os.Remove(retryStampPath(f.stateDir, f.pid))
	_ = os.Remove(doingStampPath(f.stateDir, f.pid))
	// Last, because it may ask git: the lines are out by now.
	r.endLeft(ctx, f, m, n.Was, now)
}

// endLeft ends the state entry of the session a move left, for the prompt
// that makes good a hook that was stopped (sayMoved). That hook ends the
// entry after it starts the watcher (leftBehind); one that was stopped
// before left an entry that never ends, and the digest reports a session's
// commits and its last note when it ends. The entry is in the state folder
// the session's map names, under the id the notice kept. One that has its
// end is left as it is: the hook that was stopped got that far.
func (r *run) endLeft(ctx context.Context, f facts, m *sessionmap.ByPID, was string, now time.Time) {
	s, ok := stateStore(f, m)
	if !ok {
		return
	}
	if e, err := s.Load(was); err != nil || !e.EndedAt.IsZero() {
		return
	}
	left := *m
	left.BrigadeSessionID = was
	r.stateEnd(ctx, f, &left, "moved", now)
}

// arrivalAfter is the arrival line of a move that was made, for sayMoved:
// read again from the team file and the pin of the session's checkout,
// with the backend and the follow's word the notice kept.
func (r *run) arrivalAfter(f facts, in input, m *sessionmap.ByPID, n movedNotice) (line, canon string, opts config.Options, ok bool) {
	if m.Checkout == "" {
		return "", "", opts, false
	}
	opts, err := r.options()
	if err != nil {
		return "", "", opts, false
	}
	tf, err := teamfile.Parse(filepath.Join(m.Checkout, teamfile.FileName))
	if err != nil {
		return "", "", opts, false
	}
	pin, pinned, err := teamstore.LookupPin(opts.ConfigDir, m.Checkout)
	if err != nil || !pinned || !pin.Matches(tf) {
		return "", "", opts, false
	}
	return arrivalLineFrom(opts.ConfigDir, tf, pin, n.From, n.Joined, r.commandName(f), checkoutWords(in, m.Checkout)), m.Checkout, opts, true
}

// sayArrival says line, the arrival line, once in a conversation, and
// removes the follow's mark with it: its word has been passed on.
func (r *run) sayArrival(f facts, in input, opts config.Options, canon, line string) {
	if r.tell(f, in, toldFollowed, line) {
		r.dropFollowedMark(f, opts, canon)
	}
}

// stampRetry stamps a registration on the team's new backend that failed,
// so that the next one is made a minute later at the earliest.
func (r *run) stampRetry(f facts) {
	stamp := retryStampPath(f.stateDir, f.pid)
	if err := adapterkit.MkdirPrivate(filepath.Dir(stamp)); err != nil {
		r.log.Debug("retry stamp not written", log.Err(err))
		return
	}
	if err := adapterkit.WriteAtomic(stamp, []byte(r.deps.Now().Format(time.RFC3339Nano)+"\n")); err != nil {
		r.log.Debug("retry stamp not written", log.Err(err))
	}
}

// moveContext is the context a move's registration is made under: the
// hook's, ending moveRegisterSlack before it does.
func moveContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline.Add(-moveRegisterSlack))
}

// reattach is step 2: the session's checkout is joined to the backend the
// team file names, and the session's map still names the one the team left.
// The session says how the checkout got there (arrivalLine) and is
// registered anew: with no resume hint and no heartbeat of the id it had,
// which the new backend has never heard of (connect). Its name stays, a
// request for quiet stays, and its doing line is asked for again.
//
// Two things keep it where it is for now. Messages held for it on the
// backend it is leaving stay there, so it stays with them and says so
// once. And a backend that cannot be reached, or does not answer before
// the registration's deadline (moveContext), leaves the map untouched: the
// attempt is made again, a minute later at the earliest, as a registration
// that failed is.
func (r *run) reattach(ctx context.Context, f facts, in input, m *sessionmap.ByPID, opts config.Options, tf *teamfile.File, canon string, pin *teamstore.Pin) *sessionmap.ByPID {
	newKey := teamstore.Key(tf.Adapter, tf.URL, tf.TeamRef)
	line := r.arrivalLine(f, opts, tf, canon, pin, m, checkoutWords(in, canon))
	// The member is told how the checkout got there at the first prompt that
	// knows it, whether or not the session can move yet.
	if held := r.heldCount(f, m); held > 0 {
		r.sayArrival(f, in, opts, canon, line)
		r.tell(f, in, toldWaits, moveWaitsLine(held))
		return nil
	}
	stamp := retryStampPath(f.stateDir, f.pid)
	if data, err := adapterkit.ReadStrict(stamp); err == nil {
		if last, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data))); perr == nil {
			if since := r.deps.Now().Sub(last); since >= 0 && since < registerRetryInterval {
				r.log.Debug("prompt: the last registration on the team's new backend was recent")
				return nil
			}
		}
	}
	r.log.Info("prompt: the team moved; registering this session on the backend it is on now")
	// The notice is down before the session is registered: a hook that is
	// stopped once the map names the new session has said nothing, and the
	// next prompt says it from the notice (sayMoved).
	if !r.leaveMovedNotice(f, movedNotice{
		Version: movedNoticeVersion, Was: m.BrigadeSessionID, From: m.TeamKey,
		Arrival: !slices.Contains(r.told(f, in), toldFollowed), Joined: r.joinedByFollow(f, opts, tf, canon),
	}) {
		return nil
	}
	// The registration is resolved from the session's own checkout, as its
	// first one was: the team, the pin, the workspace label and file sync
	// are that checkout's, wherever this prompt comes from. The hook does
	// not wait for the new watcher's pidfile: nothing here reads it, a
	// watcher that did not come up is started by the next prompt, and the
	// wait was half a second of the time a move has to be said in.
	own := in
	own.Cwd = canon
	var lines []string
	cctx, cancel := moveContext(ctx)
	r.held, r.moving = &lines, true
	r.connect(cctx, f, own, 0)
	r.held, r.moving = nil, false
	cancel()
	next, err := sessionmap.Store{StateDir: f.stateDir}.ReadByPID(f.pid)
	if err != nil || next.TeamKey != newKey || next.BrigadeSessionID == m.BrigadeSessionID {
		// The map is as it was: the session is still where its watcher is.
		// What the registration would have said is dropped, a `not connected`
		// line being untrue of it.
		r.log.Warn("prompt: this session was not registered on the team's new backend; it stays where it is", slog.Int("lines_dropped", len(lines)))
		r.dropMovedNotice(f)
		r.stampRetry(f)
		r.sayArrival(f, in, opts, canon, line)
		r.tell(f, in, toldUnreached, moveUnreachedLine)
		return nil
	}
	// The lines are said, and the notice goes when this hook ends (prompt).
	r.movedSaid = true
	r.sayArrival(f, in, opts, canon, line)
	for _, l := range lines {
		if r.fits(l) {
			r.say(l)
		}
	}
	r.forgetTold(f)
	_ = os.Remove(stamp)
	return next
}

// stay keeps a running session where it is at a session's start that would
// have moved it to the backend its team went to (connect: messages wait for
// it where it is, or the new backend could not be reached). Before the
// start ends as one that continued (keep), it says what a prompt says in
// the same case: how the checkout came to be on the new backend, unless
// this start said so on its own way here, and line, the reason the session
// has not moved. Each is said once in a conversation, under the words a
// prompt records them with, so the prompts after it say neither again.
func (r *run) stay(ctx context.Context, f facts, in input, res resolved, existing *sessionmap.ByPID, now time.Time, watcherRuns bool, word, line string) {
	if r.saidFollowed {
		r.note(f, in, toldFollowed)
	} else if pin, pinned, err := teamstore.LookupPin(res.opts.ConfigDir, res.checkout); err == nil && pinned {
		// A start resolved the checkout its own directory is in.
		r.sayArrival(f, in, res.opts, res.checkout, r.arrivalLine(f, res.opts, res.teamFile, res.checkout, pin, existing, thisCheckout))
	}
	r.tell(f, in, word, line)
	r.keep(ctx, f, in, res, existing, now, watcherRuns)
}

// keep ends a session's start that leaves a running session attached where
// it is, on a backend this start did not resolve. A start is not a prompt:
// `/clear` fires it for another conversation of the same process, so the
// map is kept with what a conversation changes — its id, the name, the
// permission mode, the transcript — and nothing of the backend or the
// checkout, and the start ends with the pointers, the state entry and the
// lines of the session where it is (finishKept).
//
// The backend the session is on hears of the conversation that begins, as
// it does from a start that continues (connect): the name the session has
// now, and that a doing line of the conversation before has ended, a doing
// line living inside one conversation (blankDoingLine). Without it the
// roster went on showing what the cleared conversation was doing for as
// long as the session was kept. The heartbeat goes through a client of the
// map's own adapter and team key, never this start's: the session's id is
// one only the backend that issued it knows. One that fails is the
// watcher's to repeat, and a line it would have blanked stands.
func (r *run) keep(ctx context.Context, f facts, in input, res resolved, existing *sessionmap.ByPID, now time.Time, watcherRuns bool) {
	kept := keptMap(in, existing, res.id.name, now)
	r.tellKept(ctx, f, in, res.id, existing)
	if err := res.store.WriteByPID(kept); err != nil {
		// The session is where it was, under the map it had: a start that
		// cannot write that map again says the session's line all the same,
		// and not that a session which is attached is `not connected`.
		r.log.Warn("the map of the session this start keeps was not refreshed", log.Err(err))
		r.say(startLine(existing.SessionName, existing.BrigadeSessionID, existing.TeamName, existing.Inbound))
		return
	}
	r.finishKept(ctx, f, in, res, kept, now, watcherRuns)
}

// finishKept ends a start that keeps a running session where it is, once
// the session's map is written (keep): it files the pointers and the state
// entry of the conversation that begins, as finish does, and says what is
// true of the session it keeps. That is not what finish says. This start
// resolved the checkout its own directory is in, which is on another
// backend than the session, or the session's own checkout after its team
// file changed; the session took nothing of it. Its watcher was started
// under the map the session had and reads the inbound policy, the frame,
// file sync, the polling and the announcements from there, so the notes of
// the team file this start read, its sync line, its sound and notification
// lines and the warnings of its inbound decision would each be said of
// settings the session does not run under: a `/clear` in a checkout of
// another backend told a session that syncs nothing that file sync was on,
// and one whose policy is accept that it was refuse.
//
// What it says: the context line and the host's line of the session as it
// is; its file sync as its own map holds it; and of what this start did or
// read, only what holds for the session whatever was resolved. That is the
// write this start made to the member's user settings file, a `brigade`
// that shadows the plugin's on the session's PATH, and the warnings of the
// two options that are read where they are used and not frozen in the map:
// follow_team_move, at every prompt, and team_move_in_session, in the
// start facts this start wrote. A change of the session's inbound policy
// is said by the prompt that makes it (redecide).
func (r *run) finishKept(ctx context.Context, f facts, in input, res resolved, kept *sessionmap.ByPID, now time.Time, watcherRuns bool) {
	r.writeByNative(res.store, in.SessionID, kept, now)
	r.writeByHost(f, res.store, in.SessionID, kept, now)
	r.stateStart(ctx, f, res.opts, kept, now)
	r.pruneCache(f, now)
	r.sayContext(f, kept)
	if line := r.keptSyncLine(f, kept, watcherRuns); line != "" {
		r.say(line)
	}
	said := []string{res.ensureLine}
	if path, ok := r.shadowing(f); ok {
		said = append(said, shadowLine(path))
	}
	for _, line := range append(said, res.opts.FollowTeamMoveWarning, res.opts.TeamMoveInSessionWarning) {
		if line != "" {
			r.say(line)
		}
	}
	// The context line is said: a move's notice has nothing left to tell.
	r.dropMovedNotice(f)
}

// keptSyncLine is the file sync line of a session a start keeps, from the
// session's own map: what its watcher was started to sync, never what the
// team file this start read lists. A session whose map syncs nothing says
// nothing: why it does not is the word of the start that resolved it. With
// no watcher running the line says off, as finish's does.
func (r *run) keptSyncLine(f facts, m *sessionmap.ByPID, watcherRuns bool) string {
	switch {
	case m.SyncAdapter == "":
		return ""
	case watcherRuns:
		return syncOnLine(len(m.SyncFolders), m.BoardFolder != "", m.SyncAdapter)
	case !r.canInject(f):
		return syncOffLine(syncOffNoSocket)
	}
	return syncOffLine(syncOffNoWatcher)
}

// keptMap is the map of a session that a start keeps where it is: the map
// it had, with what a conversation changes and nothing else.
func keptMap(in input, existing *sessionmap.ByPID, name string, now time.Time) *sessionmap.ByPID {
	kept := *existing
	kept.ClaudeSessionID = in.SessionID
	kept.SessionName = name
	if in.PermissionMode != "" {
		kept.PermissionMode = in.PermissionMode
	}
	kept.TranscriptPath = transcriptPath(in)
	kept.UpdatedAt = now
	return &kept
}

// tellKept is the heartbeat of a start that keeps a session: to the backend
// the map names, through the map's own adapter, with the name and the
// activity this start resolved.
func (r *run) tellKept(ctx context.Context, f facts, in input, id identity, existing *sessionmap.ByPID) {
	adapter, err := config.AdapterFromArgv(existing.AdapterCommand)
	if err != nil {
		r.log.Warn("the map's adapter command is unusable; the backend this session is on is not told of the conversation that begins", log.Err(err))
		return
	}
	where := resolved{id: id, client: r.client(adapter, existing.TeamKey, existing.ConfigDir, f.stateDir)}
	if herr := r.heartbeat(ctx, where, existing.BrigadeSessionID, existing.Inbound, blankDoingLine(in, existing, existing.DoingMode)); herr != nil {
		r.log.Warn("heartbeat failed; the watcher keeps trying", log.Err(herr))
	}
}

// runningSession is the map of the session that is running in this
// process when a session's start begins: one whose watcher is alive and
// serves it. A start over such a map is a `/clear` of a running session,
// wherever its document's directory is, and connect has to know that before
// the resolution says anything. Nil for a prompt, for Grok Build, whose
// starts are left as they are, and for a map with no live watcher, which
// may be one a dead process left.
func (r *run) runningSession(f facts) *sessionmap.ByPID {
	if !r.starting || f.host == config.HostGrok {
		return nil
	}
	m, err := (sessionmap.Store{StateDir: f.stateDir}).ReadByPID(f.pid)
	if err != nil {
		return nil
	}
	if v := r.checkPidfile(f); !v.Found || !v.Alive || v.Entry.BrigadeSessionID != m.BrigadeSessionID {
		return nil
	}
	return m
}

// stillMember reports whether this machine is still a member of the team a
// session is registered on, on the backend it is on: the binding under the
// map's team key names the map's team. A member who left the team (`team
// leave`) has a binding that names none, and a session of theirs is not one
// to keep.
func stillMember(m *sessionmap.ByPID) bool {
	binding, err := teamstore.LoadBinding(m.ConfigDir, m.TeamKey)
	return err == nil && m.TeamRef != "" && binding.TeamRef == m.TeamRef
}

// keepUnresolved ends a session's start that resolved no team where it
// comes from, over a session that is running (runningSession) and whose
// member is still in its team (stillMember): `/clear` in a checkout that is
// not joined, in a directory with no team file, or over a team file with no
// record behind it. It ends the same way a start whose resolution refused
// the member's own settings, wherever it comes from: an option that cannot
// be read, a frame text that fails a check, an adapter that cannot be
// resolved. Such a start used to return before it read the map,
// with the line of a session that is not connected, or none. The session
// was attached all the same, and its map went on naming the conversation
// that ended: the doing line was not asked for in the one that began, that
// conversation had no resume hint, and on Muse Code its shell found no
// session. So the start keeps the session as a start that resolved another
// backend keeps it (keep): the map takes what a conversation changes, the
// pointers are filed for the conversation that begins, the backend the
// session is on is told, and the line is the context line of the session
// where it is. Nothing of what was resolved is in the map, because nothing
// was. What the resolution said is dropped, being said of a session that
// is not connected or not joined, but for three things the member has to
// hear whatever session they are in: that the team file there was refused
// (keptFileLine), that their own settings were, so that what they set is
// not in force and the session goes on with what it had (keptSettingLine),
// and that the checkout followed its team, which the start did or found
// done before it failed (sayFollowed).
func (r *run) keepUnresolved(ctx context.Context, f facts, in input, existing *sessionmap.ByPID) {
	now := r.deps.Now()
	store := sessionmap.Store{StateDir: f.stateDir}
	id := r.identity(f, in)
	kept := keptMap(in, existing, id.name, now)
	r.tellKept(ctx, f, in, id, existing)
	if err := store.WriteByPID(kept); err != nil {
		// The session is where it was, under the map it had, and the road
		// its shell had to it stays.
		r.log.Warn("the map of the session this start keeps was not refreshed", log.Err(err))
		kept = existing
	} else {
		r.retirePointer(f, store, existing, in)
		r.writeByNative(store, in.SessionID, kept, now)
		r.writeByHost(f, store, in.SessionID, kept, now)
		if opts, oerr := r.options(); oerr == nil {
			r.stateStart(ctx, f, opts, kept, now)
		}
	}
	// The line of a follow this start made, or found made, was held with
	// what the resolution said, and its mark is gone: it is said here, before
	// the context line as a start that follows says it. When the checkout
	// that followed is the session's own, the conversation has now heard how
	// it arrived, and the prompt that moves the session does not say it
	// again, as one that found the machine joined already.
	if r.followLine != "" {
		r.say(r.followLine)
		if existing.Checkout == "" || existing.Checkout == r.followCheckout {
			r.note(f, in, toldFollowed)
		}
	}
	r.sayContext(f, kept)
	if r.fileRefused != "" {
		r.say(keptFileLine(r.fileRefused))
	}
	if r.settingRefused != "" {
		r.say(r.settingRefused)
	}
	// The context line is said: a move's notice has nothing left to tell.
	r.dropMovedNotice(f)
}

// keptFileLine is what a start that keeps a running session says of a team
// file the resolution refused: the refusal's own words (a file that does
// not match the team the member joined, one that holds what looks like a
// secret, one that does not parse), without its claim that the session is
// not connected, which it is.
func keptFileLine(refusal string) string {
	rest := strings.TrimPrefix(strings.TrimPrefix(refusal, "Brigade: not connected: "), "Brigade: not connected ")
	return "Brigade: this session stays where it is. In the directory this conversation began in: " + rest
}

// keptSettingLine is what a start that keeps a running session says when
// what its resolution refused was the member's own settings and not the
// team (work item 327, ADR 0008): what could not be used, the refusal's own
// reason (a fixed word, never a value or a path), what is in force instead,
// and the remedy. The refusal's own line says `not connected` and, for most
// options, `run brigade team join`: neither is true of a session that is
// kept. What is in force is what the session had. The start wrote nothing
// of what it resolved into the map and started no watcher, so the watcher
// that runs goes on reading the frame, the inbound policy and the adapter
// the map held, and it read file sync and the polling intervals when it
// started. A session that starts anew with the same settings connects to
// nothing: the kept one can so be on a weaker frame than the one its
// member tried to set, and the line says which frame holds.
func keptSettingLine(what, reason, effect, remedy string) string {
	if reason == "" {
		reason = "unusable"
	}
	return "Brigade: this session stays connected as it was. " + what + " cannot be used (" + attr(reason) + "), so " + effect +
		". " + remedy + ", then `/clear` or start a new session."
}

// keptOptionLine is keptSettingLine for options that could not be parsed.
// One option that cannot be read is all of them unread: the start resolved
// nothing, and a prompt hook that cannot parse the options does not decide
// the inbound policy again, polls for nothing and follows no move, until
// the option is fixed.
func keptOptionLine(err error) string {
	_, reason := codeOf(err)
	what, remedy := "Your Brigade options", "Fix them"
	var perr *protocol.Error
	if errors.As(err, &perr) && perr.Details["option"] != "" {
		what, remedy = "The `"+attr(perr.Details["option"])+"` option", "Fix the option"
	}
	return keptSettingLine(what, reason,
		"no change to your options is in force here: the session's watcher goes on with the frame and the options the session had, and its prompts read no option until this one is fixed",
		remedy)
}

// keptFrameLine is keptSettingLine for a frame_file whose text could not be
// read or failed a check. The options themselves were read, so only the
// frame is not the member's new one: the watcher, and a prompt's poll, go
// on framing messages as the map says, which is as before.
func keptFrameLine(err error) string {
	_, reason := codeOf(err)
	if reason == "" {
		reason = "frame_file_unreadable"
	}
	return keptSettingLine("The `frame_file` option", reason,
		"the frame your options name now is not in force here: team messages still reach this session under the frame it had",
		"Fix the `frame` or `frame_file` option")
}

// keptAdapterLine is keptSettingLine for an adapter that could not be
// resolved: the adapter_command option, or the adapter the team file of the
// start's directory names, which adapters.json has to hold. The session's
// watcher and its commands go on through the adapter the map names.
func keptAdapterLine(name string, err error) string {
	_, reason := codeOf(err)
	const goesOn = "the session goes on through the adapter it had"
	var perr *protocol.Error
	if errors.As(err, &perr) && perr.Details["source"] == config.SourceOption {
		return keptSettingLine("The `adapter_command` option", reason, "the adapter it names is not in force here: "+goesOn, "Fix the option")
	}
	return keptSettingLine("The adapter \""+attr(name)+"\" that the team file in the directory this conversation began in names", reason,
		"this start changed nothing: "+goesOn, "Run `brigade team status` in a terminal to see why and fix it")
}

// heldCount is how many messages wait for the session in its pending file:
// held under the `hold` policy, or released and not yet delivered. They are
// on the backend the session is on, and a session that left would never be
// given them. A file that cannot be read holds nothing that could be
// delivered, and keeps no session.
func (r *run) heldCount(f facts, m *sessionmap.ByPID) int {
	pending, err := inbound.FilePendingStore{
		Path: inbound.PendingPath(f.stateDir, m.BrigadeSessionID), SessionID: m.BrigadeSessionID,
	}.Load()
	if err != nil {
		r.log.Warn("prompt: pending file refused; it keeps no session from moving", log.Err(err))
		return 0
	}
	return len(pending.Entries)
}

// leftBehind is what a session that was registered anew on its team's new
// backend leaves on this machine: its state entry ends as moved, and the
// doing reminder's stamp goes, so the next eligible prompt says the line is
// blank — the new registration has none, as after a re-open that could not
// carry it.
func (r *run) leftBehind(ctx context.Context, f facts, old *sessionmap.ByPID, now time.Time) {
	r.stateEnd(ctx, f, old, "moved", now)
	if err := os.Remove(doingStampPath(f.stateDir, f.pid)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		r.log.Debug("doing stamp not removed", log.Err(err))
	}
}
