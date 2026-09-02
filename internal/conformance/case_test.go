package conformance

import (
	"strings"
	"testing"
)

func fakeCases() []Case {
	noop := func(*T) {}
	return []Case{
		{ID: "C-01", Rule: "r", Tags: []string{TagCore}, Run: noop},
		{ID: "C-03", Rule: "r", Tags: []string{TagCap("team.create")}, Run: noop},
		{ID: "C-03b", Rule: "r", Tags: []string{TagCap("team.create")}, Run: noop},
		{ID: "C-14", Rule: "r", Tags: []string{TagCore, TagSlow}, Run: noop},
		{ID: "C-41", Rule: "r", Tags: []string{TagCap("message.watch.stdin_commands")}, Run: noop},
	}
}

func ids(sels []selection) string {
	var out []string
	for _, s := range sels {
		id := s.c.ID
		if s.skip != "" {
			id += "(skip)"
		}
		out = append(out, id)
	}
	return strings.Join(out, " ")
}

func TestCaseTagHelpers(t *testing.T) {
	t.Parallel()
	c := Case{ID: "C-41", Tags: []string{TagCore, TagCap("message.watch.stdin_commands"), TagSlow}}
	if !c.HasTag(TagCore) || !c.IsSlow() || c.HasTag("cap:") {
		t.Fatalf("tag helpers: %+v", c)
	}
	if got := c.RequiredCapabilities(); len(got) != 1 || got[0] != "message.watch.stdin_commands" {
		t.Fatalf("RequiredCapabilities: %v", got)
	}
}

func TestSelectCases(t *testing.T) {
	t.Parallel()
	all := []string{"team.create"}
	for name, tc := range map[string]struct {
		opts Options
		caps []string
		want string
	}{
		"default":        {Options{}, all, "C-01 C-03 C-03b C-14(skip) C-41(skip)"},
		"slow":           {Options{Slow: true}, all, "C-01 C-03 C-03b C-14 C-41(skip)"},
		"only lowercase": {Options{Only: []string{"c-03B", "c-01"}}, all, "C-01 C-03b"},
		"skip":           {Options{Skip: []string{"C-01", "c-14"}}, all, "C-03 C-03b C-41(skip)"},
		"tags any-of":    {Options{Tags: []string{"slow", "cap:team.create"}}, all, "C-03 C-03b C-14(skip)"},
		"no caps":        {Options{}, nil, "C-01 C-03(skip) C-03b(skip) C-14(skip) C-41(skip)"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := ids(selectCases(fakeCases(), tc.opts, tc.caps)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSelectCasesSkipReasons(t *testing.T) {
	t.Parallel()
	sels := selectCases(fakeCases(), Options{Only: []string{"C-14", "C-41"}}, nil)
	if len(sels) != 2 || !strings.Contains(sels[0].skip, "--slow") || sels[1].skip != "capability not advertised: message.watch.stdin_commands" {
		t.Fatalf("skip reasons: %+v", sels)
	}
}

func TestValidateIDs(t *testing.T) {
	t.Parallel()
	if err := validateIDs(fakeCases(), Options{Only: []string{"c-01"}, Skip: []string{"C-03B"}}); err != nil {
		t.Fatalf("known ids: %v", err)
	}
	if err := validateIDs(fakeCases(), Options{Only: []string{"C-99"}}); err == nil || !strings.Contains(err.Error(), "C-99") {
		t.Fatalf("--only C-99: err %v, want unknown id", err)
	}
	if err := validateIDs(fakeCases(), Options{Skip: []string{"C-02"}}); err == nil || !strings.Contains(err.Error(), "--skip") {
		t.Fatalf("--skip C-02: err %v, want unknown id", err)
	}
}

func TestValidateCases(t *testing.T) {
	t.Parallel()
	if err := validateCases(fakeCases()); err != nil {
		t.Fatal(err)
	}
	dup := append(fakeCases(), Case{ID: "c-01", Run: func(*T) {}})
	if err := validateCases(dup); err == nil {
		t.Fatal("duplicate id accepted")
	}
	if err := validateCases([]Case{{ID: "C-77"}}); err == nil {
		t.Fatal("case without a body accepted")
	}
}
