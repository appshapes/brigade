package commands

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teammove"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
)

// `team rotate-secret` and `team revoke-member` say which backend they act
// on, and can be pointed at one (work item 341).
//
// A team that moved leaves its administrator's machine joined to it on two
// backends, and a team command goes to the one the checkout's pin names:
// after the move that is the backend the team went to, whatever the team
// file of an older commit names. An administrator who ended the team on the
// backend it left, from the checkout the move had rewritten, rotated the
// join secret of the backend the team lives on and put the members who had
// followed out of it, and nothing the command printed said which backend
// that was. `--team` cannot tell the two apart: they are one team.
//
// So the two verbs that change a team take `--on <url or host>`, the
// backend to act on, resolved as `team move --from` resolves its own; and
// with or without it they say the backend before the adapter is asked, on
// stderr, and in the result, as its `url` member. Everything else is the
// pass-through it was: the adapter's envelope, its stderr and its exit
// status.

// flagOn is the flag by which a team verb names the backend it acts on.
const flagOn = "on"

// takeOn takes `--on <value>` (or `--on=<value>`) out of a verb's
// arguments, which go to the adapter as they are otherwise.
func takeOn(rest []string) (on string, out []string, err error) {
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if !strings.HasPrefix(arg, "-") || name != flagOn {
			out = append(out, arg)
			continue
		}
		if !hasValue {
			if i+1 >= len(rest) {
				return "", nil, usage("invalid arguments: --on needs a value: the url or the host of the backend to act on")
			}
			i++
			value = rest[i]
		}
		if value == "" || on != "" {
			return "", nil, usage("invalid arguments: --on takes one value: the url or the host of the backend to act on")
		}
		on = value
	}
	return on, out, nil
}

// backendName names one backend of a team among the memberships this
// machine holds: its host, and its whole address where that host names
// another backend the machine has joined the same team on (two servers on
// one machine, or behind one host and port under two paths; work item
// 341). It is the one rule for the line that says which backend a team
// command acts on and for the `--from` of a command a line prints for its
// reader to run: there the host alone names more than one backend, the
// line would not say which, and the command was refused as printed
// (namesBackend). Either form is what `--on` and `--from` take.
func backendName(bindings []*adapterkit.Profile, teamRef, url string) string {
	host := hostOf(url)
	for _, other := range bindings {
		if other.TeamRef == teamRef && other.URL != url && hostOf(other.URL) == host {
			return url
		}
	}
	return host
}

// backendNameIn is backendName among the memberships of the store at
// configDir. A store that cannot be listed names the host.
func backendNameIn(configDir, teamRef, url string) string {
	bindings, _, err := listBindings(configDir)
	if err != nil {
		return hostOf(url)
	}
	return backendName(bindings, teamRef, url)
}

// namesBackend reports whether where, a url or a host as a person types
// one, names the backend of a membership: its url, in any spelling that is
// trivially the same, or its host as a line prints it.
func namesBackend(b *adapterkit.Profile, where string) bool {
	return teammove.SameAddress(b.URL, where) || teammove.Host(b.URL) == where
}

// resolveOn finds the membership `--on` names: one of the team --team
// names, matched as every team command matches it (matchesTeam), else of
// the team this checkout is joined to (its pin's, else its team file's),
// else, outside any project, any team's. A profile this machine left names
// a principal and no team, is no membership, and is never one of them
// (listBindings). The value is never echoed.
//
// More than one is refused by what tells them apart. Two backends of one
// team that a host names both, two servers behind one host and port under
// two paths, are told apart by the whole url, and the refusal names each:
// to say "add --team" there, as this did, names the one thing that is
// already given. Memberships of more than one team are told apart by
// --team.
func (inv Invocation) resolveOn(on, teamFlag string) (string, error) {
	configDir, err := inv.storeDir()
	if err != nil {
		return "", err
	}
	bindings, keys, err := listBindings(configDir)
	if err != nil {
		return "", err
	}
	teamRef := ""
	if teamFlag == "" {
		teamRef = checkoutTeam(configDir, inv.mustGetwd())
	}
	var found []int
	for i, b := range bindings {
		switch {
		case teamFlag != "" && !matchesTeam(b, teamFlag):
		case teamRef != "" && b.TeamRef != teamRef:
		case namesBackend(b, on):
			found = append(found, i)
		}
	}
	switch len(found) {
	case 1:
		return keys[found[0]], nil
	case 0:
		return "", &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "--on names no backend this machine has joined this team on; `brigade team list` shows what it has joined",
			Details: map[string]string{"reason": "team_unknown"},
		}
	}
	teams, urls := map[string]bool{}, make([]string, 0, len(found))
	for _, i := range found {
		teams[bindings[i].TeamRef] = true
		urls = append(urls, attrLine(bindings[i].URL))
	}
	slices.Sort(urls)
	if len(teams) > 1 {
		return "", &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "--on names a backend this machine has joined more than one team on; add --team <team_ref>",
			Details: map[string]string{"reason": "team_ambiguous"},
		}
	}
	return "", &protocol.Error{
		Code: protocol.CodeConfig,
		Message: "--on names more than one backend this machine has joined this team on: " + strings.Join(urls, ", ") +
			". Name one by its whole url",
		Details: map[string]string{"reason": "team_ambiguous"},
	}
}

