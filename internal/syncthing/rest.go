package syncthing

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// maxResponseBytes caps what one REST answer may cost. Syncthing's largest
// answer here — /rest/config/folders on an instance with many folders — is
// far below it.
const maxResponseBytes = 8 << 20

// The reason tokens of the `unavailable` refusals this package emits.
const (
	ReasonNotRunning = "not_running"
	ReasonNotFound   = "syncthing_not_found"
	ReasonAPI        = "syncthing_api"
)

// An API is Syncthing's REST API on one running instance (plan 3.2,
// docs.syncthing.net/rest): loopback only, the X-API-Key header from
// config.xml. The key lives in this struct for one invocation and nowhere
// else — never on argv, never in a log line, never in an error.
type API struct {
	base   string
	key    string
	client *http.Client
}

// ReadAPIKey parses config.xml for <gui><apikey>. A missing file, a file
// Syncthing is still writing, or an empty key are all "not ready".
func ReadAPIKey(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the instance's own config.xml under the state directory
	if err != nil {
		return "", ErrUnavailable(ReasonNotRunning, "the syncthing instance has no configuration yet")
	}
	var cfg struct {
		GUI struct {
			APIKey string `xml:"apikey"`
		} `xml:"gui"`
	}
	if err := xml.Unmarshal(raw, &cfg); err != nil || cfg.GUI.APIKey == "" {
		return "", ErrUnavailable(ReasonNotRunning, "the syncthing instance's configuration carries no API key yet")
	}
	return cfg.GUI.APIKey, nil
}

// ErrUnavailable is the 4.6 `unavailable` refusal with a reason a caller
// can turn into its one human line.
func ErrUnavailable(reason, message string) error {
	return &protocol.Error{
		Code:    protocol.CodeUnavailable,
		Message: message,
		Details: map[string]string{"reason": reason},
	}
}

// ErrAPI is the refusal for a REST call that failed. It names the endpoint
// and the HTTP status, never Syncthing's body or the key.
func ErrAPI(endpoint string, status int) error {
	d := map[string]string{"reason": ReasonAPI, "endpoint": endpoint}
	if status != 0 {
		d["status"] = strconv.Itoa(status)
	}
	return &protocol.Error{
		Code:    protocol.CodeUnavailable,
		Message: "the syncthing API did not answer as expected",
		Details: d,
	}
}

// HTTPStatus is the status an ErrAPI carries, or 0.
func HTTPStatus(err error) int {
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Details == nil {
		return 0
	}
	n, _ := strconv.Atoi(perr.Details["status"])
	return n
}

// Do performs one call. body, when non-nil, is sent as JSON; out, when
// non-nil, receives the decoded answer. A non-2xx answer is an ErrAPI with
// its status (callers tell a 4xx refusal from a transport failure by it).
func (c *API) Do(method, endpoint string, query url.Values, body, out any) error {
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
	// The client's Timeout bounds the call; there is no caller context to
	// carry.
	req, err := http.NewRequestWithContext(context.Background(), method, u, reader)
	if err != nil {
		return ErrAPI(endpoint, 0)
	}
	req.Header.Set("X-API-Key", c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return ErrAPI(endpoint, 0)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return ErrAPI(endpoint, 0)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ErrAPI(endpoint, resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return ErrAPI(endpoint, resp.StatusCode)
		}
	}
	return nil
}

// SystemStatus is GET /rest/system/status, of which MyID is read.
type SystemStatus struct {
	MyID string `json:"myID"`
}

