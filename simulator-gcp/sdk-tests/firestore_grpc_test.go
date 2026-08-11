package gcp_sdk_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	firestorepb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// newFSGRPCClient builds the high-level cloud.google.com/go/firestore client
// over gRPC (its only transport), pointed at the simulator's Firestore gRPC
// data plane via FIRESTORE_EMULATOR_HOST — the same coordinate the client uses
// to reach Google's own Firestore emulator.
func newFSGRPCClient(t *testing.T, project string) *firestore.Client {
	t.Helper()
	t.Setenv("FIRESTORE_EMULATOR_HOST", grpcAddr)
	c, err := firestore.NewClient(ctx, project,
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	)
	if err != nil {
		t.Fatalf("firestore.NewClient (gRPC): %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestFirestore_GRPC_CreateGetRoundTrip exercises CreateDocument + GetDocument
// over gRPC with nested maps, arrays, integers, timestamps, strings, and bytes.
func TestFirestore_GRPC_CreateGetRoundTrip(t *testing.T) {
	c := newFSGRPCClient(t, "fs-grpc-proj")
	doc := c.Collection("roundtrip").Doc("d1")

	ts := time.Date(2025, 6, 27, 12, 0, 0, 0, time.UTC)
	if _, err := doc.Create(ctx, map[string]any{
		"s":  "hello",
		"i":  int64(42),
		"b":  true,
		"by": []byte{1, 2, 3},
		"f":  3.5,
		"arr": []any{
			int64(1), "two", int64(3),
		},
		"nested": map[string]any{
			"inner": int64(7),
			"deep":  map[string]any{"x": "y"},
		},
		"ts": ts,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	snap, err := doc.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v, _ := snap.DataAt("s"); v != "hello" {
		t.Errorf("s = %v, want hello", v)
	}
	if v, _ := snap.DataAt("i"); v != int64(42) {
		t.Errorf("i = %v, want 42", v)
	}
	if v, _ := snap.DataAt("b"); v != true {
		t.Errorf("b = %v, want true", v)
	}
	if v, _ := snap.DataAt("by"); string(v.([]byte)) != "\x01\x02\x03" {
		t.Errorf("by = %v, want [1 2 3]", v)
	}
	if v, _ := snap.DataAt("f"); v != 3.5 {
		t.Errorf("f = %v, want 3.5", v)
	}
	if arr, _ := snap.DataAt("arr"); len(arr.([]any)) != 3 {
		t.Errorf("arr len = %d, want 3", len(arr.([]any)))
	}
	nested, _ := snap.DataAt("nested")
	if m, ok := nested.(map[string]any); !ok || m["inner"] != int64(7) {
		t.Errorf("nested.inner = %v, want 7", nested)
	}
	deep, _ := snap.DataAt("nested.deep")
	if m, ok := deep.(map[string]any); !ok || m["x"] != "y" {
		t.Errorf("nested.deep.x missing, got %v", deep)
	}
	if gts, _ := snap.DataAt("ts"); !gts.(time.Time).Equal(ts) {
		t.Errorf("ts = %v, want %v", gts, ts)
	}
}

// TestFirestore_GRPC_UpdateMerge asserts UpdateDocument with a field mask
// (merge) preserves unmasked fields.
func TestFirestore_GRPC_UpdateMerge(t *testing.T) {
	c := newFSGRPCClient(t, "fs-grpc-proj")
	doc := c.Collection("merge").Doc("m1")

	if _, err := doc.Set(ctx, map[string]any{
		"a": int64(1),
		"b": "two",
		"c": int64(3),
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Update only "a" and "b" via field paths; "c" must survive.
	if _, err := doc.Update(ctx, []firestore.Update{
		{Path: "a", Value: int64(100)},
		{Path: "b", Value: "changed"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	snap, err := doc.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v, _ := snap.DataAt("a"); v != int64(100) {
		t.Errorf("a = %v, want 100", v)
	}
	if v, _ := snap.DataAt("b"); v != "changed" {
		t.Errorf("b = %v, want changed", v)
	}
	if v, _ := snap.DataAt("c"); v != int64(3) {
		t.Errorf("c = %v, want 3 (unmasked field must survive)", v)
	}

	// Set with Merge rewrites a subset, leaving the rest intact.
	if _, err := doc.Set(ctx, map[string]any{"a": int64(7)}, firestore.MergeAll); err != nil {
		t.Fatalf("Set MergeAll: %v", err)
	}
	snap2, _ := doc.Get(ctx)
	if v, _ := snap2.DataAt("a"); v != int64(7) {
		t.Errorf("a after merge = %v, want 7", v)
	}
	if v, _ := snap2.DataAt("c"); v != int64(3) {
		t.Errorf("c after merge = %v, want 3 (merge preserves)", v)
	}
}

// TestFirestore_GRPC_DeletePrecondition asserts DeleteDocument honors a
// precondition and succeeds when none is set.
func TestFirestore_GRPC_DeletePrecondition(t *testing.T) {
	c := newFSGRPCClient(t, "fs-grpc-proj")
	doc := c.Collection("del").Doc("x1")
	if _, err := doc.Create(ctx, map[string]any{"k": "v"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Deleting a non-existent document with the Exists precondition (requires
	// exists=true) must fail with NotFound.
	missing := c.Collection("del").Doc("absent")
	if _, err := missing.Delete(ctx, firestore.Exists); status.Code(err) != codes.NotFound {
		t.Fatalf("Delete absent with Exists precondition code = %v, want NotFound", status.Code(err))
	}

	// A LastUpdateTime precondition that doesn't match the stored doc must fail
	// with FailedPrecondition.
	_, err := doc.Delete(ctx, firestore.LastUpdateTime(time.Now().Add(time.Hour)))
	if status.Code(err) != codes.FailedPrecondition && status.Code(err) != codes.Aborted {
		t.Fatalf("Delete with mismatched updateTime code = %v, want FailedPrecondition", status.Code(err))
	}

	// Unconditional delete succeeds.
	if _, err := doc.Delete(ctx); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := doc.Get(ctx); status.Code(err) != codes.NotFound {
		t.Fatalf("Get after delete code = %v, want NotFound", status.Code(err))
	}
}

// TestFirestore_GRPC_RunQuery exercises RunQuery (where, order, limit, cursor)
// over gRPC via the high-level client's Query.
func TestFirestore_GRPC_RunQuery(t *testing.T) {
	c := newFSGRPCClient(t, "fs-grpc-proj")
	coll := c.Collection("query")
	for _, n := range []int{5, 1, 3, 2, 4} {
		if _, err := coll.NewDoc().Create(ctx, map[string]any{"n": int64(n), "tag": "q"}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	// WHERE n >= 3 ORDER BY n ASC LIMIT 2 → [3, 4]
	got := collectField(t, coll.Where("n", ">=", 3).OrderBy("n", firestore.Asc).Limit(2).Documents(ctx), "n")
	if len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Errorf("query results = %v, want [3 4]", got)
	}

	// Cursor: start after n=4 yields [5] across the full ordered set.
	got2 := collectField(t, coll.OrderBy("n", firestore.Asc).StartAfter(4).Documents(ctx), "n")
	if len(got2) != 1 || got2[0] != 5 {
		t.Errorf("cursor results = %v, want [5]", got2)
	}
}

func collectField(t *testing.T, iter *firestore.DocumentIterator, field string) []int64 {
	t.Helper()
	var out []int64
	for {
		snap, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			t.Fatalf("iter.Next: %v", err)
		}
		v, _ := snap.DataAt(field)
		out = append(out, v.(int64))
	}
	return out
}

// TestFirestore_GRPC_Transaction drives BeginTransaction/Commit (and Rollback)
// via the high-level client's RunTransaction read-modify-write.
func TestFirestore_GRPC_Transaction(t *testing.T) {
	c := newFSGRPCClient(t, "fs-grpc-proj")
	doc := c.Collection("txnacct").Doc("t1")
	if _, err := doc.Set(ctx, map[string]any{"balance": int64(100)}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	err := c.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(doc)
		if err != nil {
			return err
		}
		bal, _ := snap.DataAt("balance")
		return tx.Set(doc, map[string]any{"balance": bal.(int64) - 40})
	})
	if err != nil {
		t.Fatalf("RunTransaction: %v", err)
	}
	got, err := doc.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v, _ := got.DataAt("balance"); v != int64(60) {
		t.Errorf("balance after txn = %v, want 60", v)
	}

	// Rollback: an aborted transaction must not apply its write.
	doc2 := c.Collection("txnacct").Doc("t2")
	if _, err := doc2.Set(ctx, map[string]any{"balance": int64(10)}); err != nil {
		t.Fatalf("Set t2: %v", err)
	}
	sentinel := errors.New("abort")
	if err := c.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		if err := tx.Set(doc2, map[string]any{"balance": int64(999)}); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("RunTransaction err = %v, want sentinel", err)
	}
	g2, _ := doc2.Get(ctx)
	if v, _ := g2.DataAt("balance"); v != int64(10) {
		t.Errorf("balance after rollback = %v, want 10", v)
	}
}

// TestFirestore_GRPC_BatchGetDocuments exercises the BatchGetDocuments server
// stream via GetAll, mixing existing and missing documents.
func TestFirestore_GRPC_BatchGetDocuments(t *testing.T) {
	c := newFSGRPCClient(t, "fs-grpc-proj")
	existing := c.Collection("batchget").Doc("e1")
	missing := c.Collection("batchget").Doc("m1")
	if _, err := existing.Create(ctx, map[string]any{"v": "set"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	snaps, err := c.GetAll(ctx, []*firestore.DocumentRef{existing, missing})
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("GetAll returned %d snaps, want 2", len(snaps))
	}
	if !snaps[0].Exists() {
		t.Errorf("snap[0] should exist")
	}
	if snaps[1].Exists() {
		t.Errorf("snap[1] should be missing")
	}
}

// TestFirestore_GRPC_BatchWrite exercises BatchWrite (non-atomic batch) via the
// high-level client's WriteBatch.
func TestFirestore_GRPC_BatchWrite(t *testing.T) {
	c := newFSGRPCClient(t, "fs-grpc-proj")
	d1 := c.Collection("batch").Doc("b1")
	d2 := c.Collection("batch").Doc("b2")
	batch := c.Batch()
	batch.Set(d1, map[string]any{"k": "v1"})
	batch.Set(d2, map[string]any{"k": "v2"})
	if _, err := batch.Commit(ctx); err != nil {
		t.Fatalf("Batch.Commit: %v", err)
	}
	s1, _ := d1.Get(ctx)
	s2, _ := d2.Get(ctx)
	if v, _ := s1.DataAt("k"); v != "v1" {
		t.Errorf("d1.k = %v, want v1", v)
	}
	if v, _ := s2.DataAt("k"); v != "v2" {
		t.Errorf("d2.k = %v, want v2", v)
	}
}

// TestFirestore_GRPC_ListDocuments exercises ListDocuments over gRPC.
func TestFirestore_GRPC_ListDocuments(t *testing.T) {
	c := newFSGRPCClient(t, "fs-grpc-proj")
	coll := c.Collection("listdocs")
	ids := []string{"l1", "l2", "l3"}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err := coll.Doc(id).Create(ctx, map[string]any{"x": id}); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	got := collectDocIDs(t, coll.Documents(ctx))
	sort.Strings(got)
	if len(got) != 3 {
		t.Fatalf("ListDocuments returned %d, want 3", len(got))
	}
	for i := range ids {
		if got[i] != ids[i] {
			t.Errorf("ListDocuments[%d] = %s, want %s", i, got[i], ids[i])
		}
	}
}

func collectDocIDs(t *testing.T, iter *firestore.DocumentIterator) []string {
	t.Helper()
	var out []string
	for {
		snap, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			t.Fatalf("iter.Next: %v", err)
		}
		out = append(out, snap.Ref.ID)
	}
	return out
}

// TestFirestore_GRPC_Aggregation exercises RunAggregationQuery (COUNT) over gRPC.
func TestFirestore_GRPC_Aggregation(t *testing.T) {
	c := newFSGRPCClient(t, "fs-grpc-proj")
	coll := c.Collection("agg")
	for i := 0; i < 5; i++ {
		if _, err := coll.NewDoc().Create(ctx, map[string]any{"n": int64(i)}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	q := coll.Where("n", ">=", 2)
	res, err := q.NewAggregationQuery().WithCount("count").Get(ctx)
	if err != nil {
		t.Fatalf("aggregation: %v", err)
	}
	// The high-level client surfaces aggregation values as the raw proto Value
	// (AggregationResult is map[string]interface{} holding *firestorepb.Value).
	cv := res["count"].(*firestorepb.Value)
	if got := cv.GetIntegerValue(); got != 3 {
		t.Errorf("count = %d, want 3", got)
	}
}

// TestFirestore_GRPC_ListCollectionIds exercises ListCollectionIds over gRPC.
func TestFirestore_GRPC_ListCollectionIds(t *testing.T) {
	c := newFSGRPCClient(t, "fs-grpc-proj")
	parent := c.Collection("rootcol").Doc("root")
	if _, err := parent.Create(ctx, map[string]any{"x": 1}); err != nil {
		t.Fatalf("Create root: %v", err)
	}
	if _, err := parent.Collection("childA").Doc("a").Create(ctx, map[string]any{"x": 1}); err != nil {
		t.Fatalf("Create childA: %v", err)
	}
	if _, err := parent.Collection("childB").Doc("b").Create(ctx, map[string]any{"x": 1}); err != nil {
		t.Fatalf("Create childB: %v", err)
	}
	iter := parent.Collections(ctx)
	var got []string
	for {
		ref, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			t.Fatalf("Collections iter: %v", err)
		}
		got = append(got, ref.ID)
	}
	sort.Strings(got)
	want := []string{"childA", "childB"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("collections = %v, want %v", got, want)
	}
}
