package gcp_sdk_test

import (
	"errors"
	"testing"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newFSClient builds a high-level Firestore client over the REST transport so it
// talks to the sim's Firestore v1 REST surface. The high-level client is what
// emits DocumentTransform/Precondition/Cursor/Projection wire shapes — the
// low-level REST shim does not.
func newFSClient(t *testing.T, project string) *firestore.Client {
	t.Helper()
	c, err := firestore.NewRESTClient(ctx, project,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	if err != nil {
		t.Fatalf("NewRESTClient: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestFirestore_FieldTransforms exercises the four FieldTransform operators the
// high-level client emits (Increment, ArrayUnion, ArrayRemove, ServerTimestamp)
// through Set(..., MergeAll) and Update writes (BUG-2154).
func TestFirestore_FieldTransforms(t *testing.T) {
	c := newFSClient(t, "fs-transforms")
	doc := c.Collection("counters").Doc("c1")

	// Seed with a base document.
	if _, err := doc.Set(ctx, map[string]any{
		"n":    int64(10),
		"tags": []string{"a", "b"},
	}); err != nil {
		t.Fatalf("Set seed: %v", err)
	}

	// Increment, ArrayUnion (add a new + an already-present element),
	// ArrayRemove, and ServerTimestamp in one Update.
	if _, err := doc.Update(ctx, []firestore.Update{
		{Path: "n", Value: firestore.Increment(5)},
		{Path: "tags", Value: firestore.ArrayUnion("b", "c")},
		{Path: "gone", Value: firestore.ArrayRemove("x")},
		{Path: "updatedAt", Value: firestore.ServerTimestamp},
	}); err != nil {
		t.Fatalf("Update transforms: %v", err)
	}

	snap, err := doc.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	data := snap.Data()

	if got, ok := data["n"].(int64); !ok || got != 15 {
		t.Fatalf("increment: want n=15, got %v (%T)", data["n"], data["n"])
	}

	tags, ok := data["tags"].([]any)
	if !ok {
		t.Fatalf("tags is %T, want []any", data["tags"])
	}
	gotTags := make([]string, 0, len(tags))
	for _, v := range tags {
		s, _ := v.(string)
		gotTags = append(gotTags, s)
	}
	// arrayUnion("b","c") onto ["a","b"] → ["a","b","c"] (b not duplicated).
	if len(gotTags) != 3 || gotTags[0] != "a" || gotTags[1] != "b" || gotTags[2] != "c" {
		t.Fatalf("arrayUnion: want [a b c], got %v", gotTags)
	}

	// gone started absent; arrayRemove yields an empty array.
	gone, ok := data["gone"].([]any)
	if !ok || len(gone) != 0 {
		t.Fatalf("arrayRemove on missing: want empty array, got %v (%T)", data["gone"], data["gone"])
	}

	// ServerTimestamp set the field to a real timestamp value.
	if _, ok := data["updatedAt"]; !ok {
		t.Fatalf("serverTimestamp: updatedAt field missing")
	}
	if snap.UpdateTime.IsZero() {
		t.Fatalf("serverTimestamp: updateTime not set")
	}
}

// TestFirestore_IncrementCreatesDoc verifies a standalone transform (Increment
// on an absent field, doc created by Set) applies, and that increment of a
// missing field treats it as 0.
func TestFirestore_IncrementMissingField(t *testing.T) {
	c := newFSClient(t, "fs-incr-missing")
	doc := c.Collection("counters").Doc("fresh")

	if _, err := doc.Set(ctx, map[string]any{"hits": firestore.Increment(3)}); err != nil {
		t.Fatalf("Set+Increment: %v", err)
	}
	snap, err := doc.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got, _ := snap.Data()["hits"].(int64); got != 3 {
		t.Fatalf("increment from missing: want 3, got %v", snap.Data()["hits"])
	}
}

// TestFirestore_CreatePrecondition: Create on an already-existing doc must fail
// (the high-level Create emits currentDocument.exists=false). The sim returns
// the ALREADY_EXISTS status at HTTP 409, exactly as real Firestore does; the
// gax REST transport maps HTTP 409 → codes.Aborted (its canonical HTTP→gRPC
// mapping has no entry for AlreadyExists), so the REST client surfaces Aborted.
// This is the genuine behavior of the high-level REST client against real
// Firestore as well — only the gRPC transport would surface AlreadyExists.
func TestFirestore_CreatePrecondition(t *testing.T) {
	c := newFSClient(t, "fs-create-pre")
	doc := c.Collection("users").Doc("u1")

	if _, err := doc.Create(ctx, map[string]any{"name": "alice"}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := doc.Create(ctx, map[string]any{"name": "bob"})
	if err == nil {
		t.Fatalf("second Create on existing doc: want error, got nil")
	}
	if status.Code(err) != codes.Aborted {
		t.Fatalf("second Create: want Aborted (HTTP 409 ALREADY_EXISTS via REST), got %v (%v)", status.Code(err), err)
	}
}

// TestFirestore_UpdatePrecondition: Update on a missing doc must fail with
// NotFound (the high-level Update emits currentDocument.exists=true) (BUG-2155).
func TestFirestore_UpdatePrecondition(t *testing.T) {
	c := newFSClient(t, "fs-update-pre")
	doc := c.Collection("users").Doc("ghost")

	_, err := doc.Update(ctx, []firestore.Update{{Path: "name", Value: "nobody"}})
	if err == nil {
		t.Fatalf("Update on missing doc: want error, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("Update on missing doc: want NotFound, got %v (%v)", status.Code(err), err)
	}
}

// TestFirestore_NumericEqualityQuery: Where("n","==",1) must match a doc whose n
// was stored as an integer, exercising the unified numeric equality (BUG-2156).
func TestFirestore_NumericEqualityQuery(t *testing.T) {
	c := newFSClient(t, "fs-num-eq")
	col := c.Collection("nums")

	if _, err := col.Doc("intone").Set(ctx, map[string]any{"n": int64(1)}); err != nil {
		t.Fatalf("Set int: %v", err)
	}
	if _, err := col.Doc("two").Set(ctx, map[string]any{"n": int64(2)}); err != nil {
		t.Fatalf("Set two: %v", err)
	}

	// Query with a float literal 1.0 — must match the integer-stored 1.
	iter := col.Where("n", "==", 1.0).Documents(ctx)
	names := collectNames(t, iter)
	if len(names) != 1 || names[0] != "intone" {
		t.Fatalf("numeric equality: want [intone], got %v", names)
	}
}

// TestFirestore_CursorPagination: OrderBy(...).StartAfter(...) must slice the
// ordered result by the cursor (BUG-2157).
func TestFirestore_CursorPagination(t *testing.T) {
	c := newFSClient(t, "fs-cursor")
	col := c.Collection("ranked")
	for i := 1; i <= 5; i++ {
		if _, err := col.Doc(string(rune('a'+i-1))).Set(ctx, map[string]any{"rank": int64(i)}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Page 1: first two by rank.
	page1 := collectNames(t, col.OrderBy("rank", firestore.Asc).Limit(2).Documents(ctx))
	if len(page1) != 2 || page1[0] != "a" || page1[1] != "b" {
		t.Fatalf("page1: want [a b], got %v", page1)
	}

	// Page 2: StartAfter rank=2 → ranks 3,4,5.
	page2 := collectNames(t, col.OrderBy("rank", firestore.Asc).StartAfter(int64(2)).Documents(ctx))
	if len(page2) != 3 || page2[0] != "c" || page2[1] != "d" || page2[2] != "e" {
		t.Fatalf("page2: want [c d e], got %v", page2)
	}
}

// TestFirestore_CursorTieBreakByName exercises the implicit __name__ order the
// high-level client appends to a cursor: with two docs sharing a rank, a
// document-snapshot StartAfter cursor must tie-break by document name
// (referenceValue) (BUG-2157).
func TestFirestore_CursorTieBreakByName(t *testing.T) {
	c := newFSClient(t, "fs-cursor-tie")
	col := c.Collection("tied")
	// Two docs at rank 1 (a, b) and one at rank 2 (c).
	for name, rank := range map[string]int64{"a": 1, "b": 1, "c": 2} {
		if _, err := col.Doc(name).Set(ctx, map[string]any{"rank": rank}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	// Snapshot of "a" (rank 1). StartAfter(snap of a) ordered by rank must skip
	// a itself and continue with b (same rank, name > a) then c.
	snapA, err := col.Doc("a").Get(ctx)
	if err != nil {
		t.Fatalf("get a: %v", err)
	}
	got := collectNames(t, col.OrderBy("rank", firestore.Asc).StartAfter(snapA).Documents(ctx))
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("tie-break cursor: want [b c], got %v", got)
	}
}

// TestFirestore_SelectProjection: Select("a") must return only the projected
// field (BUG-2157).
func TestFirestore_SelectProjection(t *testing.T) {
	c := newFSClient(t, "fs-select")
	col := c.Collection("docs")
	if _, err := col.Doc("d1").Set(ctx, map[string]any{
		"a": "keep",
		"b": "drop",
		"c": int64(7),
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := col.Select("a").Documents(ctx)
	snap, err := iter.Next()
	if err != nil {
		t.Fatalf("iter.Next: %v", err)
	}
	data := snap.Data()
	if _, ok := data["a"]; !ok {
		t.Fatalf("projection: field a missing, got %v", data)
	}
	if _, ok := data["b"]; ok {
		t.Fatalf("projection: field b should be absent, got %v", data)
	}
	if len(data) != 1 {
		t.Fatalf("projection: want only field a, got %v", data)
	}
}

func collectNames(t *testing.T, iter *firestore.DocumentIterator) []string {
	t.Helper()
	var names []string
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			t.Fatalf("iter.Next: %v", err)
		}
		names = append(names, snap.Ref.ID)
	}
	return names
}
