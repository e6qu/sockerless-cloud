package sim

import "sync"

// GenerationIndex answers a lookup from a store without reading all of it.
//
// The simulators hold their resources in stores whose rows are JSON, so
// `List()` decodes every row it returns. That is the right cost for a List API
// and the wrong cost for finding one row, and the difference is invisible until
// the path is hot: a CPU profile of the deployed AWS simulator, taken under
// twelve concurrent requests, put 84.8% of all its CPU in one such lookup —
// almost entirely decoding stopped Amazon ECS tasks nobody had asked about —
// and the guest's two vCPUs then capped the whole data plane at a concurrency
// of two.
//
// The worst of them are the lookups a handler wrapper makes to decide whether
// to claim a request at all, because those run for every request in the
// process whether or not the feature is in use: a load balancer's hostname
// match was scanning its store ahead of every Amazon DynamoDB call.
//
// An index built once and invalidated by hand would be a second source of
// truth and a lifecycle to get wrong. This one is keyed by the store's
// Generation instead: two reads that observe the same generation observed the
// same contents, so a resource that is deleted, renamed or replaced leaves the
// index on the next lookup with nothing to call and nothing to remember.
// Generations are unique across every store in the process, so replacing a
// store — which tests do between cases — cannot present a generation an index
// already holds.
type GenerationIndex[T any] struct {
	mu         sync.RWMutex
	generation uint64
	built      bool
	byKey      map[string][]T
}

// Lookup returns the first row indexed under key. keysOf maps a row to the keys
// it answers on, and is called only when the index is rebuilt.
func (i *GenerationIndex[T]) Lookup(store Store[T], key string, keysOf func(T) []string) (T, bool) {
	rows := i.LookupAll(store, key, keysOf)
	if len(rows) == 0 {
		var zero T
		return zero, false
	}
	return rows[0], true
}

// LookupAll returns every row indexed under key, in the order the store listed
// them. Callers that can legitimately have more than one row on a key — two
// virtual networks sharing a CIDR, say — need all of them.
func (i *GenerationIndex[T]) LookupAll(store Store[T], key string, keysOf func(T) []string) []T {
	// The generation is read before the contents, so a write landing during a
	// rebuild leaves the index recorded at the older generation and the next
	// caller rebuilds rather than trusting a partial view.
	generation := store.Generation()

	i.mu.RLock()
	if i.built && i.generation == generation {
		rows := i.byKey[key]
		i.mu.RUnlock()
		return rows
	}
	i.mu.RUnlock()

	i.mu.Lock()
	defer i.mu.Unlock()
	// Another caller may have rebuilt while this one waited for the write
	// lock. Rebuilding again would be correct but is the expense this exists
	// to avoid.
	if !i.built || i.generation != generation {
		byKey := map[string][]T{}
		for _, row := range store.List() {
			for _, k := range keysOf(row) {
				if k == "" {
					continue
				}
				byKey[k] = append(byKey[k], row)
			}
		}
		i.byKey = byKey
		i.generation = generation
		i.built = true
	}
	return i.byKey[key]
}

// PathPrefixes returns every prefix of a "/"-separated identifier that ends at
// a separator.
//
// It is the keysOf for a store whose rows are addressed by a parent's
// identifier plus a child segment. A row indexed under all of its prefixes
// answers any `HasPrefix(id, parent+"/")` question exactly, at every depth, so
// one index serves a direct child collection and a cascading delete alike,
// without the index having to know which depth a caller will ask about.
func PathPrefixes(id string) []string {
	var prefixes []string
	for i := range len(id) {
		if id[i] == '/' {
			prefixes = append(prefixes, id[:i+1])
		}
	}
	return prefixes
}
