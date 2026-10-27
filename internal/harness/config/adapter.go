package config

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// BundledAdapterName is the one adapter name that needs no registry
// entry: it always means this executable's `adapter supabase` (D26, D36).
// A registry entry of that name is ignored — the developer override for a
// local build is the dev-binary pointer (D35), not a registry rebind.
const BundledAdapterName = "supabase"

// The two harness-owned files of D36 (3.2).
const (
	// SidecarFileName is the per-profile default adapter, one line, in
	// ${configDir}/teams/<profile>/.
	SidecarFileName = "adapter"
	// RegistryFileName maps adapter names to commands, in ${configDir}.
	RegistryFileName = "adapters.json"
)

// The Adapter.Source values.
const (
	SourceOption  = "option"
	SourceProfile = "profile"
	SourceBundled = "bundled"
	// SourceMap: rebuilt from the by-pid map's adapter_command
	// (AdapterFromArgv).
	SourceMap = "map"
	// SourceEnv: decoded from the watcher's hook-built environment
	// (DecodeAdapter).
	SourceEnv = "env"
)

// The details.reason values of adapter resolution failures (all `config`).
const (
	// ReasonAdapterRelative: a path form that is not absolute.
	ReasonAdapterRelative = "adapter_relative"
	// ReasonAdapterUnregistered: a name with no adapters.json entry (or no
	// adapters.json at all).
	ReasonAdapterUnregistered = "adapter_unregistered"
	// ReasonAdapterMalformed: none of the three forms, an empty array, an
	// array with a non-string or empty element, an empty value.
	ReasonAdapterMalformed = "adapter_malformed"
	// ReasonRegistryMalformed: adapters.json is not an object of
	// string-or-array values.
	ReasonRegistryMalformed = "registry_malformed"
	// ReasonRegistryUnreadable: an I/O failure
	// other than "missing" or a mode refusal (which ReadStrict reports as
	// insecure_mode).
	ReasonRegistryUnreadable = "registry_unreadable"
)

// MaxAdapterNameLen caps a registered adapter name.
const MaxAdapterNameLen = 64

// An Adapter is a resolved adapter selection (D36). Argv is the argv
// PREFIX prepended verbatim to every invocation (`argv + --profile p +
// group verb + flags`); it is empty exactly when Bundled, which means
// "this executable, `adapter supabase`", resolved by the adapterclient
// through os.Executable() at spawn time and never stored as a path (the
// hook, the Bash tool and the watcher may run different cached binaries
// only in a development setup, and no map may pin one). Source says which
// step chose it, for `whoami` and `profile status`.
type Adapter struct {
	Argv    []string
	Bundled bool
	Source  string
}

// Encode renders the adapter as the JSON array text the hook records in
// the by-pid map and passes to the watcher: "[]" for the bundled adapter.
func (a Adapter) Encode() (string, error) {
	if a.Bundled || len(a.Argv) == 0 {
		return "[]", nil
	}
	data, err := json.Marshal(a.Argv)
	if err != nil {
		return "", &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "adapter command could not be encoded",
			Details: map[string]string{"reason": ReasonAdapterMalformed, "source": a.Source},
		}
	}
	return string(data), nil
}

// AdapterFromArgv rebuilds an Adapter from the by-pid map's
// adapter_command (Source map): an empty argv is the bundled adapter. The
// argv must pass sessionmap.CheckAdapterCommand, which ReadByPID already
// guarantees; a failure here is `config`.
func AdapterFromArgv(argv []string) (Adapter, error) {
	if err := sessionmap.CheckAdapterCommand(argv); err != nil {
		reason := ReasonAdapterMalformed
		if len(argv) > 0 && argv[0] != "" && !filepath.IsAbs(argv[0]) {
			reason = ReasonAdapterRelative
		}
		return Adapter{}, errAdapter(reason, SourceMap, msgMalformedResolved)
	}
	if len(argv) == 0 {
		return Adapter{Bundled: true, Source: SourceMap}, nil
	}
	return Adapter{Argv: append([]string(nil), argv...), Source: SourceMap}, nil
}

