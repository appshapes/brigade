// Package syncthingtest is an httptest stand-in for Syncthing's REST API:
// the endpoints the two adapters call, keyed by X-API-Key, keeping what
// they post. It is a test helper and nothing ships it.
package syncthingtest

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/appshapes/brigade/internal/syncthing"
)

// Key is the API key the fixtures write into config.xml. Every test that
// runs a verb asserts it never reaches stdout or stderr.
const Key = "fixture-api-key-7f3a"

// ID is the device id the fake answers as myID.
const ID = "SELFSEL-FSELFSE-LFSELFS-ELFSELF-SELFSEL-FSELFSE-LFSELFS-ELFSELF"

// A Fake is the server's state. A test reads and writes it under Mu.
type Fake struct {
	T *testing.T

	Mu          sync.Mutex
	Devices     []syncthing.DeviceConfig
	DevicePosts int
	Folders     []syncthing.FolderConfig
	// FolderPosts counts POST /rest/config/folders.
	FolderPosts int
	// Pauses counts the PATCHes that paused a folder.
	Pauses int
	// Deletes is the folder ids DELETEd, in order.
	Deletes []string
	// FailDelete makes a folder DELETE answer 500.
	FailDelete bool
	// Extra is the folders "another project" holds: listed, never posted.
	Extra     []syncthing.ConfiguredFolder
	Connected map[string]bool
	// Listen is options.listenAddresses; ListenSets counts its PATCHes.
	Listen     []string
	ListenSets int
	Shutdowns  int
	FailStop   bool
	OnShutdown func()
	BadKey     int
}

// New starts the fake and closes it with the test.
func New(t *testing.T) (*Fake, *httptest.Server) {
	t.Helper()
	f := &Fake{T: t, Connected: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

// FolderDevices answers the device ids of one configured folder, or nil
// when it is not configured.
func (f *Fake) FolderDevices(id string) []string {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	for _, c := range f.Folders {
		if c.ID == id {
			out := make([]string, 0, len(c.Devices))
			for _, d := range c.Devices {
				out = append(out, d.DeviceID)
			}
			return out
		}
	}
	return nil
}

// DeviceIDs answers the configured device ids, the instance's own first.
func (f *Fake) DeviceIDs() []string {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	out := []string{ID}
	for _, d := range f.Devices {
		out = append(out, d.DeviceID)
	}
	return out
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if r.Header.Get("X-API-Key") != Key {
		f.BadKey++
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	reply := func(v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			f.T.Errorf("fake API: marshal: %v", err)
		}
		_, _ = w.Write(raw)
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /rest/system/status":
		reply(map[string]string{"myID": ID})
	case "POST /rest/system/shutdown":
		f.Shutdowns++
		if f.FailStop {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		if f.OnShutdown != nil {
			f.OnShutdown()
		}
		reply(map[string]string{"ok": "shutting down"})
	case "GET /rest/system/connections":
		conns := map[string]map[string]bool{}
		for _, d := range f.Devices {
			conns[d.DeviceID] = map[string]bool{"connected": f.Connected[d.DeviceID]}
		}
		reply(map[string]any{"connections": conns})
	case "GET /rest/config/devices":
		out := []syncthing.DeviceConfig{{DeviceID: ID, Name: "self"}}
		reply(append(out, f.Devices...))
	case "POST /rest/config/devices":
		var d syncthing.DeviceConfig
		if err := json.UnmarshalRead(r.Body, &d); err != nil || strings.HasPrefix(d.DeviceID, "BAD") {
			http.Error(w, "invalid device", http.StatusBadRequest)
			return
		}
		f.DevicePosts++
		f.Devices = slices.DeleteFunc(f.Devices, func(e syncthing.DeviceConfig) bool { return e.DeviceID == d.DeviceID })
		f.Devices = append(f.Devices, d)
	case "GET /rest/config/folders":
		out := slices.Clone(f.Extra)
		for _, c := range f.Folders {
			out = append(out, syncthing.ConfiguredFolder{ID: c.ID, Label: c.Label, Path: c.Path, Paused: c.Paused, Devices: c.Devices})
		}
		reply(out)
	case "POST /rest/config/folders":
		var c syncthing.FolderConfig
		if err := json.UnmarshalRead(r.Body, &c); err != nil {
			http.Error(w, "invalid folder", http.StatusBadRequest)
			return
		}
		f.FolderPosts++
		f.Folders = slices.DeleteFunc(f.Folders, func(e syncthing.FolderConfig) bool { return e.ID == c.ID })
		f.Folders = append(f.Folders, c)
	case "GET /rest/config/options":
		// Syncthing's whole options object; the engine reads one member.
		reply(map[string]any{"listenAddresses": f.Listen, "globalAnnounceEnabled": true, "relaysEnabled": true})
	case "PATCH /rest/config/options":
		var o map[string]any
		if err := json.UnmarshalRead(r.Body, &o); err != nil || len(o) != 1 {
			http.Error(w, "the patch must name listenAddresses alone", http.StatusBadRequest)
			return
		}
		var lo syncthing.ListenOptions
		raw, _ := json.Marshal(o)
		if err := json.Unmarshal(raw, &lo); err != nil || lo.ListenAddresses == nil {
			http.Error(w, "invalid options", http.StatusBadRequest)
			return
		}
		f.Listen = lo.ListenAddresses
		f.ListenSets++
	case "GET /rest/db/status":
		id := r.URL.Query().Get("folder")
		for _, c := range f.Folders {
			if c.ID == id {
				reply(map[string]string{"state": "idle"})
				return
			}
		}
		http.Error(w, "no such folder", http.StatusNotFound)
	default:
		f.serveByID(w, r, reply)
	}
}

// serveByID handles the /rest/config/{folders,devices}/<id> routes.
func (f *Fake) serveByID(w http.ResponseWriter, r *http.Request, _ func(any)) {
	id, ok := strings.CutPrefix(r.URL.Path, "/rest/config/folders/")
	if ok && r.Method == http.MethodDelete {
		if f.FailDelete {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		// Syncthing 2.1.5 answers 200 whether or not the id was
		// configured (measured 2026-09-28).
		f.Deletes = append(f.Deletes, id)
		f.Folders = slices.DeleteFunc(f.Folders, func(e syncthing.FolderConfig) bool { return e.ID == id })
		return
	}
	if r.Method != http.MethodPatch || !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var patch map[string]any
	if err := json.UnmarshalRead(r.Body, &patch); err != nil || len(patch) != 1 || patch["paused"] != true {
		http.Error(w, "the patch must set paused alone", http.StatusBadRequest)
		return
	}
	for i := range f.Folders {
		if f.Folders[i].ID == id {
			f.Folders[i].Paused = true
			f.Pauses++
			return
		}
	}
	http.Error(w, "no such folder", http.StatusNotFound)
}
