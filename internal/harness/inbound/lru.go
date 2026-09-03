package inbound

import "container/list"

// An orderedMap is a small insertion-ordered map with a capacity: put
// appends or moves a key to the back (most recent) and evicts the oldest
// key when the capacity is exceeded, so every bounded structure of the
// pipeline — the seen ids, the per-sender states, the identical-body
// deferrals — is one of these and can never grow without limit. It is
// not safe for concurrent use; the Pipeline's mutex guards it.
type orderedMap[K comparable, V any] struct {
	capacity int
	index    map[K]*list.Element
	order    *list.List // front = oldest, back = most recent
}

type entry[K comparable, V any] struct {
	key K
	val V
}

// newOrderedMap builds a map that keeps at most capacity keys (a
// non-positive capacity means one, never unbounded).
func newOrderedMap[K comparable, V any](capacity int) *orderedMap[K, V] {
	if capacity <= 0 {
		capacity = 1
	}
	return &orderedMap[K, V]{capacity: capacity, index: map[K]*list.Element{}, order: list.New()}
}

// get returns the value for key without changing its position.
func (o *orderedMap[K, V]) get(key K) (V, bool) {
	if el, ok := o.index[key]; ok {
		return el.Value.(*entry[K, V]).val, true //nolint:errcheck // the list holds only *entry values
	}
	var zero V
	return zero, false
}

// has reports whether key is present.
func (o *orderedMap[K, V]) has(key K) bool {
	_, ok := o.index[key]
	return ok
}

// put inserts or updates key, makes it the most recent, and evicts the
// oldest key when the map is over capacity; it reports the evicted key.
func (o *orderedMap[K, V]) put(key K, val V) (K, bool) {
	if el, ok := o.index[key]; ok {
		el.Value.(*entry[K, V]).val = val //nolint:errcheck // the list holds only *entry values
		o.order.MoveToBack(el)
		var zero K
		return zero, false
	}
	o.index[key] = o.order.PushBack(&entry[K, V]{key: key, val: val})
	if o.order.Len() > o.capacity {
		oldest, _, _ := o.oldest()
		o.delete(oldest)
		return oldest, true
	}
	var zero K
	return zero, false
}

// delete removes key and reports whether it was present.
func (o *orderedMap[K, V]) delete(key K) bool {
	el, ok := o.index[key]
	if !ok {
		return false
	}
	o.order.Remove(el)
	delete(o.index, key)
	return true
}

// size is the number of keys.
func (o *orderedMap[K, V]) size() int { return o.order.Len() }

// oldest returns the least recently put key.
func (o *orderedMap[K, V]) oldest() (K, V, bool) {
	if el := o.order.Front(); el != nil {
		e := el.Value.(*entry[K, V]) //nolint:errcheck // the list holds only *entry values
		return e.key, e.val, true
	}
	var zk K
	var zv V
	return zk, zv, false
}

// keys lists the keys oldest first.
func (o *orderedMap[K, V]) keys() []K {
	out := make([]K, 0, o.order.Len())
	for el := o.order.Front(); el != nil; el = el.Next() {
		out = append(out, el.Value.(*entry[K, V]).key) //nolint:errcheck // the list holds only *entry values
	}
	return out
}

// pruneOldest removes keys from the oldest end while stale returns true,
// stopping at the first key that is not stale. Because put keeps the
// order by recency, one pass from the front is enough for a time-based
// expiry.
func (o *orderedMap[K, V]) pruneOldest(stale func(K, V) bool) int {
	n := 0
	for {
		el := o.order.Front()
		if el == nil {
			return n
		}
		e := el.Value.(*entry[K, V]) //nolint:errcheck // the list holds only *entry values
		if !stale(e.key, e.val) {
			return n
		}
		o.delete(e.key)
		n++
	}
}