// DecodeAdapter parses the RESOLVED adapter command the hook put in the
// watcher's environment (Source env): "" or "[]" is the bundled adapter, a
// JSON array of strings whose first element is an absolute path, or an
// absolute path. A registered NAME is refused here: resolution through
// adapters.json happened in the hook, and the watcher must not consult a
// file the hook did not.
func DecodeAdapter(s string) (Adapter, error) {
	v := strings.TrimSpace(s)
	switch {
	case v == "" || v == "[]":
		return Adapter{Bundled: true, Source: SourceEnv}, nil
	case v[0] == '[':
		argv, err := decodeArgv([]byte(v))
		if err != nil {
			return Adapter{}, errAdapter(ReasonAdapterMalformed, SourceEnv, msgMalformedResolved)
		}
		if len(argv) == 0 {
			return Adapter{Bundled: true, Source: SourceEnv}, nil
		}
		if !filepath.IsAbs(argv[0]) {
			return Adapter{}, errAdapter(ReasonAdapterRelative, SourceEnv, msgRelative)
		}
		return Adapter{Argv: argv, Source: SourceEnv}, nil
	case filepath.IsAbs(v) && !hasSpace(v):
		return Adapter{Argv: []string{filepath.Clean(v)}, Source: SourceEnv}, nil
	default:
		return Adapter{}, errAdapter(ReasonAdapterMalformed, SourceEnv, msgMalformedResolved)
	}
}

// RegistryPath is ${configDir}/adapters.json.
func RegistryPath(configDir string) string {
	return filepath.Join(configDir, RegistryFileName)
}

// CheckAdapterName validates a registry name: 1–64 characters from
// [A-Za-z0-9_-]. The value is not echoed.
func CheckAdapterName(name string) error {
	if name == "" || len(name) > MaxAdapterNameLen {
		return errors.New("adapter name must be 1-64 characters from letters, digits, dash and underscore")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return errors.New("adapter name must be 1-64 characters from letters, digits, dash and underscore")
		}
	}
	return nil
}

// ResolveAdapter resolves the adapter for a team (P7-6): (1) the
// adapter_command option — a per-session override, never sourced from
// any repository file; (2) the adapter NAME the team's binding or the
// project's team file carries, resolved strictly user-side through
// adapters.json; (3) the bundled adapter, which the name "supabase" (or
// no name at all) means. The old sidecar and profile-member read steps
// are gone: nothing on disk beside the binding names an adapter any
// more, and the binding names a dialect, never a command.
func ResolveAdapter(opts Options, configDir, adapterName string) (Adapter, error) {
	if opts.AdapterCommand != "" {
		return parseSpec(opts.AdapterCommand, configDir, SourceOption)
	}
	if adapterName != "" && adapterName != BundledAdapterName {
		return parseSpec(adapterName, configDir, SourceProfile)
	}
	return Adapter{Bundled: true, Source: SourceBundled}, nil
}

