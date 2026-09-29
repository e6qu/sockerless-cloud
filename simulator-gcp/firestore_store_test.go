package main

import (
	"context"
	"testing"

	fspb "cloud.google.com/go/firestore/apiv1/firestorepb"

	"github.com/e6qu/sockerless-cloud/sim"
)

// fsUnscannableStore is a real document store that fails the test when a
// caller reads it whole: a query reads the documents under its parent and no
// tenant's documents but its own.
type fsUnscannableStore struct {
	sim.Store[FSDocument]
	t *testing.T
}

func (s fsUnscannableStore) List() []FSDocument {
	s.t.Fatal("a Firestore read listed every document in the store")
	return nil
}

func (s fsUnscannableStore) Filter(func(FSDocument) bool) []FSDocument {
	s.t.Fatal("a Firestore read filtered every document in the store")
	return nil
}

func fsSeedDocuments(t *testing.T, names ...string) {
	t.Helper()
	previous := fsDocuments
	fsDocuments = fsUnscannableStore{Store: sim.NewStateStore[FSDocument](), t: t}
	t.Cleanup(func() { fsDocuments = previous })
	for _, name := range names {
		fsDocuments.Put(name, FSDocument{Name: name})
	}
}

func TestFirestoreQueriesReadOnlyTheirParentsDocuments(t *testing.T) {
	const root = "projects/p/databases/(default)/documents"
	fsSeedDocuments(t,
		root+"/users/b",
		root+"/users/a",
		root+"/users/a/orders/o1",
		root+"/teams/t",
		"projects/other/databases/(default)/documents/users/z",
	)

	var q fsStructuredQuery
	q.From = append(q.From, struct {
		CollectionID string `json:"collectionId"`
	}{CollectionID: "users"})
	docs := fsEvaluateQuery(root, q)
	if len(docs) != 2 || docs[0].Name != root+"/users/a" || docs[1].Name != root+"/users/b" {
		t.Fatalf("query over users: %+v", docs)
	}

	server := &firestoreGRPC{}
	listed, err := server.ListDocuments(context.Background(), &fspb.ListDocumentsRequest{Parent: root})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range listed.GetDocuments() {
		names = append(names, d.GetName())
	}
	want := []string{root + "/teams/t", root + "/users/a", root + "/users/b"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] || names[2] != want[2] {
		t.Fatalf("listing every collection under the root: %v, want %v", names, want)
	}

	ids, err := server.ListCollectionIds(context.Background(), &fspb.ListCollectionIdsRequest{Parent: root + "/users/a"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids.GetCollectionIds(); len(got) != 1 || got[0] != "orders" {
		t.Fatalf("collections under users/a: %v", got)
	}
}
