package syncthing

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

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
// adapter reads.
type configuredFolder struct {
	ID   string `json:"id"`
	Path string `json:"path"`
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

type deviceRef struct {
	DeviceID string `json:"deviceID"`
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
// (plan 4.3, 4.4). Devices and folders no longer listed are left alone:
// removing a device would churn its connections, and a folder another
// project on this machine shares is not this call's to drop.
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
	// naming an unknown device would be refused whole).
	devs := make([]deviceRef, 0, len(peers))
	for _, p := range peers {
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
		if heldByOther(existing, f.ID, path) {
			result.Folders = append(result.Folders, folderState{ID: f.ID, State: "conflict_path"})
			continue
		}
		// The folder is the project's (a path it lists), so it is created
		// with an ordinary directory mode, not the state tree's 0700.
		if err := os.MkdirAll(path, 0o755); err != nil { //nolint:gosec // G301: a project folder, readable like its siblings
			return nil, err
		}
		err := c.do(http.MethodPost, "/rest/config/folders", nil, folderConfig{
			ID: f.ID, Label: f.Label, Path: path, Type: "sendreceive",
			Devices:          devs,
			Versioning:       versioningConfig{Type: "trashcan", Params: map[string]string{"cleanoutDays": "14"}},
			FSWatcherEnabled: true,
			RescanIntervalS:  60,
		}, nil)
		if err != nil {
			if s := httpStatus(err); s >= 400 && s < 500 {
				in.a.log.Warn("syncthing refused a folder", slog.String("folder_id", f.ID), slog.Int("status", s))
				result.Folders = append(result.Folders, folderState{ID: f.ID, State: "rejected"})
				continue
			}
			return nil, err
		}
		result.Folders = append(result.Folders, folderState{ID: f.ID, State: c.folderState(f.ID)})
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

// heldByOther reports whether another folder id already syncs path: one
// Syncthing instance cannot hold one path under two ids (plan 3.2), which
// is the second clone of a repository on one machine.
func heldByOther(existing []configuredFolder, id, path string) bool {
	for _, e := range existing {
		if e.ID != id && filepath.Clean(e.Path) == path {
			return true
		}
	}
	return false
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
