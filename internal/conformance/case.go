package conformance

import (
	"errors"
	"slices"
	"strings"
)

// Tags of plan 9.2. Every case carries TagCore; a case gated on a
// capability carries TagCap(name) and is skipped when describe does not
// advertise it; a case tagged TagSlow runs only with --slow.
const (
	TagCore = "core"
	TagSlow = "slow"

	capTagPrefix = "cap:"
)

// TagCap returns the tag that gates a case on a 4.7 capability, e.g.
// TagCap("team.create") == "cap:team.create".
func TagCap(capability string) string { return capTagPrefix + capability }

// A Case is one conformance case. The cases package builds one per plan
// row; Run selects and executes them in the order given.
type Case struct {
	// ID is the plan id: "C-01" … "C-43", "C-03b", "C-19b", "C-29b".
	ID string
	// Rule is the 9.2 rule column, e.g. "4.5.3 ack idempotent"; it is the
	// text after the duration in the human table.
	Rule string
	// Title is one line saying what the case asserts.
	Title string
	// Tags are TagCore, TagSlow and TagCap values.
	Tags []string
	// Run is the case body. It records outcomes through its T and returns
	// normally; T.Fatalf and T.Skip abort it through a panic the runner
	// recovers, so deferred cleanup still runs.
	Run func(*T)
}

// HasTag reports whether the case carries tag.
func (c Case) HasTag(tag string) bool { return slices.Contains(c.Tags, tag) }

// IsSlow reports whether the case carries TagSlow.
func (c Case) IsSlow() bool { return c.HasTag(TagSlow) }

// RequiredCapabilities returns the capabilities named by the case's cap:
// tags, in tag order.
func (c Case) RequiredCapabilities() []string {
	var caps []string
	for _, tag := range c.Tags {
		if name, ok := strings.CutPrefix(tag, capTagPrefix); ok && name != "" {
			caps = append(caps, name)
		}
	}
	return caps
}

// validateCases checks the case list itself: every case has an id and a
// body, and no id repeats (case-insensitively).
func validateCases(cases []Case) error {
	seen := map[string]bool{}
	for _, c := range cases {
		key := strings.ToUpper(c.ID)
		switch {
		case key == "":
			return errors.New("a case has no id")
		case c.Run == nil:
			return errors.New("case " + c.ID + " has no body")
		case seen[key]:
			return errors.New("case id " + c.ID + " appears twice")
		}
		seen[key] = true
	}
	return nil
}

// validateIDs checks that every id named by --only and --skip is a case;
// an unknown id is a usage error (9.2).
func validateIDs(cases []Case, opts Options) error {
	for _, flagName := range []struct {
		name string
		ids  []string
	}{{"--only", opts.Only}, {"--skip", opts.Skip}} {
		for _, id := range flagName.ids {
			if findCase(cases, id) < 0 {
				return errors.New(flagName.name + ": unknown case id " + id)
			}
		}
	}
	return nil
}

// findCase returns the index of the case with id (case-insensitively), or
// -1.
func findCase(cases []Case, id string) int {
	return slices.IndexFunc(cases, func(c Case) bool { return strings.EqualFold(c.ID, id) })
}

func containsFold(ids []string, id string) bool {
	return slices.ContainsFunc(ids, func(s string) bool { return strings.EqualFold(s, id) })
}

// A selection is one case chosen by the flags, with the reason it will be
// reported SKIP without running when skip is non-empty.
type selection struct {
	c    Case
	skip string
}

// filterCases applies the three selectors that REMOVE a case from a run:
// --only, --skip and --tags. Nothing else does — --slow and an
// unadvertised cap: tag turn a selected case into a reported SKIP, which
// still counts as selected (`--tags slow` without --slow selects C-14) —
// so this alone decides whether a run selected anything, and Run can call
// it before it resolves the adapter.
func filterCases(cases []Case, opts Options) []Case {
	var out []Case
	for _, c := range cases {
		if len(opts.Only) > 0 && !containsFold(opts.Only, c.ID) {
			continue
		}
		if containsFold(opts.Skip, c.ID) {
			continue
		}
		if len(opts.Tags) > 0 && !slices.ContainsFunc(opts.Tags, c.HasTag) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// emptySelection is the usage error for a run whose selectors matched no
// case at all. Such a run must never be reported as a pass: `--tags
// cap:nosuch` once selected nothing and printed "0 passed, 0 failed, 0
// skipped" with exit 0, which reads exactly like a clean run. The message
// names the selectors responsible so the caller can see which one emptied
// the list.
func emptySelection(opts Options) error {
	var parts []string
	for _, sel := range []struct {
		name  string
		items []string
	}{{"--tags", opts.Tags}, {"--only", opts.Only}, {"--skip", opts.Skip}} {
		if len(sel.items) > 0 {
			parts = append(parts, sel.name+" "+strings.Join(sel.items, ","))
		}
	}
	if len(parts) == 0 {
		return errors.New("no cases to run: the case list is empty")
	}
	return errors.New("no case is selected by " + strings.Join(parts, " with ") +
		"; a run that selects nothing is not a pass")
}

// decideSkips decides, per filtered case, whether it runs or is reported
// SKIP without running: a slow case without --slow, or a cap: tag the
// adapter does not advertise. capabilities is the describe list.
func decideSkips(cases []Case, opts Options, capabilities []string) []selection {
	var out []selection
	for _, c := range cases {
		sel := selection{c: c}
		if c.IsSlow() && !opts.Slow {
			sel.skip = "slow case; run with --slow"
		}
		for _, capability := range c.RequiredCapabilities() {
			if sel.skip == "" && !slices.Contains(capabilities, capability) {
				sel.skip = "capability not advertised: " + capability
			}
		}
		out = append(out, sel)
	}
	return out
}

// selectCases is the whole selection: the filter, then the skip decision.
func selectCases(cases []Case, opts Options, capabilities []string) []selection {
	return decideSkips(filterCases(cases, opts), opts, capabilities)
}

// shuffleCases permutes cases in place with the seeded Fisher-Yates of
// splitmix64 (--shuffle). The generator is written out rather than taken
// from math/rand so that a seed names the same permutation on every Go
// release and on every platform: a run reported as "shuffled with seed
// 8123" must be reproducible from the seed alone, months later. Seed 0 is
// never shuffled; Run does not call this then.
func shuffleCases(cases []Case, seed int64) {
	state := uint64(seed) //nolint:gosec // a seed is a bit pattern, not a magnitude: the reinterpretation is the point
	next := func() uint64 {
		state += 0x9E3779B97F4A7C15
		z := state
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		return z ^ (z >> 31)
	}
	for i := len(cases) - 1; i > 0; i-- {
		j := int(next() % uint64(i+1)) //nolint:gosec // the remainder is < i+1 <= len(cases), an int by construction
		cases[i], cases[j] = cases[j], cases[i]
	}
}
