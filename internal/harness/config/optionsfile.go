package config

import (
	"encoding/json/v2"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// A host whose plugin manifest runs no `userConfig` — Muse Code (its
// diagnostics call the member "unsupported behavior": it installs but
// stays inactive; card 88) and Codex CLI (whose plugins have no option
// mechanism at all; card 107) — gets its options from a file Brigade owns:
// ${configDir}/<host>-options.json, a JSON object whose keys are the option
// names of plugin/.claude-plugin/plugin.json's userConfig and whose values
// are strings, booleans or numbers. The hook reads it once per run and
// turns each known key into the CLAUDE_PLUGIN_OPTION_<KEY> entry
// ParseOptions already understands, so every host shares one parser and
// one set of defaults. The file is found under the config directory the
// environment resolves without the file's own say (such a hook gets HOME
// and no XDG_*, so that is HOME/.config/brigade); `config_dir` inside it
// then relocates the credential store exactly as the Claude option does.
const (
	// MuseOptionsFile and CodexOptionsFile are the files' names under the
	// config directory; OptionsFile picks by host.
	MuseOptionsFile  = "muse-options.json"
	CodexOptionsFile = "codex-options.json"
	GrokOptionsFile  = "grok-options.json"
	// MaxOptionsFileBytes bounds what is read of the file.
	MaxOptionsFileBytes = 64 << 10
)

// OptionsFile is the options file's name for a host that reads one, ""
// for Claude Code (whose options arrive as CLAUDE_PLUGIN_OPTION_*) and for
// a terminal.
func OptionsFile(host Host) string {
	switch host {
	case HostMuse:
		return MuseOptionsFile
	case HostCodex:
		return CodexOptionsFile
	case HostGrok:
		return GrokOptionsFile
	case HostClaude:
		return ""
	}
	return ""
}

// WarnOptionsUnusable is the one line the hook prints when the file at
// path exists but cannot be used; the defaults then apply. Fixed text plus
// the file's own name: the file's content is never echoed.
func WarnOptionsUnusable(path string) string {
	return "Brigade: " + filepath.Base(path) + " is not a JSON object of option names to values; the defaults apply for this session."
}

// optionsFileKeys are the userConfig option names the file may set, each
// mapped to the environment variable ParseOptions reads for it. A key not
// listed is ignored, so a typo costs nothing but its effect.
var optionsFileKeys = map[string]string{
	"config_dir":             OptionConfigDir,
	"adapter_command":        OptionAdapterCommand,
	"team_inbound":           OptionTeamInbound,
	"share_workspace_label":  OptionShareWorkspaceLabel,
	"share_doing":            OptionShareDoing,
	"workspace_label":        OptionWorkspaceLabel,
	"poll_on_prompt":         OptionPollOnPrompt,
	"frame":                  OptionFrame,
	"frame_file":             OptionFrameFile,
	"label":                  OptionLabel,
	"sync":                   OptionSync,
	"message_sound":          OptionMessageSound,
	"message_notification":   OptionMessageNotification,
	"message_interval":       OptionMessageInterval,
	"claude_inbound_setting": OptionClaudeInboundSetting,
}

// OptionsPath is the options file's path for environ: the host's file
// under the config directory adapterkit resolves through Trusted
// (BRIGADE_CONFIG_DIR is stripped inside a session, so HOME/.config/brigade
// in such a hook). "" with no error for a host that reads no file.
func OptionsPath(environ []string) (string, error) {
	name := OptionsFile(HostOf(environ))
	if name == "" {
		return "", nil
	}
	dir, err := adapterkit.ConfigDir(Trusted(environ))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// OptionsEnviron returns environ with one CLAUDE_PLUGIN_OPTION_* entry
// appended per known key of the options file at path, read through
// readFile. A missing file is the defaults with no warning; a file that is
// not a JSON object, or is over MaxOptionsFileBytes, is the defaults with
// WarnOptionsUnusable(path) as the warning. A value that is a string, a
// boolean or a number is rendered as its text; any other value (an object,
// an array, null) is skipped. Appended, not prepended, because
// adapterkit.Getenv takes the LAST occurrence.
func OptionsEnviron(environ []string, path string, readFile func(string) ([]byte, error)) ([]string, string) {
	if HostOf(environ) == HostGrok {
		// Grok has its own options file. Inherited Claude plugin options
		// must not become defaults when that file omits a key or is absent.
		clean := make([]string, 0, len(environ))
		for _, entry := range environ {
			if !strings.HasPrefix(entry, "CLAUDE_PLUGIN_OPTION_") {
				clean = append(clean, entry)
			}
		}
		environ = clean
	}
	data, err := readFile(path)
	if err != nil {
		return environ, ""
	}
	if len(data) > MaxOptionsFileBytes {
		return environ, WarnOptionsUnusable(path)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return environ, WarnOptionsUnusable(path)
	}
	out := append([]string{}, environ...)
	for key, variable := range optionsFileKeys {
		v, ok := raw[key]
		if !ok {
			continue
		}
		text, ok := optionText(v)
		if !ok {
			continue
		}
		out = append(out, variable+"="+text)
	}
	return out, ""
}

// optionText renders a JSON value as an option's text: a string as itself
// (trimmed), a boolean as true/false, a number as its shortest decimal.
func optionText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x), true
	case bool:
		return strconv.FormatBool(x), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	}
	return "", false
}
