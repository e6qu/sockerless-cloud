package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	fspb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// Firestore change streams, and the listen and executePipeline document
// methods.

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
		now := fsNow()
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
		gcpWriteNamedMaps(w, streams, prefix, "changeStreams")
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

	// The fan-in sees verbs under a document path; these two are addressed on the
	// documents collection itself, so they also mount literally.
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:listen", fsListen)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:executePipeline", fsExecutePipeline)
}

// fsListen serves Listen over REST: the one ListenRequest the body carries
// opens the watch, and the ListenResponses the gRPC watch produces stream back
// as a JSON array until the client hangs up.
func fsListen(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	var req fspb.ListenRequest
	if err := protojson.Unmarshal(body, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.GetTargetChange() == nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a listen adds or removes a target")
		return
	}
	req.Database = "projects/" + sim.PathParam(r, "project") + "/databases/" + sim.PathParam(r, "database")
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	stream := &fsRESTListenStream{ctx: r.Context(), request: &req, w: w}
	_, _ = io.WriteString(w, "[")
	err = (&firestoreGRPC{}).Listen(stream)
	if err != nil && r.Context().Err() == nil {
		stream.write(map[string]any{"error": map[string]any{"code": int(status.Code(err)), "message": status.Convert(err).Message(), "status": status.Code(err).String()}})
	}
	_, _ = io.WriteString(w, "]")
}

// fsRESTListenStream is the gRPC Listen stream over one HTTP exchange: Recv
// hands the request over once and then waits for the client to hang up.
type fsRESTListenStream struct {
	grpc.ServerStream
	ctx     context.Context
	request *fspb.ListenRequest
	w       http.ResponseWriter
	sent    int
}

func (s *fsRESTListenStream) Context() context.Context { return s.ctx }

func (s *fsRESTListenStream) Recv() (*fspb.ListenRequest, error) {
	if req := s.request; req != nil {
		s.request = nil
		return req, nil
	}
	<-s.ctx.Done()
	return nil, io.EOF
}

func (s *fsRESTListenStream) Send(resp *fspb.ListenResponse) error {
	data, err := protojson.Marshal(resp)
	if err != nil {
		return err
	}
	return s.writeRaw(data)
}

func (s *fsRESTListenStream) write(v any) {
	data, err := json.Marshal(v)
	if err == nil {
		_ = s.writeRaw(data)
	}
}

func (s *fsRESTListenStream) writeRaw(data []byte) error {
	if s.sent > 0 {
		if _, err := io.WriteString(s.w, ",\n"); err != nil {
			return err
		}
	}
	s.sent++
	if _, err := s.w.Write(data); err != nil {
		return err
	}
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// fsExecutePipeline runs the pipeline and answers its result documents.
func fsExecutePipeline(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StructuredPipeline *fsStructuredPipeline `json:"structuredPipeline"`
		Transaction        string                `json:"transaction"`
		NewTransaction     *struct {
			ReadOnly *struct{} `json:"readOnly"`
		} `json:"newTransaction"`
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
	if _, ok := fsReadTimeForTxn(req.Transaction); !ok {
		GCPError(w, http.StatusBadRequest, "Invalid transaction.", "INVALID_ARGUMENT")
		return
	}
	resp := map[string]any{}
	if req.NewTransaction != nil {
		resp["transaction"] = fsBeginTxnBytes(req.NewTransaction.ReadOnly != nil, "")
	}
	now := time.Now().UTC()
	docs, err := fsRunPipeline(sim.PathParam(r, "project"), sim.PathParam(r, "database"), req.StructuredPipeline, now)
	if err != nil {
		GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
		return
	}
	resp["results"] = docs
	resp["executionTime"] = now.Truncate(time.Microsecond).Format(fsTimestampLayout)
	sim.WriteJSON(w, http.StatusOK, resp)
}