// parseSpec interprets one adapter value in the three accepted forms.
func parseSpec(value, configDir, source string) (Adapter, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return Adapter{}, errAdapter(ReasonAdapterMalformed, source, msgMalformed)
	}
	if v[0] == '[' {
		argv, err := decodeArgv([]byte(v))
		if err != nil || len(argv) == 0 {
			return Adapter{}, errAdapter(ReasonAdapterMalformed, source, msgMalformed)
		}
		if !filepath.IsAbs(argv[0]) {
			return Adapter{}, errAdapter(ReasonAdapterRelative, source, msgRelative)
		}
		return Adapter{Argv: argv, Source: source}, nil
	}
	if strings.ContainsAny(v, "\n\r") {
		// The sidecar is one line; a path or a name never spans lines.
		return Adapter{}, errAdapter(ReasonAdapterMalformed, source, msgMalformed)
	}
	switch {
	case v[0] == '{':
		return Adapter{}, errAdapter(ReasonAdapterMalformed, source, msgMalformed)
	case filepath.IsAbs(v):
		if hasSpace(v) {
			return Adapter{}, errAdapter(ReasonAdapterMalformed, source, msgCommandLine)
		}
		return Adapter{Argv: []string{filepath.Clean(v)}, Source: source}, nil
	case CheckAdapterName(v) == nil:
		if v == BundledAdapterName {
			return Adapter{Bundled: true, Source: source}, nil
		}
		return lookupRegistry(v, configDir, source)
	case looksLikePath(v):
		return Adapter{}, errAdapter(ReasonAdapterRelative, source, msgRelative)
	default:
		return Adapter{}, errAdapter(ReasonAdapterMalformed, source, msgMalformed)
	}
}

// hasSpace reports whether v contains whitespace. A bare path with a
// space is refused in favour of the array form: "/opt/adapter --root /x"
// typed as a single path is a command line that no shell will ever split
// here (4.1), and refusing it says so instead of failing at spawn with
// adapter_not_found.
func hasSpace(v string) bool {
	return strings.ContainsAny(v, " \t")
}

// looksLikePath reports whether v reads as a relative path (a slash, no
// whitespace and none of the JSON punctuation), so the refusal can say
// "relative" rather than "malformed".
func looksLikePath(v string) bool {
	return strings.ContainsRune(v, '/') && !hasSpace(v) && !strings.ContainsAny(v, "\"'{}[]")
}

// lookupRegistry resolves a registered name through adapters.json.
func lookupRegistry(name, configDir, source string) (Adapter, error) {
	data, err := adapterkit.ReadStrict(RegistryPath(configDir))
	var perr *protocol.Error
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		return Adapter{}, errAdapter(ReasonAdapterUnregistered, source, msgUnregistered)
	case errors.As(err, &perr):
		return Adapter{}, perr
	default:
		return Adapter{}, errAdapter(ReasonRegistryUnreadable, source, "adapters.json could not be read")
	}
	var reg map[string]jsontext.Value
	if err := json.Unmarshal(data, &reg); err != nil {
		return Adapter{}, errAdapter(ReasonRegistryMalformed, source, msgRegistryMalformed)
	}
	raw, ok := reg[name]
	if !ok {
		return Adapter{}, errAdapter(ReasonAdapterUnregistered, source, msgUnregistered)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch {
		case !filepath.IsAbs(s):
			return Adapter{}, errAdapter(ReasonAdapterRelative, source, msgRelative)
		case hasSpace(s):
			return Adapter{}, errAdapter(ReasonAdapterMalformed, source, msgCommandLine)
		}
		return Adapter{Argv: []string{filepath.Clean(s)}, Source: source}, nil
	}
	argv, err := decodeArgv(raw)
	if err != nil {
		return Adapter{}, errAdapter(ReasonRegistryMalformed, source, msgRegistryMalformed)
	}
	if len(argv) == 0 {
		return Adapter{}, errAdapter(ReasonAdapterMalformed, source, msgMalformed)
	}
	if !filepath.IsAbs(argv[0]) {
		return Adapter{}, errAdapter(ReasonAdapterRelative, source, msgRelative)
	}
	return Adapter{Argv: argv, Source: source}, nil
}

// decodeArgv parses a JSON array of non-empty strings. An empty array is
// returned as such; the caller decides what it means.
func decodeArgv(data []byte) ([]string, error) {
	var argv []string
	if err := json.Unmarshal(data, &argv); err != nil {
		return nil, err
	}
	for _, a := range argv {
		if a == "" {
			return nil, errors.New("empty argv element")
		}
	}
	if argv == nil {
		argv = []string{}
	}
	return argv, nil
}

