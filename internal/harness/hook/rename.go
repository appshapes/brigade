package hook

import (
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/config"
)

// takeRename takes the pending name `brigade rename` left for this
// session (card 174): the file is read under the strict private-file
// rules and removed whatever follows — it is one-shot — and the name is
// returned only when the file names this prompt's conversation (a stamp
// adopted through pid reuse must never name another session), the host
// is Claude Code (the one host whose prompt hook output has a
// sessionTitle field), and the name is not already the title. A missing
// file is the common path and costs one stat.
func (r *run) takeRename(f facts, in input) string {
	if f.host != config.HostClaude {
		return ""
	}
	path := config.RenameStamp(f.stateDir, f.pid)
	data, err := adapterkit.ReadStrict(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.log.Warn("prompt: pending name refused and removed", log.Err(err))
			r.removeRename(path)
		}
		return ""
	}
	r.removeRename(path)
	id, name, ok := strings.Cut(strings.TrimSpace(string(data)), " ")
	if !ok || in.SessionID == "" || id != in.SessionID {
		r.log.Debug("prompt: the pending name names another conversation; dropped")
		return ""
	}
	name = oneLine(name, config.MaxSessionNameChars)
	if name == "" || name == in.SessionTitle {
		return ""
	}
	return name
}

// removeRename removes the pending-name file; a failure is one debug
// line, and the next prompt takes the file again.
func (r *run) removeRename(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		r.log.Debug("prompt: pending name not removed", log.Err(err))
	}
}

// emitPromptJSON ends a prompt that applies a name: the one JSON document
// Claude Code reads, carrying the name as sessionTitle and every line the
// prompt said meanwhile — collected by say while collecting was set — as
// additionalContext, which Claude Code injects exactly as it injects
// plain stdout. Should the document not encode, the lines go out plain
// and the name is dropped: the context must reach the model whatever
// happens to the title.
func (r *run) emitPromptJSON(name string) {
	lines := r.collected
	r.collecting, r.collected = false, nil
	if r.streams.Out == nil {
		return
	}
	b, err := marshalPromptOutput(name, strings.Join(lines, "\n"))
	if err != nil {
		r.log.Warn("prompt: the name could not be encoded; the context lines go out plain", log.Err(err))
		for _, line := range lines {
			_, _ = io.WriteString(r.streams.Out, line+"\n")
		}
		return
	}
	_, _ = io.WriteString(r.streams.Out, string(b)+"\n")
}

// marshalPromptOutput is the UserPromptSubmit hook's JSON output with a
// session title: {"hookSpecificOutput":{"hookEventName":"UserPromptSubmit",
// "sessionTitle":…,"additionalContext":…}}, the context omitted when there
// is none.
func marshalPromptOutput(name, context string) ([]byte, error) {
	type specific struct {
		HookEventName     string `json:"hookEventName"`
		SessionTitle      string `json:"sessionTitle"`
		AdditionalContext string `json:"additionalContext,omitzero"`
	}
	return json.Marshal(struct {
		HookSpecificOutput specific `json:"hookSpecificOutput"`
	}{specific{"UserPromptSubmit", name, context}})
}
