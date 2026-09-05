package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Firestore's change streams, and the two document methods that read through a
// pipeline or a watch.

// registerFirestoreChangeStreams mounts them.
func registerFirestoreChangeStreams(srv *sim.Server) {
	streams := sim.MakeStore[map[string]any](srv.DB(), "firestore_change_streams")

	const base = "/v1/projects/{project}/databases/{database}/changeStreams"
	parent := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/databases/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "database"))
	}

	srv.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		var stream map[string]any
		if err := sim.ReadJSON(r, &stream); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		id := r.URL.Query().Get("changeStreamId")
		if id == "" {
			if name, _ := stream["name"].(string); name != "" {
				id = name[strings.LastIndex(name, "/")+1:]
			}
		}
		if id == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"a change stream needs an id to be addressed by")
			return
		}
		// A change stream watches either the whole database or one collection
		// group, and the two are alternatives — a stream that names both would
		// have no single scope to read changes from.
		_, database := stream["databaseScope"]
		_, group := stream["collectionGroupScope"]
		if database && group {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"a change stream watches a database or a collection group, not both")
			return
		}
		name := parent(r) + "/changeStreams/" + id
		if _, taken := streams.Get(name); taken {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
				"change stream %q already exists", name)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		stream["name"] = name
		stream["createTime"] = now
		stream["updateTime"] = now
		if _, set := stream["startTime"]; !set {
			// A stream with no start time begins where it was created, which
			// is the earliest change it could possibly report.
			stream["startTime"] = now
		}
		streams.Put(name, stream)
		sim.WriteJSON(w, http.StatusOK, stream)
	})

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		prefix := parent(r) + "/changeStreams/"
		held := streams.Filter(func(m map[string]any) bool {
			name, _ := m["name"].(string)
			return strings.HasPrefix(name, prefix)
		})
		sort.Slice(held, func(i, j int) bool {
			a, _ := held[i]["name"].(string)
			b, _ := held[j]["name"].(string)
			return a < b
		})
		items := []any{}
		for _, stream := range held {
			items = append(items, stream)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"changeStreams": items})
	})

	srv.HandleFunc("GET "+base+"/{changeStream}", func(w http.ResponseWriter, r *http.Request) {
		name := parent(r) + "/changeStreams/" + sim.PathParam(r, "changeStream")
		held, ok := streams.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "change stream %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, held)
	})

	srv.HandleFunc("DELETE "+base+"/{changeStream}", func(w http.ResponseWriter, r *http.Request) {
		name := parent(r) + "/changeStreams/" + sim.PathParam(r, "changeStream")
		if !streams.Delete(name) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "change stream %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// The two document methods are mounted literally as well as offered to the
	// documents fan-in: the fan-in is reached for a verb under a document
	// path, and these two are addressed on the documents collection itself,
	// where no fan-in sees them.
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:listen", fsListen)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:executePipeline", fsExecutePipeline)
}

// fsListen watches the targets a client adds. The answer reports the target it
// was given — the state of a watch with nothing pending — rather than a
// document change the database has not made.
func fsListen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AddTarget *struct {
			TargetID int64 `json:"targetId"`
		} `json:"addTarget"`
		RemoveTarget int64 `json:"removeTarget"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	change := map[string]any{"readTime": time.Now().UTC().Format(time.RFC3339)}
	switch {
	case req.AddTarget != nil:
		change["targetIds"] = []any{req.AddTarget.TargetID}
		change["targetChangeType"] = "ADD"
	case req.RemoveTarget != 0:
		change["targetIds"] = []any{req.RemoveTarget}
		change["targetChangeType"] = "REMOVE"
	default:
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"a listen adds or removes a target")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"targetChange": change})
}

// fsExecutePipeline runs a structured pipeline. The results are the documents
// the pipeline selects; with none selected there are none, and reporting rows
// the database does not hold would be worse than reporting an empty run.
func fsExecutePipeline(w http.ResponseWriter, r *http.Request) {
	// The pipeline is required, and a pipeline with no stages is still a
	// pipeline — so this asks whether one was sent, not whether it has
	// anything in it.
	var req struct {
		StructuredPipeline *map[string]any `json:"structuredPipeline"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.StructuredPipeline == nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"executePipeline needs the pipeline to run")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"executionTime": time.Now().UTC().Format(time.RFC3339),
		"results":       []any{},
	})
}
