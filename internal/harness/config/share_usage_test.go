package config_test

import (
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
)

// TestShareUsageIsOffByDefault is work item 348 (the owner's ruling of
// 2026-11-24): `share_usage` is off unless the member turns it on, and a
// value that is no boolean is refused.
func TestShareUsageIsOffByDefault(t *testing.T) {
	t.Parallel()
	home := []string{"HOME=" + t.TempDir()}
	o, err := config.ParseOptions(home)
	if err != nil || o.ShareUsage {
		t.Fatalf("default: %v %v", o.ShareUsage, err)
	}
	for raw, want := range map[string]bool{"true": true, "1": true, "false": false, "no": false} {
		o, err := config.ParseOptions(append([]string{config.OptionShareUsage + "=" + raw}, home...))
		if err != nil || o.ShareUsage != want {
			t.Fatalf("%q: %v %v", raw, o.ShareUsage, err)
		}
	}
	if _, err := config.ParseOptions(append([]string{config.OptionShareUsage + "=maybe"}, home...)); err == nil {
		t.Fatal("share_usage=maybe was taken")
	}
}