// SystemStatus answers the instance's own device id.
func (c *API) SystemStatus() (*SystemStatus, error) {
	var st SystemStatus
	if err := c.Do(http.MethodGet, "/rest/system/status", nil, nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Shutdown asks the daemon to stop (POST /rest/system/shutdown).
func (c *API) Shutdown() error {
	return c.Do(http.MethodPost, "/rest/system/shutdown", nil, nil, nil)
}

// Connections maps each configured device id to whether it is connected
// now (GET /rest/system/connections).
func (c *API) Connections() (map[string]bool, error) {
	var resp struct {
		Connections map[string]struct {
			Connected bool `json:"connected"`
		} `json:"connections"`
	}
	if err := c.Do(http.MethodGet, "/rest/system/connections", nil, nil, &resp); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(resp.Connections))
	for id, conn := range resp.Connections {
		out[id] = conn.Connected
	}
	return out, nil
}

// A ConfiguredFolder is the part of a /rest/config/folders entry the
// adapters read: its devices too, because the sync adapter keeps every
// device a folder already has (a server a person added by hand stays) and
// carries them to the folder's new id; its label and whether it is
// paused, because it pauses a folder a checkout shared and its project no
// longer lists.
type ConfiguredFolder struct {
	ID      string      `json:"id"`
	Label   string      `json:"label"`
	Path    string      `json:"path"`
	Paused  bool        `json:"paused"`
	Devices []DeviceRef `json:"devices"`
}

// Folders is GET /rest/config/folders.
func (c *API) Folders() ([]ConfiguredFolder, error) {
	var out []ConfiguredFolder
	if err := c.Do(http.MethodGet, "/rest/config/folders", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// A ConfiguredDevice is the part of a /rest/config/devices entry the
// adapters read.
type ConfiguredDevice struct {
	DeviceID string `json:"deviceID"`
	Name     string `json:"name"`
}

// Devices is GET /rest/config/devices.
func (c *API) Devices() ([]ConfiguredDevice, error) {
	var out []ConfiguredDevice
	if err := c.Do(http.MethodGet, "/rest/config/devices", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FolderState is Syncthing's state word for one folder (GET
// /rest/db/status), or "unknown" when it does not answer — a folder added a
// moment ago can briefly be absent from the database.
func (c *API) FolderState(id string) string {
	var st struct {
		State string `json:"state"`
	}
	if err := c.Do(http.MethodGet, "/rest/db/status", url.Values{"folder": {id}}, nil, &st); err != nil || st.State == "" {
		return "unknown"
	}
	return st.State
}

// The objects the adapters post (plan 3.2; docs.syncthing.net/rest/config.html).
// POST /rest/config/devices and /rest/config/folders replace-or-add by id,
// which is what makes an apply idempotent. Every member not named here
// keeps Syncthing's default: discovery, relays and NAT traversal stay on.
type DeviceConfig struct {
	DeviceID  string   `json:"deviceID"`
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
}

// A DeviceRef is one entry of a folder's device list. The members beyond
// the id are Syncthing's, carried back unchanged for a device the sync
// adapter keeps (an untrusted device's encryption password among them)
// and never logged.
type DeviceRef struct {
	DeviceID           string `json:"deviceID"`
	IntroducedBy       string `json:"introducedBy,omitzero"`
	EncryptionPassword string `json:"encryptionPassword,omitzero"`
}

// ListenOptions is the one member of /rest/config/options the engine
// sets (plan folder-sync 4.4, P18-6): the sync listen addresses, on the
// instance's own port.
type ListenOptions struct {
	ListenAddresses []string `json:"listenAddresses"`
}

// ListenAddresses is the instance's listen address list on port: TCP and
// QUIC on every interface, and Syncthing's dynamic relay pool — the
// default list with the instance's own port in place of 22000, so a
// Syncthing a person already runs keeps 22000 to itself.
func ListenAddresses(port int) []string {
	p := strconv.Itoa(port)
	return []string{"tcp://0.0.0.0:" + p, "quic://0.0.0.0:" + p, "dynamic+https://relays.syncthing.net/endpoint"}
}

// EnsureListen sets options.listenAddresses to ListenAddresses(port) when
// they differ (PATCH /rest/config/options, which leaves every other
// option as it is), and reports whether it changed them.
func (c *API) EnsureListen(port int) (bool, error) {
	var cur ListenOptions
	if err := c.Do(http.MethodGet, "/rest/config/options", nil, nil, &cur); err != nil {
		return false, err
	}
	want := ListenAddresses(port)
	if slices.Equal(cur.ListenAddresses, want) {
		return false, nil
	}
	if err := c.Do(http.MethodPatch, "/rest/config/options", nil, ListenOptions{ListenAddresses: want}, nil); err != nil {
		return false, err
	}
	return true, nil
}

// VersioningConfig is a folder's versioning member: the sync adapter's
// trash can, or the empty type for none.
type VersioningConfig struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params"`
}

// A FolderConfig is what an adapter posts to /rest/config/folders.
type FolderConfig struct {
	ID               string           `json:"id"`
	Label            string           `json:"label"`
	Path             string           `json:"path"`
	Type             string           `json:"type"`
	Devices          []DeviceRef      `json:"devices"`
	Versioning       VersioningConfig `json:"versioning"`
	FSWatcherEnabled bool             `json:"fsWatcherEnabled"`
	// FSWatcherDelayS is the change-detection delay; absent keeps
	// Syncthing's default (10 s). The message adapter sets 1 on the team
	// folder, so a message is announced within a second of its write.
	FSWatcherDelayS int `json:"fsWatcherDelayS,omitzero"`
	RescanIntervalS int `json:"rescanIntervalS"`
	// Paused is always sent, false: a folder the project lists again
	// after the sync adapter paused it syncs again.
	Paused bool `json:"paused"`
}

// PausePatch is the one member the sync adapter PATCHes on a folder the
// project no longer lists (PATCH /rest/config/folders/<id>, which leaves
// every other member as it is).
type PausePatch struct {
	Paused bool `json:"paused"`
}

// SetDevice is POST /rest/config/devices: replace-or-add by id.
func (c *API) SetDevice(d DeviceConfig) error {
	return c.Do(http.MethodPost, "/rest/config/devices", nil, d, nil)
}

// RemoveDevice is DELETE /rest/config/devices/<id>: the device leaves the
// configuration and every folder's device list with it.
func (c *API) RemoveDevice(id string) error {
	return c.Do(http.MethodDelete, "/rest/config/devices/"+url.PathEscape(id), nil, nil, nil)
}

// NamePatch is the one member PatchDeviceName sends.
type NamePatch struct {
	Name string `json:"name"`
}

// PatchDeviceName is PATCH /rest/config/devices/<id> with the name alone.
// On the instance's own id it is the name the instance announces to a
// peer it connects to, which is how a joining member carries its proof
// (internal/adapters/syncthing).
func (c *API) PatchDeviceName(id, name string) error {
	return c.Do(http.MethodPatch, "/rest/config/devices/"+url.PathEscape(id), nil, NamePatch{Name: name}, nil)
}

// SetFolder is POST /rest/config/folders: replace-or-add by id.
func (c *API) SetFolder(f FolderConfig) error {
	return c.Do(http.MethodPost, "/rest/config/folders", nil, f, nil)
}

// RemoveFolder is DELETE /rest/config/folders/<id>; Syncthing keeps the
// files.
func (c *API) RemoveFolder(id string) error {
	return c.Do(http.MethodDelete, "/rest/config/folders/"+url.PathEscape(id), nil, nil, nil)
}

// PauseFolder is PATCH /rest/config/folders/<id> with paused alone.
func (c *API) PauseFolder(id string, paused bool) error {
	return c.Do(http.MethodPatch, "/rest/config/folders/"+url.PathEscape(id), nil, PausePatch{Paused: paused}, nil)
}

// A PendingDevice is one device that connected to this instance and was
// not accepted (GET /rest/cluster/pending/devices): when it last tried,
// the name it announced, and its address.
type PendingDevice struct {
	Time    time.Time `json:"time"`
	Name    string    `json:"name"`
	Address string    `json:"address"`
}

// PendingDevices lists the devices waiting for acceptance, by id.
func (c *API) PendingDevices() (map[string]PendingDevice, error) {
	var out map[string]PendingDevice
	if err := c.Do(http.MethodGet, "/rest/cluster/pending/devices", nil, nil, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]PendingDevice{}
	}
	return out, nil
}

// DismissPendingDevice forgets one pending device (DELETE
// /rest/cluster/pending/devices?device=<id>); it is listed again the
// next time it connects.
func (c *API) DismissPendingDevice(id string) error {
	return c.Do(http.MethodDelete, "/rest/cluster/pending/devices", url.Values{"device": {id}}, nil, nil)
}