// matchesTeam reports whether --team names a membership's team: by its
// reference or by its name, the one rule of every team command.
func matchesTeam(b *adapterkit.Profile, teamFlag string) bool {
	return b.TeamRef == teamFlag || b.TeamName == teamFlag
}

// checkoutTeam is the reference of the team the directory's checkout is
// joined to: its pin's, else its team file's, "" outside a project or in
// one that names no team.
func checkoutTeam(configDir, cwd string) string {
	path, found := teamfile.Discover(cwd)
	if !found {
		return ""
	}
	if canon, err := teamfile.Canonicalize(filepath.Dir(path)); err == nil {
		if pin, pinned, perr := teamstore.LookupPin(configDir, canon); perr == nil && pinned {
			return pin.TeamRef
		}
	}
	if f, err := teamfile.Parse(path); err == nil {
		return f.TeamRef
	}
	return ""
}

// teamAdminOn runs `team rotate-secret` or `team revoke-member` on the
// backend `--on` names, or, with no `--on`, on the one the pass-through has
// always resolved (--team, the checkout's pin, the session's map). It says
// the backend on stderr before the adapter is asked, and in the result.
func (inv Invocation) teamAdminOn(verb string, raw rawArgs) error {
	on, rest, err := takeOn(raw.Rest)
	if err != nil {
		return err
	}
	raw.Rest = rest
	key := ""
	switch {
	case on != "":
		if key, err = inv.resolveOn(on, raw.Team); err != nil {
			return err
		}
	case raw.Team != "":
		if key, err = inv.resolveTeamKey(raw.Team); err != nil {
			return err
		}
	}
	t, err := inv.terminalTarget(key, true)
	if err != nil {
		return err
	}
	if raw.Adapter != "" {
		ad, aerr := config.ResolveAdapter(config.Options{}, t.configDir, raw.Adapter)
		if aerr != nil {
			return aerr
		}
		t.adapter = ad
		t.client = inv.client(t)
	}
	binding, berr := teamstore.LoadBinding(t.configDir, t.profile)
	if berr != nil || binding.URL == "" {
		// A profile that names no backend (an empty store, the scripted
		// path): there is nothing to name, and the adapter's own answer
		// does the talking.
		return inv.spawnThrough(t, "team", verb, raw.Rest, nil)
	}
	said := "brigade: team " + verb + " acts on team \"" + nameLine(binding.TeamName) + "\" (" + sanitizeID(binding.TeamRef) + ") at " + attrLine(backendNameIn(t.configDir, binding.TeamRef, binding.URL))
	if _, err := io.WriteString(inv.Err, said+"\n"); err != nil {
		return err
	}
	var answered bytes.Buffer
	exit, err := t.client.PassThrough(context.Background(), "team", verb, raw.Rest, inv.In, &answered, inv.Err)
	if err != nil {
		return err
	}
	if _, err := inv.Out.Write(withBackend(answered.Bytes(), binding.URL)); err != nil {
		return err
	}
	if exit != 0 {
		return ExitStatus(exit)
	}
	return nil
}

// withBackend adds the member `url`, the backend a team verb acted on, to
// the result of the adapter's envelope, and changes nothing else of it: not
// a byte of what the adapter wrote before or after. An answer that is not
// one envelope whose result is an object, a failure, and a result that has
// a `url` of its own are returned as they are.
func withBackend(envelope []byte, backend string) []byte {
	var env struct {
		OK     bool           `json:"ok"`
		Result jsontext.Value `json:"result"`
	}
	if json.Unmarshal(bytes.TrimSpace(envelope), &env) != nil || !env.OK || len(env.Result) < 2 || env.Result[0] != '{' {
		return envelope
	}
	var members map[string]jsontext.Value
	if json.Unmarshal(env.Result, &members) != nil {
		return envelope
	}
	if _, has := members["url"]; has {
		return envelope
	}
	// The result's closing brace is the last `}` before the envelope's own:
	// found by reading the envelope's members, not by counting braces.
	dec := jsontext.NewDecoder(bytes.NewReader(envelope))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return envelope
	}
	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		if err != nil {
			return envelope
		}
		if name.String() != "result" {
			if err := dec.SkipValue(); err != nil {
				return envelope
			}
			continue
		}
		if _, err := dec.ReadValue(); err != nil {
			return envelope
		}
		end := int(dec.InputOffset()) - 1
		if end <= 0 || end >= len(envelope) || envelope[end] != '}' {
			return envelope
		}
		text, err := json.Marshal(backend)
		if err != nil {
			return envelope
		}
		member := `"url":` + string(text)
		if len(members) > 0 {
			member = "," + member
		}
		out := make([]byte, 0, len(envelope)+len(member))
		out = append(out, envelope[:end]...)
		out = append(out, member...)
		return append(out, envelope[end:]...)
	}
	return envelope
}
