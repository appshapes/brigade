package teamfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

const (
	repointURL = "https://brigade.example.com:8743"
	repointPin = "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
)

// TestToServerKeepsEverythingElse: the file a move rewrites names the
// server, its pin when there is one and no key, and every other byte of it
// is the byte it was: the order of its members, the members this version
// does not define, the white space a person gave it.
func TestToServerKeepsEverythingElse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, in, pin, want string
	}{
		{
			name: "the one line team create writes",
			in:   `{"adapter":"supabase","publishable_key":"sb_publishable_x","team_name":"ops","team_ref":"t_9","url":"https://abc.supabase.co","version":1}` + "\n",
			pin:  repointPin,
			want: `{"adapter":"rest","team_name":"ops","team_ref":"t_9","url":"` + repointURL + `","certificate":"` + repointPin + `","version":1}` + "\n",
		},
		{
			name: "no pin",
			in:   `{"adapter":"supabase","publishable_key":"sb_publishable_x","team_ref":"t_9","url":"https://abc.supabase.co","version":1}`,
			want: `{"adapter":"rest","team_ref":"t_9","url":"` + repointURL + `","version":1}`,
		},
		{
			name: "a file a person indented, with members of its own",
			in: "{\n" +
				"  \"version\": 1,\n" +
				"  \"adapter\": \"supabase\",\n" +
				"  \"url\": \"https://abc.supabase.co\",\n" +
				"  \"publishable_key\": \"sb_publishable_x\",\n" +
				"  \"team_ref\": \"t_9\",\n" +
				"  \"team_name\": \"ops\",\n" +
				"  \"sync\": { \"folders\": [\".brigade\"] },\n" +
				"  \"gateway\": {\"email\": \"ops@example.com\"},\n" +
				"  \"later\": [1,  2]\n" +
				"}\n",
			pin: repointPin,
			want: "{\n" +
				"  \"version\": 1,\n" +
				"  \"adapter\": \"rest\",\n" +
				"  \"url\": \"" + repointURL + "\",\n" +
				"  \"certificate\": \"" + repointPin + "\",\n" +
				"  \"team_ref\": \"t_9\",\n" +
				"  \"team_name\": \"ops\",\n" +
				"  \"sync\": { \"folders\": [\".brigade\"] },\n" +
				"  \"gateway\": {\"email\": \"ops@example.com\"},\n" +
				"  \"later\": [1,  2]\n" +
				"}\n",
		},
		{
			name: "the key is the first member",
			in:   "{\t\"publishable_key\": \"sb_publishable_x\",\t\"adapter\": \"supabase\",\t\"url\": \"https://abc.supabase.co\",\t\"team_ref\": \"t_9\",\t\"version\": 1 }",
			pin:  repointPin,
			want: "{\t\"adapter\": \"rest\",\t\"url\": \"" + repointURL + "\",\t\"certificate\": \"" + repointPin + "\",\t\"team_ref\": \"t_9\",\t\"version\": 1 }",
		},
		{
			name: "the key is the last member, and url the first",
			in:   `{"url":"http://127.0.0.1:1", "adapter":"fs", "team_ref":"t_9", "version":1, "publishable_key":"placeholder"}`,
			pin:  repointPin,
			want: `{"url":"` + repointURL + `", "certificate":"` + repointPin + `", "adapter":"rest", "team_ref":"t_9", "version":1}`,
		},
		{
			name: "a pin the file had is replaced where it stood",
			in:   `{"version":1,"certificate":"sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824","adapter":"rest","url":"https://old.example.com","team_ref":"t_9"}`,
			pin:  repointPin,
			want: `{"version":1,"certificate":"` + repointPin + `","adapter":"rest","url":"` + repointURL + `","team_ref":"t_9"}`,
		},
		{
			name: "line ends as Windows writes them, and no line end after the last brace",
			in:   "{\r\n\t\"version\": 1,\r\n\t\"adapter\": \"supabase\",\r\n\t\"url\": \"https://abc.supabase.co\",\r\n\t\"publishable_key\": \"sb_publishable_x\",\r\n\t\"team_ref\": \"t_9\"\r\n}",
			pin:  repointPin,
			want: "{\r\n\t\"version\": 1,\r\n\t\"adapter\": \"rest\",\r\n\t\"url\": \"" + repointURL + "\",\r\n\t\"certificate\": \"" + repointPin + "\",\r\n\t\"team_ref\": \"t_9\"\r\n}",
		},
		{
			name: "white space before the first brace and after the last",
			in:   "  \n{\"version\":1,\"adapter\":\"supabase\",\"url\":\"https://abc.supabase.co\",\"team_ref\":\"t_9\" ,\n \"publishable_key\":\"sb_publishable_x\"\n}  \n\n\t ",
			pin:  repointPin,
			want: "  \n{\"version\":1,\"adapter\":\"rest\",\"url\":\"" + repointURL + "\",\"certificate\":\"" + repointPin + "\",\"team_ref\":\"t_9\"\n}  \n\n\t ",
		},
		{
			name: "member names spelled with escapes, and members of the same names inside another",
			in:   `{"version":1,"\u0061dapter":"supabase","\u0075rl":"https://abc.supabase.co","publishable_ke\u0079":"k","team_ref":"t_9","sync":{"url":"x","adapter":"y","publishable_key":"z","certificate":"w"},"t\u00e9am":"d\u00e9vs \ud83d\ude00"}`,
			pin:  repointPin,
			want: `{"version":1,"\u0061dapter":"rest","\u0075rl":"` + repointURL + `","certificate":"` + repointPin + `","team_ref":"t_9","sync":{"url":"x","adapter":"y","publishable_key":"z","certificate":"w"},"t\u00e9am":"d\u00e9vs \ud83d\ude00"}`,
		},
		{
			name: "a pin the file had goes when the server has none, with the key before it",
			in:   `{"publishable_key":"k","certificate":"sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824","version":1,"adapter":"other","url":"https://old.example.com","team_ref":"t_9"}`,
			want: `{"version":1,"adapter":"rest","url":"` + repointURL + `","team_ref":"t_9"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := toServer([]byte(tc.in), repointURL, tc.pin)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("the rewritten file:\n%s\nwant:\n%s", got, tc.want)
			}
			f, err := parseBytes(FileName, got)
			if err != nil || f.Adapter != RestAdapter || f.URL != repointURL || f.Certificate != tc.pin || f.PublishableKey != "" || f.TeamRef != "t_9" {
				t.Fatalf("the rewritten file parses to %+v, %v", f, err)
			}
			// A second move to the same server changes nothing.
			again, err := toServer(got, repointURL, tc.pin)
			if err != nil || string(again) != string(got) {
				t.Fatalf("rewriting the rewritten file: %v\n%s", err, again)
			}
		})
	}
}

// TestToServerRefusesWhatItCannotRewrite: a file that does not parse is
// refused with the parser's own answer, and a destination the result could
// not name is refused before anything is returned.
func TestToServerRefusesWhatItCannotRewrite(t *testing.T) {
	t.Parallel()
	valid := `{"adapter":"supabase","publishable_key":"k","team_ref":"t_9","url":"https://abc.supabase.co","version":1}`
	for name, tc := range map[string]struct{ in, url, pin string }{
		"not a team file":            {`[1]`, repointURL, ""},
		"a file with no team":        {`{"adapter":"supabase","publishable_key":"k","url":"https://abc.supabase.co","version":1}`, repointURL, ""},
		"a server over plain http":   {valid, "http://brigade.example.com", ""},
		"a pin that is not one":      {valid, repointURL, "sha256:abc"},
		"a pin beside a loopback":    {valid, "http://127.0.0.1:8743", repointPin},
		"a url that is not one":      {valid, "not a url", ""},
		"a destination with no url":  {valid, "", ""},
		"a secret where the url is":  {valid, "https://brg1.t_9.abababababababab", ""},
		"a file that carries a join": {strings.Replace(valid, `"k"`, `"brg1.t_9.abababababababab"`, 1), repointURL, ""},
		"a byte-order mark":          {"\xef\xbb\xbf" + valid, repointURL, ""},
		"a member named twice":       {strings.Replace(valid, `"version":1`, `"url":"https://abc.supabase.co","version":1`, 1), repointURL, ""},
		"a key named twice":          {strings.Replace(valid, `"version":1`, `"publishable_key":"k","version":1`, 1), repointURL, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := toServer([]byte(tc.in), tc.url, tc.pin)
			if err == nil || got != nil {
				t.Fatalf("toServer answered %q, %v", got, err)
			}
			if strings.Contains(err.Error(), "abababab") {
				t.Fatalf("the refusal carries the file's text: %v", err)
			}
		})
	}
	if _, err := toServer([]byte(valid), "http://brigade.example.com", ""); !errors.Is(err, errRepoint) {
		t.Fatalf("a destination the file may not name: %v, want errRepoint", err)
	}
}

// TestToServerNeverAnswersAFileTooLargeToRead: a file that is as large as a
// team file can be and still read grows when its url gets longer and a pin
// is added. The result would be refused by Parse, and with it by every
// session of the checkout, so ToServer refuses it as too large and the
// caller writes nothing. One byte of room is enough room.
func TestToServerNeverAnswersAFileTooLargeToRead(t *testing.T) {
	t.Parallel()
	doc := `{"adapter":"supabase","publishable_key":"k","team_ref":"t_9","url":"https://a.co","version":1}`
	moved := `{"adapter":"rest","team_ref":"t_9","url":"` + repointURL + `","certificate":"` + repointPin + `","version":1}`
	write := func(t *testing.T, text string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), FileName)
		//nolint:gosec // G306: a committed team file is 0644
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(path); err != nil {
			t.Fatalf("the file before the move does not parse: %v", err)
		}
		return path
	}
	t.Run("a file at the cap", func(t *testing.T) {
		t.Parallel()
		path := write(t, doc+strings.Repeat(" ", MaxBytes-len(doc)))
		got, err := ToServer(path, repointURL, repointPin)
		var perr *protocol.Error
		if got != nil || !errors.As(err, &perr) || perr.Code != protocol.CodeConfig || perr.Details["reason"] != ReasonTooLarge {
			t.Fatalf("ToServer answered %d bytes and %v, want the too_large refusal", len(got), err)
		}
	})
	t.Run("a file with exactly the room it needs", func(t *testing.T) {
		t.Parallel()
		path := write(t, doc+strings.Repeat(" ", MaxBytes-len(moved)))
		got, err := ToServer(path, repointURL, repointPin)
		if err != nil || len(got) != MaxBytes {
			t.Fatalf("ToServer answered %d bytes and %v, want a file of %d", len(got), err, MaxBytes)
		}
		//nolint:gosec // G306: a committed team file is 0644
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		if f, err := Parse(path); err != nil || f.Adapter != RestAdapter || f.URL != repointURL || f.Certificate != repointPin {
			t.Fatalf("the file that was written parses to %+v, %v", f, err)
		}
	})
	t.Run("one byte less room", func(t *testing.T) {
		t.Parallel()
		path := write(t, doc+strings.Repeat(" ", MaxBytes-len(moved)+1))
		if got, err := ToServer(path, repointURL, repointPin); got != nil || err == nil {
			t.Fatalf("ToServer answered %d bytes and %v, want a refusal", len(got), err)
		}
	})
}
