package write_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/protocol"
)

func pin() teamstore.Pin {
	return teamstore.Pin{Adapter: "supabase", URL: "https://abc.supabase.co",
		PublishableKey: "sb_publishable_x", TeamRef: "t_4f9c",
		ConsentedAt: time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)}
}

func TestPinRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := write.Pin(dir, "/checkout/payments", pin()); err != nil {
		t.Fatal(err)
	}
	got, ok, err := teamstore.LookupPin(dir, "/checkout/payments")
	if err != nil || !ok {
		t.Fatalf("lookup after write: %v, %v", ok, err)
	}
	if got.TeamRef != "t_4f9c" || !got.ConsentedAt.Equal(pin().ConsentedAt) {
		t.Fatalf("pin round-trip lost members: %+v", got)
	}
	fi, err := os.Lstat(teamstore.PinsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("projects.json mode = %o, want 0600", fi.Mode().Perm())
	}
	// A second checkout pins beside the first, not over it.
	if err := write.Pin(dir, "/checkout/other", pin()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := teamstore.LookupPin(dir, "/checkout/payments"); !ok {
		t.Fatal("the first pin vanished when the second was written")
	}
}

func TestPinWaitsForTheFlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lock, err := adapterkit.LockFile(teamstore.PinsPath(dir)+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = lock.Unlock()
		close(released)
	}()
	if err := write.Pin(dir, "/checkout/x", pin()); err != nil {
		t.Fatalf("Pin must wait for the holder, not fail: %v", err)
	}
	<-released
	if _, ok, _ := teamstore.LookupPin(dir, "/checkout/x"); !ok {
		t.Fatal("the pin written under contention is missing")
	}
}

func TestPinRefusesToRewriteAMalformedStore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := adapterkit.WriteAtomic(teamstore.PinsPath(dir), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	err := write.Pin(dir, "/x", pin())
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Details["reason"] != "pins_malformed" {
		t.Fatalf("a store that cannot be read back must not be rewritten blind: %v", err)
	}
}

// TestSymlinkedCheckoutAgreement is the library half of review low fix
// 7: a pin written under the canonicalization of the real path is found
// under the canonicalization of a symlinked path to the same checkout.
func TestSymlinkedCheckoutAgreement(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	//nolint:gosec // G301: an ordinary checkout directory
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	cfg := t.TempDir()
	canonReal, err := teamfile.Canonicalize(realDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := write.Pin(cfg, canonReal, pin()); err != nil {
		t.Fatal(err)
	}
	canonLink, err := teamfile.Canonicalize(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := teamstore.LookupPin(cfg, canonLink); !ok {
		t.Fatalf("pin written via %q not found via %q — the one shared canonicalization is broken", canonReal, canonLink)
	}
}

func TestTempKeyLifecycle(t *testing.T) {
	t.Parallel()
	tmp, err := write.TempKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tmp, "tmp-") {
		t.Fatalf("temp key %q lacks the visible tmp- prefix", tmp)
	}
	if err := adapterkit.CheckProfileName(tmp); err != nil {
		t.Fatalf("the temp key must drive the adapter's --profile: %v", err)
	}

	dir := t.TempDir()
	p := &adapterkit.Profile{Version: adapterkit.ProfileVersion, Adapter: "supabase"}
	if err := adapterkit.SaveProfile(dir, tmp, p); err != nil {
		t.Fatal(err)
	}
	key := teamstore.Key("supabase", "https://abc.supabase.co", "t_4f9c")
	if err := write.Promote(dir, tmp, key); err != nil {
		t.Fatal(err)
	}
	if _, err := teamstore.LoadBinding(dir, key); err != nil {
		t.Fatalf("the promoted binding must load under the real key: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "teams", tmp)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the temp directory survived its promotion")
	}
}

func TestPromoteRefusesAnExistingDestination(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := teamstore.Key("supabase", "https://abc.supabase.co", "t_4f9c")
	p := &adapterkit.Profile{Version: adapterkit.ProfileVersion, Adapter: "supabase"}
	if err := adapterkit.SaveProfile(dir, key, p); err != nil {
		t.Fatal(err)
	}
	tmp, err := write.TempKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.SaveProfile(dir, tmp, p); err != nil {
		t.Fatal(err)
	}
	perr := write.Promote(dir, tmp, key)
	var pe *protocol.Error
	if !errors.As(perr, &pe) || pe.Details["reason"] != "team_dir_exists" {
		t.Fatalf("promoting onto an existing credential dir must refuse loudly: %v", perr)
	}
}

func TestPruneOrphans(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := &adapterkit.Profile{Version: adapterkit.ProfileVersion, Adapter: "supabase"}
	old, err := write.TempKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.SaveProfile(dir, old, p); err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(dir, "teams", old)
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldDir, past, past); err != nil {
		t.Fatal(err)
	}
	fresh, err := write.TempKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.SaveProfile(dir, fresh, p); err != nil {
		t.Fatal(err)
	}
	key := teamstore.Key("supabase", "https://x.co", "t_keep")
	if err := adapterkit.SaveProfile(dir, key, p); err != nil {
		t.Fatal(err)
	}

	removed, err := write.PruneOrphans(dir, 24*time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != oldDir {
		t.Fatalf("removed = %v, want exactly the stale %q", removed, oldDir)
	}
	if _, err := os.Lstat(filepath.Join(dir, "teams", fresh)); err != nil {
		t.Fatal("a fresh temp dir (a create racing the prune) was removed")
	}
	if _, err := os.Lstat(filepath.Join(dir, "teams", key)); err != nil {
		t.Fatal("a real credential dir was removed by the prune")
	}
}

// TestPatchSurvivesAdapterRewrite is verify-round high fix 1's two-writer
// ownership test: the harness patches its three members, the adapter then
// load-modify-saves its own, and neither writer loses the other's work.
func TestPatchSurvivesAdapterRewrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := teamstore.Key("fs", "http://127.0.0.1:1", "t_dev")
	p := &adapterkit.Profile{Version: adapterkit.ProfileVersion, Adapter: "fs",
		TeamRef: "t_dev", PrincipalRef: "p_1"}
	if err := adapterkit.SaveProfile(dir, key, p); err != nil {
		t.Fatal(err)
	}
	if err := write.PatchBindingBackend(dir, key, "fs", "http://127.0.0.1:1", "placeholder"); err != nil {
		t.Fatal(err)
	}
	// The adapter's own rewrite: load, change an adapter-owned member, save.
	loaded, err := adapterkit.LoadProfile(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	loaded.TeamName = "dev"
	if err := adapterkit.SaveProfile(dir, key, loaded); err != nil {
		t.Fatal(err)
	}
	final, err := teamstore.LoadBinding(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if final.URL != "http://127.0.0.1:1" || final.PublishableKey != "placeholder" {
		t.Fatalf("the adapter's rewrite lost the harness's patch: %+v", final)
	}
	if final.TeamName != "dev" || final.PrincipalRef != "p_1" {
		t.Fatalf("the patch lost the adapter's members: %+v", final)
	}
}
