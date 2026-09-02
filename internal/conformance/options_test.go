package conformance

import (
	"errors"
	"flag"
	"strings"
	"testing"
	"time"
)

func TestParseArgsFullCommandLine(t *testing.T) {
	t.Parallel()
	o, err := ParseArgs([]string{
		"--adapter", "bin/x", "--env", "A=1", "--env", "B=2", "--shared-env", "BRIGADE_FS_ROOT",
		"--setup", "setup.sh --x", "--rebind", "rebind.sh", "--tags", "core, cap:team.create,slow",
		"--only", "C-20,c-21", "--skip", "C-14", "--slow", "--shuffle", "8123", "--timeout", "3s", "--keep-temp", "--json", "-v",
		"--", "adapter", "supabase",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Options{
		Adapter: "bin/x", Env: []string{"A=1", "B=2"}, SharedEnv: "BRIGADE_FS_ROOT", Setup: "setup.sh --x",
		Rebind: "rebind.sh", Tags: []string{"core", "cap:team.create", "slow"}, Only: []string{"C-20", "c-21"},
		Skip: []string{"C-14"}, Slow: true, Shuffle: 8123, Timeout: 3 * time.Second, KeepTemp: true, JSON: true, Verbose: true,
		FixedArgs: []string{"adapter", "supabase"},
	}
	if strings.Join(o.Env, ",") != strings.Join(want.Env, ",") || strings.Join(o.Tags, ",") != strings.Join(want.Tags, ",") ||
		strings.Join(o.Only, ",") != strings.Join(want.Only, ",") || strings.Join(o.Skip, ",") != strings.Join(want.Skip, ",") ||
		strings.Join(o.FixedArgs, ",") != strings.Join(want.FixedArgs, ",") {
		t.Fatalf("lists: got %+v, want %+v", o, want)
	}
	if o.Adapter != want.Adapter || o.SharedEnv != want.SharedEnv || o.Setup != want.Setup || o.Rebind != want.Rebind ||
		o.Slow != want.Slow || o.Shuffle != want.Shuffle || o.Timeout != want.Timeout || o.KeepTemp != want.KeepTemp ||
		o.JSON != want.JSON || o.Verbose != want.Verbose {
		t.Fatalf("scalars: got %+v, want %+v", o, want)
	}
}

func TestParseArgsDefaults(t *testing.T) {
	t.Parallel()
	o, err := ParseArgs([]string{"--adapter", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Timeout != DefaultTimeout || o.Slow || o.Shuffle != 0 || o.JSON || o.Verbose || o.KeepTemp || len(o.FixedArgs) != 0 {
		t.Fatalf("defaults: %+v", o)
	}
}

func TestParseArgsUsageErrors(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"no adapter":          {"--json"},
		"unknown flag":        {"--adapter", "x", "--frobnicate"},
		"bad timeout":         {"--adapter", "x", "--timeout", "soon"},
		"zero timeout":        {"--adapter", "x", "--timeout", "0s"},
		"env without =":       {"--adapter", "x", "--env", "NOVALUE"},
		"shared-env with =":   {"--adapter", "x", "--shared-env", "A=b"},
		"non-numeric shuffle": {"--adapter", "x", "--shuffle", "please"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseArgs(args); err == nil || errors.Is(err, flag.ErrHelp) {
				t.Fatalf("ParseArgs(%v): err %v, want a usage error", args, err)
			}
		})
	}
}

func TestParseArgsHelp(t *testing.T) {
	t.Parallel()
	if _, err := ParseArgs([]string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("-h: err %v, want flag.ErrHelp", err)
	}
	var b strings.Builder
	Usage(&b)
	for _, flagName := range []string{"-adapter", "-env", "-shared-env", "-setup", "-rebind", "-tags", "-only", "-skip", "-slow", "-shuffle", "-timeout", "-keep-temp", "-json", "-v"} {
		if !strings.Contains(b.String(), "\n  "+flagName+" ") && !strings.Contains(b.String(), "\n  "+flagName+"\t") &&
			!strings.Contains(b.String(), "\n  "+flagName+"\n") {
			t.Errorf("usage text lacks %s:\n%s", flagName, b.String())
		}
	}
	if !strings.Contains(b.String(), "3 launcher error") {
		t.Errorf("usage text lacks the exit legend")
	}
}
