// Package sparse works with files whose unwritten extents are holes: it asks
// the filesystem which extents hold data, deallocates extents, copies files
// without allocating their holes, and does the arithmetic of extent sets.
package sparse

import "sort"

// Extent is a byte range whose Start and End are both inclusive, the way the
// storage services report ranges.
type Extent struct {
	Start int64
	End   int64
}

// Merge adds [start,end] to a sorted, disjoint extent set, coalescing it with
// every extent it touches or overlaps.
func Merge(set []Extent, start, end int64) []Extent {
	out := append(append([]Extent(nil), set...), Extent{Start: start, End: end})
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	merged := out[:0]
	for _, e := range out {
		if n := len(merged); n > 0 && e.Start <= merged[n-1].End+1 {
			merged[n-1].End = max(merged[n-1].End, e.End)
			continue
		}
		merged = append(merged, e)
	}
	return append([]Extent(nil), merged...)
}

// Subtract removes [start,end] from a sorted, disjoint extent set.
func Subtract(set []Extent, start, end int64) []Extent {
	var out []Extent
	for _, e := range set {
		if e.End < start || e.Start > end {
			out = append(out, e)
			continue
		}
		if e.Start < start {
			out = append(out, Extent{Start: e.Start, End: start - 1})
		}
		if e.End > end {
			out = append(out, Extent{Start: end + 1, End: e.End})
		}
	}
	return out
}

// Clip narrows an extent set to the window [start,end].
func Clip(set []Extent, start, end int64) []Extent {
	var out []Extent
	for _, e := range set {
		lo, hi := max(e.Start, start), min(e.End, end)
		if lo <= hi {
			out = append(out, Extent{Start: lo, End: hi})
		}
	}
	return out
}

// Diff returns the parts of a that b does not cover.
func Diff(a, b []Extent) []Extent {
	out := append([]Extent(nil), a...)
	for _, e := range b {
		out = Subtract(out, e.Start, e.End)
	}
	return out
}
