package syncthing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/protocol"
)

// maxResponseBytes caps what one REST answer may cost. Syncthing's largest
// answer here — /rest/config/folders on an instance with many folders — is
// far below it.
const maxResponseBytes = 8 << 20

// An api is Syncthing's REST API on one running instance (plan 3.2,
// docs.syncthing.net/rest): loopback only, the X-API-Key header from
// config.xml. The key lives in this struct for one invocation and nowhere
// else — never on argv, never in a log line, never in an error.
type api struct {
	base   string
	key    string
	client *http.Client
}

// api reads the key from config.xml (fresh on every invocation: Syncthing
// owns the file) and the port Brigade kept, and builds the client.
func (in *instance) api() (*api, error) {
	key, err := readAPIKey(in.path("config.xml"))
	if err != nil {
		return nil, err
	}
	port, err := in.currentPort()
	if err != nil {
		return nil, err
	}
	return &api{base: in.a.d.baseURL(port), key: key, client: in.a.http}, nil
}

// readAPIKey parses config.xml for <gui><apikey>. A missing file, a file
// Syncthing is still writing, or an empty key are all "not ready".
func readAPIKey(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the instance's own config.xml under the state directory
	if err != nil {
		return "", errUnavailable("not_running", "the syncthing instance has no configuration yet")
	}
	var cfg struct {
		GUI struct {
			APIKey string `xml:"apikey"`
		} `xml:"gui"`
	}
	if err := xml.Unmarshal(raw, &cfg); err != nil || cfg.GUI.APIKey == "" {
		return "", errUnavailable("not_running", "the syncthing instance's configuration carries no API key yet")
	}
	return cfg.GUI.APIKey, nil
}

// errAPI is the refusal for a REST call that failed. It names the endpoint
// and the HTTP status, never Syncthing's body or the key.
func errAPI(endpoint string, status int) error {
	d := map[string]string{"reason": "syncthing_api", "endpoint": endpoint}
	if status != 0 {
		d["status"] = strconv.Itoa(status)
	}
	return &protocol.Error{
		Code:    protocol.CodeUnavailable,
		Message: "the syncthing API did not answer as expected",
		Details: d,
	}
}

// do performs one call. body, when non-nil, is sent as JSON; out, when
// non-nil, receives the decoded answer. A non-2xx answer is an errAPI with
// its status (callers tell a 4xx refusal from a transport failure by it).
func (c *api) do(method, endpoint string, query url.Values, body, out any) error {
	u := c.base + endpoint
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	// The client's 5 s Timeout bounds the call; there is no caller context
	// to carry.
	req, err := http.NewRequestWithContext(context.Background(), method, u, reader)
	if err != nil {
		return errAPI(endpoint, 0)
	}
	req.Header.Set("X-API-Key", c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return errAPI(endpoint, 0)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return errAPI(endpoint, 0)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errAPI(endpoint, resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return errAPI(endpoint, resp.StatusCode)
		}
	}
	return nil
}

// httpStatus is the status an errAPI carries, or 0.
func httpStatus(err error) int {
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Details == nil {
		return 0
	}
	n, _ := strconv.Atoi(perr.Details["status"])
	return n
}

type systemStatus struct {
	MyID string `json:"myID"`
}

