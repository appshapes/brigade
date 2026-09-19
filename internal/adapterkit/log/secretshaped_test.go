package log_test

import (
	"testing"

	aklog "github.com/appshapes/brigade/internal/adapterkit/log"
)

// TestSecretShapedIsTheRedactorsPrefixList: the predicate fires on exactly
// the prefix-literal formats Redact masks — a JWT, a join secret, a
// Supabase secret key, glued to other text or not — and on nothing else:
// not on the bare literal without the shape behind it, and not on a Bearer
// header (that rule is a log rule, and a refusal that fired on "fixing
// bearer token parsing" would cost the doing line its feature, plan 5.1).
func TestSecretShapedIsTheRedactorsPrefixList(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"a JWT alone", jwtToken, true},
		{"a JWT glued into a sentence", "token=" + jwtToken + "&x=1", true},
		{"a join secret", "rotating " + brgSecret + " now", true},
		{"a Supabase secret key", "key " + sbSecret, true},
		{"jwt-shaped junk glued in front of a real JWT", "eyJXX.YY." + jwtToken, true},
		{"the eyJ literal without the shape", "the eyJ header prefix", false},
		{"the brg1. literal followed by whitespace", "brg1. and more words", false},
		{"the sb_secret_ literal alone", "sb_secret_ is the prefix", false},
		{"a Bearer header", "Authorization: Bearer " + bearerTok, false},
		{"prose about bearer tokens", "fixing bearer token parsing", false},
		{"empty", "", false},
		{"plain prose", "card 25 part A - the verb, the option, the mode", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := aklog.SecretShaped(tc.in); got != tc.want {
				t.Fatalf("SecretShaped(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
