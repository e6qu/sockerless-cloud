package listq

import (
	"errors"
	"reflect"
	"testing"
)

func TestWindowPaging(t *testing.T) {
	items := []int{0, 1, 2, 3, 4}
	cases := []struct {
		name           string
		token          string
		size, def, max int
		want           []int
		next           string
	}{
		{"all when no size and no default", "", 0, 0, 0, []int{0, 1, 2, 3, 4}, ""},
		{"default applies without size", "", 0, 2, 0, []int{0, 1}, "2"},
		{"size under default wins", "", 1, 3, 3, []int{0}, "1"},
		{"max caps size", "", 10, 0, 3, []int{0, 1, 2}, "3"},
		{"max caps missing size", "", 0, 0, 4, []int{0, 1, 2, 3}, "4"},
		{"token resumes", "3", 2, 0, 0, []int{3, 4}, ""},
		{"offset at end is empty", "5", 2, 0, 0, []int{}, ""},
		{"offset past end is empty", "9", 2, 0, 0, []int{}, ""},
	}
	for _, c := range cases {
		page, next, err := OffsetPage(items, c.token, c.size, c.def, c.max)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(page, c.want) || next != c.next {
			t.Errorf("%s: got %v next %q, want %v next %q", c.name, page, next, c.want, c.next)
		}
	}
}

func TestWindowRejectsTokensItNeverIssued(t *testing.T) {
	for _, tok := range []string{"abc", "-1", "1.5", " 2"} {
		if _, _, err := OffsetPage([]int{1, 2}, tok, 1, 0, 0); !errors.Is(err, ErrBadToken) {
			t.Errorf("token %q: got %v, want ErrBadToken", tok, err)
		}
	}
	if _, _, err := TokenPage(Base64Decimal, []int{1, 2}, "2", 1, 0, 0); !errors.Is(err, ErrBadToken) {
		t.Errorf("a decimal token is not a base64 token: got %v", err)
	}
}

func TestStrictTokensRejectOffsetsPastTheEnd(t *testing.T) {
	items := []int{1, 2}
	if _, _, err := TokenPage(Decimal.Strictly(), items, "3", 1, 0, 0); !errors.Is(err, ErrBadToken) {
		t.Errorf("strict: got %v, want ErrBadToken", err)
	}
	if page, _, err := TokenPage(Decimal.Strictly(), items, "2", 1, 0, 0); err != nil || len(page) != 0 {
		t.Errorf("strict at the end: %v %v", page, err)
	}
	if page, next, err := OffsetPage(items, "3", 1, 0, 0); err != nil || len(page) != 0 || next != "" {
		t.Errorf("lenient: %v %q %v", page, next, err)
	}
}

func TestBase64DecimalRoundTrips(t *testing.T) {
	items := []string{"a", "b", "c"}
	page, next, err := TokenPage(Base64Decimal, items, "", 2, 0, 0)
	if err != nil || !reflect.DeepEqual(page, []string{"a", "b"}) || next != "Mg==" {
		t.Fatalf("first page: %v %q %v", page, next, err)
	}
	page, next, err = TokenPage(Base64Decimal, items, next, 2, 0, 0)
	if err != nil || !reflect.DeepEqual(page, []string{"c"}) || next != "" {
		t.Fatalf("second page: %v %q %v", page, next, err)
	}
}

func TestEmptyPageIsNotNil(t *testing.T) {
	page, _, err := OffsetPage[int](nil, "", 0, 0, 0)
	if err != nil || page == nil {
		t.Fatalf("got %v, %v; want a non-nil empty page", page, err)
	}
}

func TestLookupAndScalarString(t *testing.T) {
	d := Doc{"a": map[string]any{"b": float64(3), "c": true, "l": []any{"x"}}, "s": "v", "n": nil}
	for _, c := range []struct {
		path, sep, want string
		ok              bool
	}{
		{"a.b", ".", "3", true},
		{"a/c", "/", "true", true},
		{"a.l", ".", `["x"]`, true},
		{"s", ".", "v", true},
		{"n", ".", "", true},
		{"a.z", ".", "", false},
		{"s.x", ".", "", false},
	} {
		got, ok := Field(d, c.path, c.sep)
		if got != c.want || ok != c.ok {
			t.Errorf("Field(%q,%q) = %q,%v want %q,%v", c.path, c.sep, got, ok, c.want, c.ok)
		}
	}
	if got := ScalarString(float64(1e21)); got != "1000000000000000000000" {
		t.Errorf("large numbers render without exponent: %q", got)
	}
}

func TestCompareOrdered(t *testing.T) {
	if CompareOrdered("9", "10") >= 0 {
		t.Error("numbers compare numerically")
	}
	if CompareOrdered("b", "a") <= 0 {
		t.Error("text compares lexically")
	}
	if _, ok := CompareNumeric("x", "1"); ok {
		t.Error("text is not numeric")
	}
}

func eq(path, want string) Cmp {
	return Cmp{Path: path, Sep: ".", Test: func(v string, present bool) bool { return present && v == want }}
}

func TestNodesEvaluate(t *testing.T) {
	d := Doc{"a": "1", "b": "2"}
	cases := []struct {
		n    Node
		want bool
	}{
		{True{}, true},
		{eq("a", "1"), true},
		{And{eq("a", "1"), eq("b", "3")}, false},
		{Or{eq("a", "0"), eq("b", "2")}, true},
		{Not{eq("a", "1")}, false},
	}
	for i, c := range cases {
		if got := c.n.Eval(d); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
}

func TestParseOrderBy(t *testing.T) {
	keys, err := ParseOrderBy("name desc, size,age asc", false)
	want := []OrderKey{{"name", true}, {"size", false}, {"age", false}}
	if err != nil || !reflect.DeepEqual(keys, want) {
		t.Fatalf("got %v %v", keys, err)
	}
	if _, err := ParseOrderBy("name DESC", false); err == nil {
		t.Error("a case-sensitive grammar rejects DESC")
	}
	if keys, err := ParseOrderBy("name DESC", true); err != nil || !keys[0].Desc {
		t.Errorf("a case-folding grammar reads DESC: %v %v", keys, err)
	}
	for _, bad := range []string{"a b c", "a sideways", "a,,b", ","} {
		if _, err := ParseOrderBy(bad, false); err == nil {
			t.Errorf("%q must not parse", bad)
		}
	}
}

func TestApplyListFiltersAndOrders(t *testing.T) {
	type res struct {
		Name string `json:"name"`
		Size int    `json:"size"`
		Tier string `json:"tier"`
	}
	items := []res{{"a", 10, "x"}, {"b", 9, "y"}, {"c", 10, "y"}, {"d", 100, "x"}}
	got, err := ApplyList(items, nil, []OrderKey{{"size", true}, {"name", false}}, ".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Name)
	}
	if !reflect.DeepEqual(names, []string{"d", "a", "c", "b"}) {
		t.Errorf("numeric descending then name: %v", names)
	}
	got, err = ApplyList(items, eq("tier", "y"), nil, ".")
	if err != nil || len(got) != 2 || got[0].Name != "b" || got[1].Name != "c" {
		t.Errorf("filter keeps input order: %v %v", got, err)
	}
}
