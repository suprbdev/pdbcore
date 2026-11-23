package introspect

import "sync/atomic"

type nameKey struct{ schema, name string }

// sliceIndex is an immutable lookup map over one Catalog slice, stamped with
// the slice's length and end elements so a lookup can tell the slice was
// reshaped since the map was built.
type sliceIndex[K comparable, T any] struct {
	n           int
	first, last *T
	m           map[K]*T
}

func (ix *sliceIndex[K, T]) fresh(s []*T) bool {
	if ix == nil || ix.n != len(s) {
		return false
	}
	return len(s) == 0 || (ix.first == s[0] && ix.last == s[len(s)-1])
}

// lookup returns the first element of s whose key is k. The index lives in
// slot and is rebuilt when s changes length or end elements. Elements that
// are edited in place keep their position, so a hit is re-checked with
// match; a stale miss after an in-place key change needs [Catalog.Reindex].
func lookup[K comparable, T any](slot *atomic.Pointer[sliceIndex[K, T]], s []*T, k K, key func(*T) K) *T {
	ix := slot.Load()
	if !ix.fresh(s) {
		ix = &sliceIndex[K, T]{n: len(s), m: make(map[K]*T, len(s))}
		if len(s) > 0 {
			ix.first, ix.last = s[0], s[len(s)-1]
		}
		for _, e := range s {
			if _, dup := ix.m[key(e)]; !dup {
				ix.m[key(e)] = e
			}
		}
		slot.Store(ix)
	}
	e := ix.m[k]
	if e != nil && key(e) != k {
		slot.Store(nil)
		return lookup(slot, s, k, key)
	}
	return e
}

// catalogIndexes holds the lazily built by-name lookup maps. It is not
// serialized and is rebuilt on demand.
type catalogIndexes struct {
	tables     atomic.Pointer[sliceIndex[nameKey, Table]]
	enums      atomic.Pointer[sliceIndex[nameKey, Enum]]
	enumRefs   atomic.Pointer[sliceIndex[string, Enum]]
	composites atomic.Pointer[sliceIndex[nameKey, Composite]]
	extensions atomic.Pointer[sliceIndex[string, Extension]]
	ranges     atomic.Pointer[sliceIndex[nameKey, RangeType]]
	multis     atomic.Pointer[sliceIndex[nameKey, RangeType]]
	domains    atomic.Pointer[sliceIndex[nameKey, Domain]]
	domainRefs atomic.Pointer[sliceIndex[string, Domain]]
}

// Reindex discards the by-name lookup maps behind [Catalog.Table],
// [Catalog.Enum], [Catalog.Composite] and the other lookups. Lookups
// already rebuild when a slice changes length or its first or last element,
// so a mutation that only reorders, replaces interior elements or renames
// an element in place and then misses a lookup needs this. Call it after
// such edits, before the catalog is shared with concurrent readers.
func (c *Catalog) Reindex() {
	c.idx.tables.Store(nil)
	c.idx.enums.Store(nil)
	c.idx.enumRefs.Store(nil)
	c.idx.composites.Store(nil)
	c.idx.extensions.Store(nil)
	c.idx.ranges.Store(nil)
	c.idx.multis.Store(nil)
	c.idx.domains.Store(nil)
	c.idx.domainRefs.Store(nil)
}
