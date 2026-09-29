package listq

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Doc is a resource in its JSON form, the shape filters and orderings read.
type Doc = map[string]any

// Node is one node of a parsed filter expression.
type Node interface {
	Eval(Doc) bool
}

// True matches every document; it is what an empty filter parses to.
type True struct{}

func (True) Eval(Doc) bool { return true }

type And struct{ L, R Node }

func (n And) Eval(d Doc) bool { return n.L.Eval(d) && n.R.Eval(d) }

type Or struct{ L, R Node }

func (n Or) Eval(d Doc) bool { return n.L.Eval(d) || n.R.Eval(d) }

type Not struct{ Inner Node }

func (n Not) Eval(d Doc) bool { return !n.Inner.Eval(d) }

// Test decides a comparison from the scalar form of the field it names and
// whether the document has that field at all. A cloud grammar builds one per
// operator, since the clouds disagree on how an absent field compares.
type Test func(value string, present bool) bool

// Cmp is a leaf comparison against the field at Path, whose segments Sep
// joins.
type Cmp struct {
	Path, Sep string
	Test      Test
}

func (n Cmp) Eval(d Doc) bool {
	v, ok := Field(d, n.Path, n.Sep)
	return n.Test(v, ok)
}

// Lookup walks path, split on sep, through nested objects.
func Lookup(d Doc, path, sep string) (any, bool) {
	var cur any = d
	for _, seg := range strings.Split(path, sep) {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[seg]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// Field is Lookup rendered through ScalarString.
func Field(d Doc, path, sep string) (string, bool) {
	v, ok := Lookup(d, path, sep)
	if !ok {
		return "", false
	}
	return ScalarString(v), true
}

// ScalarString renders a decoded JSON value as the text a filter literal is
// compared with: strings verbatim, numbers without exponent or trailing zeros,
// and composite values as their JSON.
func ScalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			panic("listq: a decoded JSON value failed to encode: " + err.Error())
		}
		return string(b)
	}
}

// CompareNumeric compares a and b as numbers, reporting false when either is
// not one.
func CompareNumeric(a, b string) (int, bool) {
	af, aerr := strconv.ParseFloat(a, 64)
	bf, berr := strconv.ParseFloat(b, 64)
	if aerr != nil || berr != nil {
		return 0, false
	}
	switch {
	case af < bf:
		return -1, true
	case af > bf:
		return 1, true
	}
	return 0, true
}

// CompareOrdered compares numerically when both sides are numbers and
// lexically otherwise, the order both Google Cloud and Azure filters apply to
// their relational operators.
func CompareOrdered(a, b string) int {
	if c, ok := CompareNumeric(a, b); ok {
		return c
	}
	return strings.Compare(a, b)
}

// ToDoc renders v through its JSON encoding.
func ToDoc[T any](v T) (Doc, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("render list item: %w", err)
	}
	var d Doc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("render list item: %w", err)
	}
	return d, nil
}

// OrderKey is one ordering term: the field at Path, descending when Desc.
type OrderKey struct {
	Path string
	Desc bool
}

// ParseOrderBy reads a comma-separated list of "field [asc|desc]" terms. A
// field path never holds whitespace, so a term splits on whitespace; foldCase
// admits the direction keywords in any case. A term with more than two words,
// an unknown direction or an empty field is an error.
func ParseOrderBy(s string, foldCase bool) ([]OrderKey, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var keys []OrderKey
	for _, term := range strings.Split(s, ",") {
		parts := strings.Fields(term)
		switch len(parts) {
		case 1:
			keys = append(keys, OrderKey{Path: parts[0]})
		case 2:
			dir := parts[1]
			if foldCase {
				dir = strings.ToLower(dir)
			}
			switch dir {
			case "asc":
				keys = append(keys, OrderKey{Path: parts[0]})
			case "desc":
				keys = append(keys, OrderKey{Path: parts[0], Desc: true})
			default:
				return nil, fmt.Errorf("invalid order direction %q in %q", parts[1], s)
			}
		default:
			return nil, fmt.Errorf("invalid order term %q", strings.TrimSpace(term))
		}
	}
	return keys, nil
}

// ApplyList keeps the items filter matches, in the order the keys give, each
// key read from the path its sep splits; items the keys cannot tell apart keep
// their input order. A nil filter matches everything.
func ApplyList[T any](items []T, filter Node, order []OrderKey, sep string) ([]T, error) {
	if filter == nil && len(order) == 0 {
		return items, nil
	}
	type row struct {
		item T
		doc  Doc
	}
	rows := make([]row, 0, len(items))
	for _, it := range items {
		d, err := ToDoc(it)
		if err != nil {
			return nil, err
		}
		if filter != nil && !filter.Eval(d) {
			continue
		}
		rows = append(rows, row{it, d})
	}
	if len(order) > 0 {
		sort.SliceStable(rows, func(i, j int) bool {
			for _, k := range order {
				a, _ := Field(rows[i].doc, k.Path, sep)
				b, _ := Field(rows[j].doc, k.Path, sep)
				c := CompareOrdered(a, b)
				if c == 0 {
					continue
				}
				if k.Desc {
					return c > 0
				}
				return c < 0
			}
			return false
		})
	}
	out := make([]T, len(rows))
	for i, r := range rows {
		out[i] = r.item
	}
	return out, nil
}
