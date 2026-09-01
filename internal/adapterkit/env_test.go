package adapterkit_test

import (
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// The resolvers are pure over an os.Environ()-form slice, so these tests
// never call t.Setenv and stay parallel.

func TestConfigDirResolution(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		environ []string
		want    string
		wantErr bool
	}{
		{
			name:    "BRIGADE_CONFIG_DIR wins over everything",
			environ: []string{"HOME=/h", "XDG_CONFIG_HOME=/x", "BRIGADE_CONFIG_DIR=/b"},
			want:    "/b",
		},
		{
			name:    "BRIGADE_CONFIG_DIR is cleaned",
			environ: []string{"BRIGADE_CONFIG_DIR=/b//deep/../c/"},
			want:    "/b/c",
		},
		{
			name:    "relative BRIGADE_CONFIG_DIR is refused, not honoured and not ignored",
			environ: []string{"HOME=/h", "BRIGADE_CONFIG_DIR=rel/path"},
			wantErr: true,
		},
		{
			name:    "absolute XDG_CONFIG_HOME",
			environ: []string{"HOME=/h", "XDG_CONFIG_HOME=/x"},
			want:    "/x/brigade",
		},
		{
			name:    "relative XDG_CONFIG_HOME is ignored (6.2: it could point inside the project)",
			environ: []string{"HOME=/h", "XDG_CONFIG_HOME=rel/cache"},
			want:    "/h/.config/brigade",
		},
		{
			name:    "dot-relative XDG_CONFIG_HOME is ignored",
			environ: []string{"HOME=/h", "XDG_CONFIG_HOME=./cache"},
			want:    "/h/.config/brigade",
		},
		{
			name:    "empty XDG_CONFIG_HOME falls back to HOME",
			environ: []string{"HOME=/h", "XDG_CONFIG_HOME="},
			want:    "/h/.config/brigade",
		},
		{
			name:    "HOME fallback",
			environ: []string{"HOME=/h"},
			want:    "/h/.config/brigade",
		},
		{
			name:    "no HOME at all",
			environ: []string{},
			wantErr: true,
		},
		{
			name:    "relative HOME",
			environ: []string{"HOME=h"},
			wantErr: true,
		},
		{
			name:    "last occurrence of a duplicate wins",
			environ: []string{"BRIGADE_CONFIG_DIR=/first", "BRIGADE_CONFIG_DIR=/second"},
			want:    "/second",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := adapterkit.ConfigDir(tc.environ)
			if tc.wantErr {
				perr := asProtocol(t, err)
				if perr.Code != protocol.CodeConfig {
					t.Fatalf("code = %q, want config", perr.Code)
				}
				if exit := perr.Code.Exit(); exit != 11 {
					t.Fatalf("exit = %d, want 11", exit)
				}
				return
			}
			if err != nil {
				t.Fatalf("ConfigDir: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ConfigDir = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStateDirResolution(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		environ []string
		want    string
		wantErr bool
	}{
		{
			name:    "BRIGADE_STATE_DIR wins",
			environ: []string{"HOME=/h", "XDG_STATE_HOME=/x", "BRIGADE_STATE_DIR=/s"},
			want:    "/s",
		},
		{
			name:    "absolute XDG_STATE_HOME",
			environ: []string{"HOME=/h", "XDG_STATE_HOME=/x"},
			want:    "/x/brigade",
		},
		{
			name:    "relative XDG_STATE_HOME is ignored",
			environ: []string{"HOME=/h", "XDG_STATE_HOME=.state"},
			want:    "/h/.local/state/brigade",
		},
		{
			name:    "HOME fallback is ~/.local/state/brigade",
			environ: []string{"HOME=/h"},
			want:    "/h/.local/state/brigade",
		},
		{
			name:    "relative BRIGADE_STATE_DIR is refused",
			environ: []string{"HOME=/h", "BRIGADE_STATE_DIR=./s"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := adapterkit.StateDir(tc.environ)
			if tc.wantErr {
				perr := asProtocol(t, err)
				if perr.Code != protocol.CodeConfig {
					t.Fatalf("code = %q, want config", perr.Code)
				}
				return
			}
			if err != nil {
				t.Fatalf("StateDir: %v", err)
			}
			if got != tc.want {
				t.Fatalf("StateDir = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProfileNameResolution(t *testing.T) {
	t.Parallel()
	if got := adapterkit.ProfileName([]string{"HOME=/h"}); got != "default" {
		t.Fatalf("ProfileName with no BRIGADE_PROFILE = %q, want default", got)
	}
	if got := adapterkit.ProfileName([]string{"BRIGADE_PROFILE=work"}); got != "work" {
		t.Fatalf("ProfileName = %q, want work", got)
	}
	if got := adapterkit.ProfileName([]string{"BRIGADE_PROFILE="}); got != "default" {
		t.Fatalf("ProfileName with an empty BRIGADE_PROFILE = %q, want default", got)
	}
}

func TestGetenv(t *testing.T) {
	t.Parallel()
	environ := []string{"A=1", "B=", "A=2", "PREFIXA=9"}
	if got := adapterkit.Getenv(environ, "A"); got != "2" {
		t.Fatalf("Getenv(A) = %q, want the last occurrence 2", got)
	}
	if got := adapterkit.Getenv(environ, "B"); got != "" {
		t.Fatalf("Getenv(B) = %q, want empty", got)
	}
	if got := adapterkit.Getenv(environ, "C"); got != "" {
		t.Fatalf("Getenv(C) = %q, want empty", got)
	}
	if got := adapterkit.Getenv(environ, "PREFIX"); got != "" {
		t.Fatalf("Getenv(PREFIX) = %q; a name must match exactly, not as a prefix", got)
	}
}
