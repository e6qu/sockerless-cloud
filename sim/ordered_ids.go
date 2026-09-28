package sim

import (
	"sort"
	"strings"
)

// orderedIDs keeps a store's ids sorted, so the ids under a prefix are a
// binary search and the run after it. Scanning every id and sorting the
// matches, which ListPrefix did before, made a prefix read cost the whole
// store: a DynamoDB query paid for every item of every table.
type orderedIDs struct {
	ids []string
}

func (o *orderedIDs) add(id string) {
	i := sort.SearchStrings(o.ids, id)
	if i < len(o.ids) && o.ids[i] == id {
		return
	}
	o.ids = append(o.ids, "")
	copy(o.ids[i+1:], o.ids[i:])
	o.ids[i] = id
}

func (o *orderedIDs) remove(id string) {
	i := sort.SearchStrings(o.ids, id)
	if i < len(o.ids) && o.ids[i] == id {
		o.ids = append(o.ids[:i], o.ids[i+1:]...)
	}
}

// withPrefix returns the ids beginning with prefix, in order. The slice is the
// store's own; callers hold the store's lock while they read it.
func (o *orderedIDs) withPrefix(prefix string) []string {
	start := sort.SearchStrings(o.ids, prefix)
	end := start
	for end < len(o.ids) && strings.HasPrefix(o.ids[end], prefix) {
		end++
	}
	return o.ids[start:end]
}

func orderedIDsOf[T any](items map[string]T) orderedIDs {
	ids := make([]string, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return orderedIDs{ids: ids}
}