// The fixed messages of adapter resolution failures.
const (
	msgRelative          = "the adapter command must be an absolute path, or a JSON array whose first element is one; a relative path is refused because it would resolve against whatever the working directory happens to be"
	msgUnregistered      = "the adapter name is not registered in adapters.json; add an entry there mapping the name to the adapter's absolute path or argv"
	msgMalformed         = "the adapter command is not an absolute path, a JSON array of strings, or a registered name"
	msgCommandLine       = "the adapter command reads as a command line; there is no shell here (4.1), so give fixed arguments as a JSON array such as [\"/abs/adapter\", \"--flag\"] — a path that itself contains spaces must use the array form too"
	msgMalformedResolved = "the resolved adapter command must be a JSON array of strings starting with an absolute path, or an absolute path"
	msgRegistryMalformed = "adapters.json must be a JSON object mapping names to an absolute path or a JSON array of strings"
)

// errAdapter is the `config` failure of adapter resolution.
func errAdapter(reason, source, message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: message,
		Details: map[string]string{"reason": reason, "source": source},
	}
}

// SourceRegistry names adapters.json itself as the source of a failure of
// RegisterAdapter.
const SourceRegistry = "registry"

// RegisterAdapter adds or replaces the entry name → argv in
// ${configDir}/adapters.json (D36: a third-party name is registered once by
// `profile init --adapter`; the file stays editable by the human). name must
// pass CheckAdapterName and must not be the bundled name (it needs no entry
// and cannot be rebound); argv must be non-empty with an absolute first
// element. An existing registry is read through ReadStrict and refused on
// the same terms as resolution (insecure_mode, registry_malformed), so a
// broken or world-readable file is never silently replaced; every other
// entry is kept verbatim. The file is written atomically, 0600.
func RegisterAdapter(configDir, name string, argv []string) error {
	if CheckAdapterName(name) != nil || name == BundledAdapterName {
		return errAdapter(ReasonAdapterMalformed, SourceRegistry, "the adapter name must be 1-64 characters from letters, digits, dash and underscore, and not the bundled name")
	}
	switch {
	case len(argv) == 0:
		return errAdapter(ReasonAdapterMalformed, SourceRegistry, msgMalformed)
	case sessionmap.CheckAdapterCommand(argv) != nil && filepath.IsAbs(argv[0]):
		return errAdapter(ReasonAdapterMalformed, SourceRegistry, msgMalformed)
	case sessionmap.CheckAdapterCommand(argv) != nil:
		return errAdapter(ReasonAdapterRelative, SourceRegistry, msgRelative)
	}
	reg := map[string]jsontext.Value{}
	data, err := adapterkit.ReadStrict(RegistryPath(configDir))
	var perr *protocol.Error
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &reg); err != nil {
			return errAdapter(ReasonRegistryMalformed, SourceRegistry, msgRegistryMalformed)
		}
	case errors.Is(err, fs.ErrNotExist):
	case errors.As(err, &perr):
		return perr
	default:
		return errAdapter(ReasonRegistryUnreadable, SourceRegistry, "adapters.json could not be read")
	}
	entry, err := json.Marshal(argv)
	if err != nil {
		return errAdapter(ReasonAdapterMalformed, SourceRegistry, msgMalformed)
	}
	reg[name] = jsontext.Value(entry)
	out, err := json.Marshal(reg, json.Deterministic(true))
	if err != nil {
		return errAdapter(ReasonRegistryMalformed, SourceRegistry, msgRegistryMalformed)
	}
	if err := adapterkit.MkdirPrivate(configDir); err != nil {
		return errAdapter(ReasonRegistryUnreadable, SourceRegistry, "the config directory could not be created")
	}
	if err := adapterkit.WriteAtomic(RegistryPath(configDir), append(out, '\n')); err != nil {
		return errAdapter(ReasonRegistryUnreadable, SourceRegistry, "adapters.json could not be written")
	}
	return nil
}
