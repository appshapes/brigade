package usage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestShippedPrices: the table prices a Claude model as the pricing page
// gives it, whatever suffix Claude Code puts on the id, and has no price
// for a model it cannot cite.
func TestShippedPrices(t *testing.T) {
	p, err := LoadPrices("")
	if err != nil {
		t.Fatal(err)
	}
	opus, ok := p.For("claude-opus-5-5[1m]")
	if !ok || opus.Input != 4 || opus.Output != 20 || opus.CacheRead != 0.20 || opus.CacheWrite != 5 || opus.Source == "" || opus.AsOf == "" {
		t.Fatalf("opus: %+v %v", opus, ok)
	}
	// One million of each kind of token: $4 + $20 + $0.20 + $5.
	if got := opus.Cost(Counts{Input: 1e6, Output: 1e6, CacheRead: 1e6, CacheWrite: 1e6}); math.Abs(got-29.20) > 1e-9 {
		t.Fatalf("cost %v", got)
	}
	if _, ok := p.For("gpt-6.2-astra"); ok {
		t.Fatal("a price for a model Brigade cannot cite")
	}
}

// TestLocalPrices: the member's file adds a price and changes one; a file
// that is not usable leaves the shipped prices, and says so.
func TestLocalPrices(t *testing.T) {
	dir := t.TempDir()
	body := `{"models":{"GPT-6.2-Astra":{"input":1.25,"output":10,"cache_read":0.125,"cache_write":0},"claude-opus-5-5":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75,"source":"our contract"}}}`
	if err := os.WriteFile(filepath.Join(dir, PricesFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPrices(dir)
	if err != nil {
		t.Fatal(err)
	}
	if g, ok := p.For("gpt-6.2-astra"); !ok || g.Output != 10 || g.Source != PricesFile {
		t.Fatalf("added: %+v %v", g, ok)
	}
	if o, _ := p.For("claude-opus-5-5"); o.Input != 3 || o.Source != "our contract" {
		t.Fatalf("changed: %+v", o)
	}
	for _, bad := range []string{"not json", `{"models":{"x":{"input":-1}}}`} {
		if err := os.WriteFile(filepath.Join(dir, PricesFile), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		p, err := LoadPrices(dir)
		if err == nil {
			t.Fatalf("%q was taken", bad)
		}
		if _, ok := p.For("claude-opus-5-5"); !ok {
			t.Fatalf("%q lost the shipped prices", bad)
		}
	}
}

// TestSinceAndItemUsage: what was spent between two readings, a harness
// that started again counted from zero, and a work item's sums priced
// where the model has a price.
func TestSinceAndItemUsage(t *testing.T) {
	prev := Counts{Input: 100, Output: 10, CacheRead: 1000}
	if got := Since(prev, Counts{Input: 150, Output: 30, CacheRead: 1500, CacheWrite: 5}); got != (Counts{Input: 50, Output: 20, CacheRead: 500, CacheWrite: 5}) {
		t.Fatalf("since: %+v", got)
	}
	if got := Since(prev, Counts{Input: 7, Output: 3}); got != (Counts{Input: 7, Output: 3}) {
		t.Fatalf("after a reset: %+v", got)
	}
	p, _ := LoadPrices("")
	var u ItemUsage
	u.Add(349, "claude-opus-5-5", Counts{Input: 1e6}, p)
	u.Add(349, "gpt-6.2-astra", Counts{Output: 1e6}, p)
	if u.Number != 349 || u.Tokens != (Counts{Input: 1e6, Output: 1e6}) || u.CostUSD != 4 || u.Priced != 1 || u.Unpriced != 1 || u.Model != "gpt-6.2-astra" {
		t.Fatalf("item usage: %+v", u)
	}
}
