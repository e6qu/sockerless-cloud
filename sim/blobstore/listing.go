package blobstore

import "strings"

// Entry is one line of a delimited listing: an item, or a common prefix that
// stands for every item whose name continues past the listing's prefix to the
// delimiter.
type Entry[T any] struct {
	// Key is the item's name, or the common prefix.
	Key    string
	Prefix bool
	// Item is the item an item entry lists; a prefix entry has none.
	Item T
	// Cursor is where the entry sits in the listing: the item's own
	// position, or the common prefix.
	Cursor string
	// last is the position of the last item a common prefix stands for.
	last string
}

// RollUp lists items, which are in listing order and all named under prefix,
// rolling every name that continues past prefix to delimiter into one common
// prefix entry. cursor gives each item a position unique within the listing,
// for services that list more than one item under a name; nil uses the name.
// Without a delimiter every item is listed.
func RollUp[T any](items []T, name, cursor func(T) string, prefix, delimiter string) []Entry[T] {
	if cursor == nil {
		cursor = name
	}
	entries := make([]Entry[T], 0, len(items))
	for _, item := range items {
		key := name(item)
		if delimiter != "" {
			if at := strings.Index(strings.TrimPrefix(key, prefix), delimiter); at >= 0 {
				common := key[:len(prefix)+at+len(delimiter)]
				if n := len(entries); n > 0 && entries[n-1].Prefix && entries[n-1].Key == common {
					entries[n-1].last = cursor(item)
					continue
				}
				entries = append(entries, Entry[T]{Key: common, Prefix: true, Cursor: common, last: cursor(item)})
				continue
			}
		}
		position := cursor(item)
		entries = append(entries, Entry[T]{Key: key, Item: item, Cursor: position, last: position})
	}
	return entries
}

// PageAfter returns the entries that follow marker, at most limit of them
// (a negative limit is none), whether more follow, and the cursor to resume
// from, which is the last entry's. A common prefix follows marker when an item
// it stands for does, unless marker is the prefix itself: a listing resumed
// from a common prefix resumes past everything under it.
func PageAfter[T any](entries []Entry[T], marker string, limit int) (page []Entry[T], truncated bool, next string) {
	start := 0
	if marker != "" {
		for start < len(entries) {
			e := entries[start]
			if e.last > marker && (!e.Prefix || e.Key != marker) {
				break
			}
			start++
		}
	}
	page = entries[start:]
	if limit >= 0 && len(page) > limit {
		page, truncated = page[:limit], true
		if limit > 0 {
			next = page[limit-1].Cursor
		}
	}
	return page, truncated, next
}
