package watch_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/testutil"
)

// Exercise the real command writer and inspect every byte sent to the
// adapter. Fixtures carry no personal content; even their native ID and
// usage envelope must stay local. The Claude test alongside this one
// continues to cover the unchanged transcript path.
func TestHeartbeatCarriesHostFacts(t *testing.T) {
	t.Parallel()
	const id = "0199aa11-bb22-7333-8444-555566667777"
	const nextID = "0199aa11-bb22-7333-8444-555566667778"
	for _, host := range []string{"muse", "codex"} {
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			fx := newFixture(t, fixtureOptions{host: host})
			if host == "muse" {
				fx.sink = filepath.Join(fx.dirs.Root, "sink.ndjson")
			}
			record := filepath.Join(t.TempDir(), "commands.ndjson")
			fx.useHelper("record=" + record)
			root := filepath.Join(fx.dirs.Home, ".local/share/muse")
			fixture, want := "muse-session.jsonl", "hook-model/42000"
			name := "renamed muse"
			if host == "codex" {
				root = filepath.Join(fx.dirs.Home, "custom-codex")
				fx.extraEnv = []string{"CODEX_HOME=" + root}
				fixture, want, name = "codex-rollout.jsonl", "hook-model/73000", "renamed codex"
			}
			day := filepath.Join(root, "sessions/2026/10/18")
			path := filepath.Join(day, id, "session.jsonl")
			if host == "codex" {
				path = filepath.Join(day, "rollout-2026-10-18T10-00-00-"+id+".jsonl")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			load := func(file string) string {
				b, err := os.ReadFile(filepath.Join("testdata/hostfacts", file))
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			original := load(fixture)
			writeTranscript(t, path, original)
			if host == "codex" {
				writeTranscript(t, filepath.Join(root, "session_index.jsonl"), load("codex-index.jsonl"))
			}
			setMap := func(native, name string) {
				fx.writeMapWith(func(m *sessionmap.ByPID) { m.ClaudeSessionID = native; m.SessionName = name; m.Model = "hook-model" })
			}
			setMap(id, "work")
			deps := fx.deps()
			deps.HeartbeatInterval = 150 * time.Millisecond
			r := fx.start(deps, fx.args()...)
			fx.waitLog("watch ready", nil)
			matches := func(wantName, wantFacts string) bool {
				hbs := recordedHeartbeats(t, record)
				if len(hbs) == 0 {
					return false
				}
				last := hbs[len(hbs)-1]
				return last.SessionName != nil && *last.SessionName == wantName && heartbeatFacts(last) == wantFacts
			}
			testutil.Eventually(t, waitShort, pollEvery, func() bool { return matches(name, want) })
			// A host rename and compaction are visible without a new hook.
			if host == "codex" {
				appendTranscript(t, filepath.Join(root, "session_index.jsonl"), `{"id":"`+id+`","thread_name":"after rename"}`)
				appendTranscript(t, path, `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":12}}}}`)
			} else {
				appendTranscript(t, path, `{"payload_type":"session.name.changed","stream":{"kind":"session","id":"`+id+`"},"payload":{"session_id":"`+id+`","new_name":"after rename"}}`,
					`{"payload_type":"runtime.session","stream":{"kind":"session","id":"`+id+`"},"payload":{"kind":"run","event":{"kind":"model_completed","usage":{"prompt_tokens":12}}}}`)
			}
			testutil.Eventually(t, waitShort, pollEvery, func() bool { return matches("after rename", "hook-model/12") })
			// No usage on a new native thread: never resend the old count or name.
			setMap(nextID, "new thread")
			fx.waitLog("native session id updated from the by-pid map", nil)
			nextName := "new thread"
			if host == "codex" {
				nextName = "other session"
			} // exact second index entry
			testutil.Eventually(t, waitShort, pollEvery, func() bool { return matches(nextName, "hook-model/-") })
			if code := r.stopAndWait(); code != 0 {
				t.Fatalf("exit %d", code)
			}
			fx.assertLogNamesNoPath(path, root, id, nextID)
			b, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{id, nextID, root, "payload", "last_token_usage", "model_completed"} {
				if strings.Contains(string(b), forbidden) {
					t.Errorf("private data sent: %s", forbidden)
				}
			}
			for _, hb := range recordedHeartbeats(t, record) {
				if err := hb.Validate(); err != nil {
					t.Error(err)
				}
			}
		})
	}
}