func (c *api) systemStatus() (*systemStatus, error) {
	var st systemStatus
	if err := c.do(http.MethodGet, "/rest/system/status", nil, nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (c *api) shutdown() error {
	return c.do(http.MethodPost, "/rest/system/shutdown", nil, nil, nil)
}

// connections maps each configured device id to whether it is connected
// now (GET /rest/system/connections).
func (c *api) connections() (map[string]bool, error) {
	var resp struct {
		Connections map[string]struct {
			Connected bool `json:"connected"`
		} `json:"connections"`
	}
	if err := c.do(http.MethodGet, "/rest/system/connections", nil, nil, &resp); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(resp.Connections))
	for id, conn := range resp.Connections {
		out[id] = conn.Connected
	}
	return out, nil
}

// configuredFolder is the part of a /rest/config/folders entry this
// adapter reads: its devices too, because apply keeps every device a
// folder already has (a server a person added by hand stays); its label
// and whether it is paused, because apply pauses a folder this checkout
// shared and its project no longer lists (formerFolders).
type configuredFolder struct {
	ID      string      `json:"id"`
	Label   string      `json:"label"`
	Path    string      `json:"path"`
	Paused  bool        `json:"paused"`
	Devices []deviceRef `json:"devices"`
}

func (c *api) folders() ([]configuredFolder, error) {
	var out []configuredFolder
	if err := c.do(http.MethodGet, "/rest/config/folders", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type configuredDevice struct {
	DeviceID string `json:"deviceID"`
}

func (c *api) devices() ([]configuredDevice, error) {
	var out []configuredDevice
	if err := c.do(http.MethodGet, "/rest/config/devices", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// folderState is Syncthing's state word for one folder (GET
// /rest/db/status), or "unknown" when it does not answer — a folder added a
// moment ago can briefly be absent from the database.
func (c *api) folderState(id string) string {
	var st struct {
		State string `json:"state"`
	}
	if err := c.do(http.MethodGet, "/rest/db/status", url.Values{"folder": {id}}, nil, &st); err != nil || st.State == "" {
		return "unknown"
	}
	return st.State
}

// The objects apply posts (plan 3.2; docs.syncthing.net/rest/config.html).
// POST /rest/config/devices and /rest/config/folders replace-or-add by id,
// which is what makes apply idempotent. Every member not named here keeps
// Syncthing's default: discovery, relays and NAT traversal stay on.
type deviceConfig struct {
	DeviceID  string   `json:"deviceID"`
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
}

// A deviceRef is one entry of a folder's device list. The members beyond
// the id are Syncthing's, carried back unchanged for a device apply keeps
// (an untrusted device's encryption password among them) and never logged.
type deviceRef struct {
	DeviceID           string `json:"deviceID"`
	IntroducedBy       string `json:"introducedBy,omitzero"`
	EncryptionPassword string `json:"encryptionPassword,omitzero"`
}

// listenOptions is the one member of /rest/config/options apply's
// instance sets (plan folder-sync 4.4, P18-6): the sync listen
// addresses, on the instance's own port.
type listenOptions struct {
	ListenAddresses []string `json:"listenAddresses"`
}

// listenAddresses is the instance's listen address list on port: TCP and
// QUIC on every interface, and Syncthing's dynamic relay pool — the
// default list with the instance's own port in place of 22000, so a
// Syncthing a person already runs keeps 22000 to itself.
func listenAddresses(port int) []string {
	p := strconv.Itoa(port)
	return []string{"tcp://0.0.0.0:" + p, "quic://0.0.0.0:" + p, "dynamic+https://relays.syncthing.net/endpoint"}
}

// ensureListen sets options.listenAddresses to listenAddresses(port) when
// they differ (PATCH /rest/config/options, which leaves every other
// option as it is), and reports whether it changed them.
func (c *api) ensureListen(port int) (bool, error) {
	var cur listenOptions
	if err := c.do(http.MethodGet, "/rest/config/options", nil, nil, &cur); err != nil {
		return false, err
	}
	want := listenAddresses(port)
	if slices.Equal(cur.ListenAddresses, want) {
		return false, nil
	}
	if err := c.do(http.MethodPatch, "/rest/config/options", nil, listenOptions{ListenAddresses: want}, nil); err != nil {
		return false, err
	}
	return true, nil
}

type versioningConfig struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params"`
}

type folderConfig struct {
	ID               string           `json:"id"`
	Label            string           `json:"label"`
	Path             string           `json:"path"`
	Type             string           `json:"type"`
	Devices          []deviceRef      `json:"devices"`
	Versioning       versioningConfig `json:"versioning"`
	FSWatcherEnabled bool             `json:"fsWatcherEnabled"`
	RescanIntervalS  int              `json:"rescanIntervalS"`
	// Paused is always sent, false: a folder the project lists again
	// after apply paused it (formerFolders) syncs again.
	Paused bool `json:"paused"`
}

// pausePatch is the one member apply PATCHes on a folder the project no
// longer lists (PATCH /rest/config/folders/<id>, which leaves every
// other member as it is).
type pausePatch struct {
	Paused bool `json:"paused"`
}

// running is the daemon's API when daemon.pid names a live process, else
// the `not_running` refusal: apply and status never start a daemon — that
// is attach's, and only attach counts a session in.
func (in *instance) running() (*api, error) {
	if _, ok := in.daemon(); !ok {
		return nil, errUnavailable("not_running", "the syncthing instance is not running; attach starts it")
	}
	return in.api()
}

// apply introduces the peers and shares the folders with every one of them
// (plan 4.3, 4.4). It only ever adds: a device the instance already holds
// is left exactly as it is (a person may have added it by hand, with its
// own name and addresses); a folder's device list becomes the devices it
// already has plus the peers, never fewer, so an always-on Syncthing added
// to a folder by hand stays; devices no longer listed are left alone. A
// folder id the instance holds at another path belongs to another
// checkout on this machine and is left alone too (conflict_path). A
// folder THIS checkout shared and its project no longer lists is paused,
// never deleted — its files stay — and reported as `paused`; it syncs
// again when the project lists it again, because every folder apply
// posts carries paused: false. A folder whose directory cannot be
// created is reported `rejected`, and the rest are applied.
func (in *instance) apply(folders []folderSpec, peers []peerSpec) (*applyResult, error) {
	for _, f := range folders {
		if f.ID == "" || !filepath.IsAbs(f.Path) {
			return nil, errInput("folders", "every folder needs an id and an absolute path")
		}
	}
	for _, p := range peers {
		if p.Peer == "" {
			return nil, errInput("peers", "every peer needs a descriptor")
		}
	}
	c, err := in.running()
	if err != nil {
		return nil, err
	}

	// One teammate's unusable descriptor (Syncthing answers 4xx) must not
	// stop the rest of the team syncing: it is skipped, logged by status
	// code only, and left out of the folders' device lists (a folder
	// naming an unknown device would be refused whole). A device already
	// configured is accepted as it stands, never re-posted.
	known, err := c.devices()
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(known))
	for _, d := range known {
		have[d.DeviceID] = true
	}
	devs := make([]deviceRef, 0, len(peers))
	for _, p := range peers {
		if have[p.Peer] {
			devs = append(devs, deviceRef{DeviceID: p.Peer})
			continue
		}
		err := c.do(http.MethodPost, "/rest/config/devices", nil,
			deviceConfig{DeviceID: p.Peer, Name: p.Label, Addresses: []string{"dynamic"}}, nil)
		if err != nil {
			if s := httpStatus(err); s >= 400 && s < 500 {
				in.a.log.Warn("syncthing refused a peer; skipped", slog.Int("status", s))
				continue
			}
			return nil, err
		}
		devs = append(devs, deviceRef{DeviceID: p.Peer})
	}

	existing, err := c.folders()
	if err != nil {
		return nil, err
	}
	result := &applyResult{Folders: make([]folderState, 0, len(folders)), Peers: make([]peerState, 0, len(peers))}
	for _, f := range folders {
		path := filepath.Clean(f.Path)
		current, conflict := place(existing, f.ID, path)
		if conflict {
			result.Folders = append(result.Folders, folderState{ID: f.ID, State: stateConflictPath})
			continue
		}
		var had []deviceRef
		if current != nil {
			had = current.Devices
		}
		// The folder is the project's (a path it lists), so it is created
		// with an ordinary directory mode, not the state tree's 0700.
		if err := os.MkdirAll(path, 0o755); err != nil { //nolint:gosec // G301: a project folder, readable like its siblings
			in.a.log.Warn("a folder could not be created; rejected", slog.String("folder_id", f.ID), adapterlog.Err(err))
			result.Folders = append(result.Folders, folderState{ID: f.ID, State: stateRejected})
			continue
		}
		err := c.do(http.MethodPost, "/rest/config/folders", nil, folderConfig{
			ID: f.ID, Label: f.Label, Path: path, Type: "sendreceive",
			Devices:          unionDevices(had, devs),
			Versioning:       versioningConfig{Type: "trashcan", Params: map[string]string{"cleanoutDays": "14"}},
			FSWatcherEnabled: true,
			RescanIntervalS:  60,
		}, nil)
		if err != nil {
			if s := httpStatus(err); s >= 400 && s < 500 {
				in.a.log.Warn("syncthing refused a folder", slog.String("folder_id", f.ID), slog.Int("status", s))
				result.Folders = append(result.Folders, folderState{ID: f.ID, State: stateRejected})
				continue
			}
			return nil, err
		}
		result.Folders = append(result.Folders, folderState{ID: f.ID, State: c.folderState(f.ID)})
	}
	for _, e := range formerFolders(existing, folders) {
		if !e.Paused {
			// A failed pause is logged and tried again next round; it
			// never costs the listed folders their apply.
			if err := c.do(http.MethodPatch, "/rest/config/folders/"+url.PathEscape(e.ID), nil, pausePatch{Paused: true}, nil); err != nil {
				in.a.log.Warn("a folder the project no longer lists could not be paused", slog.String("folder_id", e.ID), adapterlog.Err(err))
				continue
			}
			in.a.log.Info("paused a folder the project no longer lists", slog.String("folder_id", e.ID))
		}
		result.Folders = append(result.Folders, folderState{ID: e.ID, State: statePaused})
	}

	conns, err := c.connections()
	if err != nil {
		return nil, err
	}
	for _, p := range peers {
		result.Peers = append(result.Peers, peerState{Peer: p.Peer, Connected: conns[p.Peer]})
	}
	return result, nil
}

// stateConflictPath is the folder state for a folder this checkout cannot
// hold (plan 4.4): its id is held at another path — the same folder of a
// second clone of the repository on this machine — or its path is held by
// another folder id.
const stateConflictPath = "conflict_path"

// stateRejected is the folder state for a folder Syncthing refused, or
// whose directory could not be created.
const stateRejected = "rejected"

// statePaused is the folder state for a folder this checkout shared and
// its project no longer lists: apply paused it (formerFolders).
const statePaused = "paused"

// formerFolders is the folders of existing that THIS checkout shared and
// its project no longer lists: apply pauses them. A folder qualifies only
// when all of this holds, so another checkout's folder — another
// repository of the same team, a second clone on another branch, a
// folder a person configured by hand — is never touched:
//
//   - its id carries this team's prefix, "brigade-<ref8>-", which every
//     requested id shares (a request whose ids do not share one pauses
//     nothing, and so does an empty one);
//   - it is not requested;
//   - its label ends in a folder name whose hash is its id — Brigade
//     labels a folder "<repository>/<folder>" and derives the id from the
//     folder (docs/sync-adapters.md, "Folder ids") — so the name is
//     proven, not guessed;
//   - its path is where this checkout puts that name: the checkout's
//     root (or the ancestor a ".." folder climbs to) is recovered the same
//     way from a requested folder's proven name and path.
func formerFolders(existing []configuredFolder, folders []folderSpec) []configuredFolder {
	if len(folders) == 0 {
		return nil
	}
	prefix, ok := teamPrefix(folders[0].ID)
	if !ok {
		return nil
	}
	// An anchor is an ancestor of the checkout's root: base is the root
	// with up levels climbed.
	type anchor struct {
		up   int
		base string
	}
	requested := make(map[string]bool, len(folders))
	var anchors []anchor
	for _, f := range folders {
		if p, ok := teamPrefix(f.ID); !ok || p != prefix {
			return nil
		}
		requested[f.ID] = true
		name, ok := folderName(prefix, f.ID, f.Label)
		if !ok {
			continue
		}
		up, rest := climb(name)
		if base, ok := trimFolder(filepath.Clean(f.Path), rest); ok {
			anchors = append(anchors, anchor{up: up, base: base})
		}
	}
	var out []configuredFolder
	for _, e := range existing {
		if requested[e.ID] || !strings.HasPrefix(e.ID, prefix) {
			continue
		}
		name, ok := folderName(prefix, e.ID, e.Label)
		if !ok {
			continue
		}
		up, rest := climb(name)
		for _, a := range anchors {
			if up < a.up {
				continue
			}
			base := a.base
			for range up - a.up {
				base = filepath.Dir(base)
			}
			if samePath(e.Path, filepath.Join(base, rest)) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// teamPrefix is a Brigade folder id's team part, "brigade-<ref8>-", or
// false for an id of another shape.
func teamPrefix(id string) (string, bool) {
	parts := strings.Split(id, "-")
	if len(parts) != 3 || parts[0] != "brigade" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return parts[0] + "-" + parts[1] + "-", true
}

// folderName finds the folder name a Brigade label ends in whose id,
// under prefix, is id: the whole label (a session with no workspace
// label) or the text after any "/" of it ("<repository>/<folder>", the
// folder itself possibly nested).
func folderName(prefix, id, label string) (string, bool) {
	for i := -1; i < len(label); i++ {
		if i >= 0 && label[i] != '/' {
			continue
		}
		name := label[i+1:]
		if name == "" || name == "." || strings.HasPrefix(name, "/") || filepath.ToSlash(filepath.Clean(filepath.FromSlash(name))) != name {
			continue
		}
		sum := sha256.Sum256([]byte(name))
		if prefix+hex.EncodeToString(sum[:])[:12] == id {
			return name, true
		}
	}
	return "", false
}

// climb splits a clean relative folder name into the ".." levels it
// climbs (path.Clean puts them all first) and the rest.
func climb(name string) (int, string) {
	up := 0
	for {
		switch {
		case name == "..":
			return up + 1, ""
		case strings.HasPrefix(name, "../"):
			up++
			name = name[len("../"):]
		default:
			return up, name
		}
	}
}

// trimFolder is p with the folder part rest removed from its end — the
// directory rest was joined to — or false when p does not end in rest.
func trimFolder(p, rest string) (string, bool) {
	if rest == "" {
		return p, true
	}
	base, ok := strings.CutSuffix(p, string(filepath.Separator)+filepath.FromSlash(rest))
	if !ok {
		return "", false
	}
	if base == "" {
		base = string(filepath.Separator)
	}
	return base, true
}

// place finds folder id in the instance's configuration: the entry when
// it is configured at path (nil when it is not configured yet), and
// conflict when this checkout must leave it alone — the id is configured
// at a different path (another checkout on this machine got there first;
// re-pointing it would move the folder between the two every round), or
// another id already syncs path (one instance cannot hold one path under
// two ids, plan 3.2).
func place(existing []configuredFolder, id, path string) (*configuredFolder, bool) {
	var current *configuredFolder
	for i := range existing {
		e := &existing[i]
		same := samePath(e.Path, path)
		switch {
		case e.ID == id && !same:
			return nil, true
		case e.ID == id:
			current = e
		case same:
			return nil, true
		}
	}
	return current, false
}

// samePath reports whether a and b name one directory: equal once
// cleaned, or equal once symlinks are resolved (macOS's /var is
// /private/var, and a path posted by an earlier round may be either
// spelling).
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// unionDevices is had followed by every device of add it does not name
// yet: a folder's device list only ever grows here, and an entry it
// already has is kept exactly as Syncthing reported it.
func unionDevices(had, add []deviceRef) []deviceRef {
	out := make([]deviceRef, 0, len(had)+len(add))
	seen := make(map[string]bool, len(had)+len(add))
	for _, d := range had {
		if d.DeviceID == "" || seen[d.DeviceID] {
			continue
		}
		seen[d.DeviceID] = true
		out = append(out, d)
	}
	for _, d := range add {
		if !seen[d.DeviceID] {
			seen[d.DeviceID] = true
			out = append(out, d)
		}
	}
	return out
}

// status reports the engine as it is: every folder and every other device
// the instance holds (it is shared by every session and team on this
// machine), with their states. No daemon is running=false and empty lists,
// not a failure.
func (in *instance) status() (*statusResult, error) {
	result := &statusResult{Folders: []folderState{}, Peers: []peerState{}}
	if _, ok := in.daemon(); !ok {
		return result, nil
	}
	c, err := in.api()
	if err != nil {
		return nil, err
	}
	st, err := c.systemStatus()
	if err != nil {
		return nil, err
	}
	result.Running = true
	result.Peer = st.MyID
	folders, err := c.folders()
	if err != nil {
		return nil, err
	}
	for _, f := range folders {
		result.Folders = append(result.Folders, folderState{ID: f.ID, Path: f.Path, State: c.folderState(f.ID)})
	}
	devices, err := c.devices()
	if err != nil {
		return nil, err
	}
	conns, err := c.connections()
	if err != nil {
		return nil, err
	}
	for _, d := range devices {
		if d.DeviceID == st.MyID {
			continue
		}
		result.Peers = append(result.Peers, peerState{Peer: d.DeviceID, Connected: conns[d.DeviceID]})
	}
	return result, nil
}
