package main

import "testing"

// TestCosmosParseQuery_Malformed guards that the SQL-subset parser never panics
// on malformed / hostile input — including the historical fuzz crash seeds from
// the old equality-only parser (Unicode case-folding offsets, NULs). A query the
// parser can't handle returns an error, never the whole collection.
func TestCosmosParseQuery_Malformed(t *testing.T) {
	cases := []string{
		"00©0000000000000WHERE", // original fuzz crash seed
		"K WHERE c.id = 'v'",    // Kelvin sign before WHERE (no leading SELECT)
		"İWHERE c.id = 'v'",     // dotted capital I (expands on fold)
		"WHERE",
		"where c.id =",
		"\x00where\x00=\x00",
		"SELECT ( ( ( ( ( c.a FROM c",
		"",
	}
	for _, q := range cases {
		// Must not panic; an unparseable query simply returns an error.
		_, _ = cosmosParseQuery(q)
	}
}

// TestCosmosRunQuery exercises the parser + evaluator against a small document
// set covering the full supported grammar: projection, nested paths, numeric
// unification, AND/OR/NOT, IN, the string functions, ORDER BY, OFFSET/LIMIT, and
// the COUNT aggregate.
func TestCosmosRunQuery(t *testing.T) {
	docs := []CosmosDocument{
		{ID: "1", Body: map[string]any{"id": "1", "team": "platform", "score": float64(5), "nested": map[string]any{"city": "nyc"}, "name": "alice"}},
		{ID: "2", Body: map[string]any{"id": "2", "team": "platform", "score": float64(10), "nested": map[string]any{"city": "sf"}, "name": "bob"}},
		{ID: "3", Body: map[string]any{"id": "3", "team": "infra", "score": float64(7), "name": "carol"}},
	}
	params := map[string]any{"@team": "platform", "@min": float64(5)}

	run := func(t *testing.T, query string) []map[string]any {
		t.Helper()
		plan, err := cosmosParseQuery(query)
		if err != nil {
			t.Fatalf("parse %q: %v", query, err)
		}
		return cosmosRunQuery(plan, docs, params)
	}

	t.Run("select all", func(t *testing.T) {
		if got := run(t, "SELECT * FROM c"); len(got) != 3 {
			t.Fatalf("want 3 rows, got %d", len(got))
		}
	})

	t.Run("equality param + numeric range", func(t *testing.T) {
		got := run(t, "SELECT * FROM c WHERE c.team = @team AND c.score > @min")
		if len(got) != 1 || got[0]["id"] != "2" {
			t.Fatalf("want only doc 2 (score>5 in platform), got %v", got)
		}
	})

	t.Run("numeric unification", func(t *testing.T) {
		// 5 (int in doc) must equal 5.0 literal.
		got := run(t, "SELECT * FROM c WHERE c.score = 5.0")
		if len(got) != 1 || got[0]["id"] != "1" {
			t.Fatalf("want doc 1 for score=5.0, got %v", got)
		}
	})

	t.Run("nested path", func(t *testing.T) {
		got := run(t, "SELECT * FROM c WHERE c.nested.city = 'sf'")
		if len(got) != 1 || got[0]["id"] != "2" {
			t.Fatalf("want doc 2 for nested.city=sf, got %v", got)
		}
	})

	t.Run("OR and NOT", func(t *testing.T) {
		got := run(t, "SELECT * FROM c WHERE c.team = 'infra' OR NOT c.score < 10")
		// infra (doc3) OR score>=10 (doc2) → docs 2 and 3.
		if len(got) != 2 {
			t.Fatalf("want 2 rows, got %v", got)
		}
	})

	t.Run("IN", func(t *testing.T) {
		got := run(t, "SELECT * FROM c WHERE c.id IN ('1','3')")
		if len(got) != 2 {
			t.Fatalf("want 2 rows for IN, got %v", got)
		}
	})

	t.Run("CONTAINS", func(t *testing.T) {
		got := run(t, "SELECT * FROM c WHERE CONTAINS(c.name, 'ar')")
		if len(got) != 1 || got[0]["id"] != "3" {
			t.Fatalf("want carol for CONTAINS 'ar', got %v", got)
		}
	})

	t.Run("STARTSWITH ENDSWITH", func(t *testing.T) {
		if got := run(t, "SELECT * FROM c WHERE STARTSWITH(c.name,'a')"); len(got) != 1 || got[0]["id"] != "1" {
			t.Fatalf("STARTSWITH a → want alice, got %v", got)
		}
		if got := run(t, "SELECT * FROM c WHERE ENDSWITH(c.name,'b')"); len(got) != 1 || got[0]["id"] != "2" {
			t.Fatalf("ENDSWITH b → want bob, got %v", got)
		}
	})

	t.Run("ORDER BY DESC", func(t *testing.T) {
		got := run(t, "SELECT c.id FROM c ORDER BY c.score DESC")
		if len(got) != 3 || got[0]["id"] != "2" || got[2]["id"] != "1" {
			t.Fatalf("ORDER BY score DESC wrong order: %v", got)
		}
	})

	t.Run("OFFSET LIMIT", func(t *testing.T) {
		// Ascending scores: doc1=5, doc3=7, doc2=10. OFFSET 1 LIMIT 1 → doc3.
		got := run(t, "SELECT c.id FROM c ORDER BY c.score ASC OFFSET 1 LIMIT 1")
		if len(got) != 1 || got[0]["id"] != "3" {
			t.Fatalf("OFFSET 1 LIMIT 1 ascending want doc 3, got %v", got)
		}
	})

	t.Run("TOP", func(t *testing.T) {
		got := run(t, "SELECT TOP 2 c.id FROM c ORDER BY c.score ASC")
		if len(got) != 2 || got[0]["id"] != "1" {
			t.Fatalf("TOP 2 ascending want [doc1, doc3], got %v", got)
		}
	})

	t.Run("projection", func(t *testing.T) {
		got := run(t, "SELECT c.id, c.team FROM c WHERE c.id = '1'")
		if len(got) != 1 {
			t.Fatalf("want 1 row, got %v", got)
		}
		if _, ok := got[0]["score"]; ok {
			t.Fatalf("projection must drop unselected fields: %v", got[0])
		}
		if got[0]["team"] != "platform" {
			t.Fatalf("projection missing team: %v", got[0])
		}
	})

	t.Run("count aggregate", func(t *testing.T) {
		got := run(t, "SELECT VALUE COUNT(1) FROM c WHERE c.team = 'platform'")
		if len(got) != 1 || got[0]["$1"] != 2 {
			t.Fatalf("COUNT(1) want 2, got %v", got)
		}
	})
}
