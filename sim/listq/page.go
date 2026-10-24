// Package listq holds the cloud-neutral plumbing behind list operations:
// offset and key-cursor pagination, a filter expression tree evaluated over a
// resource's JSON form, field lookup, and ordering. Each cloud keeps its own filter grammar,
// page-token encoding and error shape and maps ErrBadToken and parse errors to
// the error its API returns.
package listq

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ErrBadToken reports a page token the list operation never issued.
var ErrBadToken = errors.New("malformed page token")

// Tokens is how a list spells the item offset a page token carries.
type Tokens struct {
	Encode func(offset int) string
	Decode func(token string) (int, bool)
	// Strict rejects an offset past the end of the list. Otherwise such a
	// token, issued before the list shrank, yields an empty last page.
	Strict bool
}

// Strictly is t with Strict set.
func (t Tokens) Strictly() Tokens {
	t.Strict = true
	return t
}

// Decimal spells the offset as a base-10 integer.
var Decimal = Tokens{
	Encode: strconv.Itoa,
	Decode: func(token string) (int, bool) {
		n, err := strconv.Atoi(token)
		return n, err == nil
	},
}

// Base64Decimal spells the offset as a base-10 integer wrapped in standard
// base64.
var Base64Decimal = Tokens{
	Encode: func(offset int) string {
		return base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
	},
	Decode: func(token string) (int, bool) {
		raw, err := base64.StdEncoding.DecodeString(token)
		if err != nil {
			return 0, false
		}
		n, err := strconv.Atoi(string(raw))
		return n, err == nil
	},
}

// Window resolves one page over a list of total items. An empty token starts
// at the first item; any other token must decode to a non-negative offset or
// the call answers ErrBadToken. A size of zero or less takes def, a def of zero
// or less returns every remaining item, and max, when positive, caps the page.
func Window(t Tokens, total int, token string, size, def, max int) (start, end int, next string, err error) {
	if token != "" {
		n, ok := t.Decode(token)
		if !ok || n < 0 || t.Strict && n > total {
			return 0, 0, "", ErrBadToken
		}
		start = min(n, total)
	}
	limit := size
	if limit <= 0 {
		limit = def
	}
	if max > 0 && (limit <= 0 || limit > max) {
		limit = max
	}
	end = total
	if limit > 0 && start+limit < total {
		end = start + limit
		next = t.Encode(end)
	}
	return start, end, next, nil
}

// OffsetPage slices items by a lenient Decimal token; see Window for how
// token, size, def and max combine. The page is never nil.
func OffsetPage[T any](items []T, token string, size, def, max int) (page []T, next string, err error) {
	return TokenPage(Decimal, items, token, size, def, max)
}

// TokenPage is OffsetPage with the token spelled and checked by t.
func TokenPage[T any](t Tokens, items []T, token string, size, def, max int) (page []T, next string, err error) {
	start, end, next, err := Window(t, len(items), token, size, def, max)
	if err != nil {
		return nil, "", err
	}
	page = items[start:end]
	if page == nil {
		page = []T{}
	}
	return page, next, nil
}

// KeyPage pages items in ascending order of key, which names each item
// uniquely: ColumnPage with key as its one column.
func KeyPage[T any](items []T, key func(T) string, token string, size, def, max int) (page []T, next string, err error) {
	return ColumnPage(items, []Column{{Value: func(i int) string { return key(items[i]) }}}, token, size, def, max)
}

// Column is one term of a list's order: the value item i sorts by, how two
// values compare (as text when Compare is nil), and whether the term runs
// descending.
type Column struct {
	Value   func(i int) string
	Compare func(a, b string) int
	Desc    bool
}

func (c Column) compare(a, b string) int {
	order := strings.Compare(a, b)
	if c.Compare != nil {
		order = c.Compare(a, b)
	}
	if c.Desc {
		return -order
	}
	return order
}

// ColumnPage pages items in the order columns give, the last column naming
// each item uniquely, with a token that carries the sort values of the last
// item a page returned rather than an offset: the next page resumes strictly
// after them, so an item created or deleted between two calls moves no other
// item across a page boundary. An empty token starts at the first item; any
// other token must be one ColumnPage issued for as many columns, or the call
// answers ErrBadToken. size, def and max combine as in Window.
func ColumnPage[T any](items []T, columns []Column, token string, size, def, max int) (page []T, next string, err error) {
	rows := make([][]string, len(items))
	for i := range items {
		rows[i] = make([]string, len(columns))
		for c, column := range columns {
			rows[i][c] = column.Value(i)
		}
	}
	compare := func(a, b []string) int {
		for c, column := range columns {
			if order := column.compare(a[c], b[c]); order != 0 {
				return order
			}
		}
		return 0
	}
	index := make([]int, len(items))
	for i := range index {
		index[i] = i
	}
	sort.SliceStable(index, func(i, j int) bool { return compare(rows[index[i]], rows[index[j]]) < 0 })
	start := 0
	if token != "" {
		after, ok := columnMarker(token, len(columns))
		if !ok {
			return nil, "", ErrBadToken
		}
		start = sort.Search(len(index), func(i int) bool { return compare(rows[index[i]], after) > 0 })
	}
	_, end, _, _ := Window(Decimal, len(index)-start, "", size, def, max)
	end += start
	if end < len(index) && end > start {
		encoded, err := json.Marshal(rows[index[end-1]])
		if err != nil {
			return nil, "", err
		}
		next = base64.RawURLEncoding.EncodeToString(append([]byte(columnTokenTag), encoded...))
	}
	page = make([]T, 0, end-start)
	for _, i := range index[start:end] {
		page = append(page, items[i])
	}
	return page, next, nil
}

// columnTokenTag marks a token as one ColumnPage issued.
const columnTokenTag = "after\x00"

func columnMarker(token string, terms int) ([]string, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, false
	}
	body, ok := strings.CutPrefix(string(decoded), columnTokenTag)
	if !ok {
		return nil, false
	}
	var after []string
	if json.Unmarshal([]byte(body), &after) != nil || len(after) != terms {
		return nil, false
	}
	return after, true
}

// IntKey spells n so that string order matches numeric order, for a key or
// column whose values are integers.
func IntKey(n int64) string {
	return fmt.Sprintf("%020d", uint64(n)^(1<<63))
}
