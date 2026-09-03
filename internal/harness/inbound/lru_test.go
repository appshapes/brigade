package inbound

import (
	"strings"
	"testing"
)

func TestOrderedMapPutGetDeleteOrder(t *testing.T) {
	t.Parallel()
	m := newOrderedMap[string, int](3)
	if m.size() != 0 {
		t.Fatalf("size %d", m.size())
	}
	if _, _, ok := m.oldest(); ok {
		t.Fatal("oldest on empty")
	}
	for i, k := range []string{"a", "b", "c"} {
		if ev, evicted := m.put(k, i); evicted {
			t.Fatalf("put %s evicted %q", k, ev)
		}
	}
	if v, ok := m.get("b"); !ok || v != 1 {
		t.Fatalf("get b = %d %v", v, ok)
	}
	if !m.has("a") || m.has("z") {
		t.Fatal("has")
	}
	if got := strings.Join(m.keys(), ""); got != "abc" {
		t.Fatalf("keys %q", got)
	}
	// Updating an existing key moves it to the back without eviction.
	if _, evicted := m.put("a", 10); evicted {
		t.Fatal("update evicted")
	}
	if got := strings.Join(m.keys(), ""); got != "bca" {
		t.Fatalf("keys after update %q", got)
	}
	if v, _ := m.get("a"); v != 10 {
		t.Fatalf("updated value %d", v)
	}
	// Over capacity: the oldest (b) goes.
	ev, evicted := m.put("d", 3)
	if !evicted || ev != "b" {
		t.Fatalf("evicted %q %v, want b", ev, evicted)
	}
	if got := strings.Join(m.keys(), ""); got != "cad" || m.size() != 3 {
		t.Fatalf("keys %q size %d", got, m.size())
	}
	if k, v, ok := m.oldest(); !ok || k != "c" || v != 2 {
		t.Fatalf("oldest %s %d %v", k, v, ok)
	}
	if !m.delete("a") || m.delete("a") {
		t.Fatal("delete")
	}
	if got := strings.Join(m.keys(), ""); got != "cd" {
		t.Fatalf("keys after delete %q", got)
	}
}

func TestOrderedMapPruneOldest(t *testing.T) {
	t.Parallel()
	m := newOrderedMap[string, int](10)
	for i, k := range []string{"a", "b", "c", "d"} {
		m.put(k, i)
	}
	// Stale = value < 2: prunes a and b, stops at c even though d would
	// also be tested stale if reached (it is not, by design: one pass from
	// the front, and recency order makes that sufficient).
	n := m.pruneOldest(func(_ string, v int) bool { return v < 2 })
	if n != 2 || strings.Join(m.keys(), "") != "cd" {
		t.Fatalf("pruned %d, keys %q", n, m.keys())
	}
	if n := m.pruneOldest(func(string, int) bool { return true }); n != 2 || m.size() != 0 {
		t.Fatalf("prune all: %d left %d", n, m.size())
	}
	if n := m.pruneOldest(func(string, int) bool { return true }); n != 0 {
		t.Fatalf("prune empty: %d", n)
	}
}

func TestOrderedMapCapacityFloor(t *testing.T) {
	t.Parallel()
	for _, c := range []int{0, -5} {
		m := newOrderedMap[int, int](c)
		m.put(1, 1)
		if ev, evicted := m.put(2, 2); !evicted || ev != 1 || m.size() != 1 {
			t.Fatalf("capacity %d: evicted %d %v size %d", c, ev, evicted, m.size())
		}
	}
}
