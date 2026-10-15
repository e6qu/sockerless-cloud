// Package listq holds the cloud-neutral plumbing behind list operations:
// offset and key-cursor pagination, a filter expression tree evaluated over a
// resource's JSON form, field lookup, and ordering. Each cloud keeps its own filter grammar,
// page-token encoding and error shape and maps ErrBadToken and parse errors to
// the error its API returns.
package listq

import (
	"encoding/base64"
	"errors"
	"sort"
	"strconv"
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

// KeyPage pages items, sorted ascending by key, with a token that names the
// last key a page returned rather than an offset, so an item created or
// deleted before that key between two calls neither repeats nor skips one
// after it. An empty token starts at the first item; any other token must be
// one KeyPage issued. size, def and max combine as in Window.
//
// The token carries a marker byte ahead of the key, so a token another
// paginator issued, or one a caller made up, fails as ErrBadToken instead of
// reading as a key.
func KeyPage[T any](items []T, key func(T) string, token string, size, def, max int) (page []T, next string, err error) {
	start := 0
	if token != "" {
		raw, decodeErr := base64.RawURLEncoding.DecodeString(token)
		if decodeErr != nil || len(raw) < 2 || raw[0] != keyTokenMarker {
			return nil, "", ErrBadToken
		}
		after := string(raw[1:])
		start = sort.Search(len(items), func(i int) bool { return key(items[i]) > after })
	}
	_, end, _, _ := Window(Decimal, len(items)-start, "", size, def, max)
	page = items[start : start+end]
	if start+end < len(items) && len(page) > 0 {
		next = base64.RawURLEncoding.EncodeToString(append([]byte{keyTokenMarker}, key(page[len(page)-1])...))
	}
	if page == nil {
		page = []T{}
	}
	return page, next, nil
}

const keyTokenMarker = 0x6b
