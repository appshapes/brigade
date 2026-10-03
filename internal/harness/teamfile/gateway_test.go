package teamfile_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/teamfile"
)

// withGateway is validDoc with a `gateway` member of the given raw JSON.
func withGateway(member string) string {
	return strings.TrimSuffix(validDoc, "}") + `,"gateway":` + member + `}`
}

// TestGatewayRules runs every rule of the `gateway` member (card 48) both
// ways. A rejected member is NEVER a refusal: the file parses, Gateway is
// nil, the token says why, and the team the file names is untouched.
func TestGatewayRules(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", teamfile.MaxGatewayEmailBytes-len("@x.io")) + "@x.io"
	cases := []struct {
		name, member string
		reason       string // "" means usable
		email        string // want, when usable
		ignored      []string
	}{
		{"the installer's shape", `{"email":"89219b45@seluusa.resend.app"}`, "", "89219b45@seluusa.resend.app", nil},
		{"a plus tag and a subdomain", `{"email":"brigade+team@mail.example.co.uk"}`, "", "brigade+team@mail.example.co.uk", nil},
		{"exactly the byte cap", `{"email":"` + long + `"}`, "", long, nil},
		{"an unknown inner member is ignored and named", `{"email":"a@b.io","provider":"resend"}`, "", "a@b.io", []string{"gateway.provider"}},
		{"not an object", `"a@b.io"`, teamfile.GatewayNotObject, "", nil},
		{"an array", `["a@b.io"]`, teamfile.GatewayNotObject, "", nil},
		{"no email", `{}`, teamfile.GatewayEmailMissing, "", nil},
		{"email is not a string", `{"email":1}`, teamfile.GatewayEmailInvalid, "", nil},
		{"no at sign", `{"email":"nobody"}`, teamfile.GatewayEmailInvalid, "", nil},
		{"two at signs", `{"email":"a@b@c.io"}`, teamfile.GatewayEmailInvalid, "", nil},
		{"no dot in the domain", `{"email":"a@localhost"}`, teamfile.GatewayEmailInvalid, "", nil},
		{"a display-name form", `{"email":"Brigade <a@b.io>"}`, teamfile.GatewayEmailInvalid, "", nil},
		{"whitespace inside", `{"email":"a b@c.io"}`, teamfile.GatewayEmailInvalid, "", nil},
		{"a format character inside", `{"email":"a‮@c.io"}`, teamfile.GatewayEmailInvalid, "", nil},
		{"over the byte cap", `{"email":"` + strings.Repeat("a", teamfile.MaxGatewayEmailBytes) + `@x.io"}`, teamfile.GatewayEmailTooLong, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f, err := teamfile.Parse(write(t, withGateway(c.member)))
			if err != nil {
				t.Fatalf("a gateway member must never refuse the file: %v", err)
			}
			if f.TeamRef != "t_4f9c" {
				t.Fatalf("the team the file names changed: %+v", f)
			}
			if f.Gateway != nil && f.GatewayUnusable != "" {
				t.Fatalf("both a usable gateway and a reason: %+v", f)
			}
			if f.GatewayUnusable != c.reason {
				t.Fatalf("reason = %q, want %q", f.GatewayUnusable, c.reason)
			}
			if c.reason == "" && (f.Gateway == nil || f.Gateway.Email != c.email) {
				t.Fatalf("gateway = %+v, want email %q", f.Gateway, c.email)
			}
			if c.reason != "" && f.Gateway != nil {
				t.Fatalf("an unusable member yielded a config: %+v", f.Gateway)
			}
			if !slices.Equal(f.Ignored, c.ignored) && !(len(f.Ignored) == 0 && len(c.ignored) == 0) {
				t.Fatalf("Ignored = %q, want %q", f.Ignored, c.ignored)
			}
		})
	}
}

func TestGatewayReasonsListIsClosed(t *testing.T) {
	t.Parallel()
	want := []string{teamfile.GatewayNotObject, teamfile.GatewayEmailMissing, teamfile.GatewayEmailInvalid, teamfile.GatewayEmailTooLong}
	if !slices.Equal(teamfile.GatewayReasons(), want) {
		t.Fatalf("GatewayReasons() = %q", teamfile.GatewayReasons())
	}
}

func TestCarriedGateway(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		doc           string // "" means no file at all
		worldWritable bool
		want          *teamfile.GatewayConfig
		cause         string
	}{
		"no file":                {"", false, nil, ""},
		"a file without gateway": {validDoc, false, nil, ""},
		"a usable member": {
			withGateway(`{"email":"a@b.io","future":true}`), false, &teamfile.GatewayConfig{Email: "a@b.io"}, "",
		},
		"a refused file with no gateway": {"not json", false, nil, ""},
		"an unusable member":             {withGateway(`{"email":"nope"}`), false, nil, teamfile.GatewayEmailInvalid},
		"a refused file that declares gateway": {
			strings.Replace(withGateway(`{"email":"a@b.io"}`), `"version":1`, `"version":2`, 1), false, nil, teamfile.ReasonUnsupportedVersion,
		},
		"a file read refuses unread": {withGateway(`{"email":"a@b.io"}`), true, nil, teamfile.ReasonWorldWritable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), teamfile.FileName)
			if c.doc != "" {
				p = write(t, c.doc)
			}
			if c.worldWritable {
				//nolint:gosec // G302: the world-writable mode is the precondition under test
				if err := os.Chmod(p, 0o646); err != nil {
					t.Fatal(err)
				}
			}
			got, cause := teamfile.CarriedGateway(p)
			if !reflect.DeepEqual(got, c.want) || cause != c.cause {
				t.Fatalf("= %+v, %q; want %+v, %q", got, cause, c.want, c.cause)
			}
		})
	}
}
