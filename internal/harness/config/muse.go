package config

import (
	"encoding/json/v2"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// Muse Code's plugin manifest has no working `userConfig` (its diagnostics
// call the member "unsupported behavior": it installs but stays inactive),
// so a Muse session's options come from a file Brigade owns (card 88):
// ${configDir}/muse-options.json, a JSON object whose keys are the option
// names of plugin/.claude-plugin/plugin.json's userConfig and whose values
// are strings, booleans or numbers. The hook reads it once per run and
// turns each known key into the CLAUDE_PLUGIN_OPTION_<KEY> entry
// ParseOptions already understands, so the two hosts share one parser and
// one set of defaults. The file is found under the config directory the
// environment resolves without the file's own say (a Muse hook gets HOME
// and no XDG_*, so that is HOME/.config/brigade); `config_dir` inside it
// then relocates the credential store exactly as the Claude option does.
const (
	// MuseOptionsFile is the file's name under the config directory.
	MuseOptionsFile = "muse-options.json"
	// MaxMuseOptionsBytes bounds what is read of it.
	MaxMuseOptionsBytes = 64 << 10
	// WarnMuseOptionsUnusable is the one line the hook prints when the file
	// exists but cannot be used; the defaults then apply. Fixed text: the
	// file's content is never echoed.
	WarnMuseOptionsUnusable = "Brigade: muse-options.json is not a JSON object of option names to values; the defaults apply for this session."
)

// museOptionKeys are the userConfig option names the file may set, each
// mapped to the environment variable ParseOptions reads for it. A key not
// listed is ignored, so a typo costs nothing but its effect.
var museOptionKeys = map[string]string{
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

// MuseOptionsPath is the options file's path for environ: the config
// directory adapterkit resolves through Trusted (BRIGADE_CONFIG_DIR is
// stripped inside a session, so HOME/.config/brigade in a Muse hook).
func MuseOptionsPath(environ []string) (string, error) {
	dir, err := adapterkit.ConfigDir(Trusted(environ))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, MuseOptionsFile), nil
}

// MuseOptionsEnviron returns environ with one CLAUDE_PLUGIN_OPTION_* entry
// appended per known key of the options file at path, read through
// readFile. A missing file is the defaults with no warning; a file that is
// not a JSON object, or is over MaxMuseOptionsBytes, is the defaults with
// WarnMuseOptionsUnusable as the warning. A value that is a string, a
// boolean or a number is rendered as its text; any other value (an object,
// an array, null) is skipped. Appended, not prepended, because
// adapterkit.Getenv takes the LAST occurrence.
func MuseOptionsEnviron(environ []string, path string, readFile func(string) ([]byte, error)) ([]string, string) {
	data, err := readFile(path)
	if err != nil {
		return environ, ""
	}
	if len(data) > MaxMuseOptionsBytes {
		return environ, WarnMuseOptionsUnusable
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return environ, WarnMuseOptionsUnusable
	}
	out := append([]string{}, environ...)
	for key, variable := range museOptionKeys {
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
