package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	sppb "cloud.google.com/go/spanner/apiv1/spannerpb"
	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var spannerRESTData = &spannerDataGRPC{}

func spannerRESTDatabaseName(r *http.Request) string {
	return spannerDatabaseName(
		sim.PathParam(r, "project"),
		spannerPathPart(r, "instance", 0),
		spannerPathPart(r, "database", 2),
	)
}

func spannerRESTSessionName(r *http.Request) (string, string) {
	value := spannerPathPart(r, "session", 4)
	session, action, _ := strings.Cut(value, ":")
	return spannerSessionName(
		sim.PathParam(r, "project"),
		spannerPathPart(r, "instance", 0),
		spannerPathPart(r, "database", 2),
		session,
	), action
}

func spannerReadRESTProto(w http.ResponseWriter, r *http.Request, message proto.Message) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "read Cloud Spanner request body: %v", err)
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(body, message); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "decode Cloud Spanner request body: %v", err)
		return false
	}
	return true
}

func spannerWriteRESTProto(w http.ResponseWriter, message proto.Message) {
	body, err := (protojson.MarshalOptions{}).Marshal(message)
	if err != nil {
		sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "encode Cloud Spanner response: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func spannerWriteRESTError(w http.ResponseWriter, err error) {
	st := status.Convert(err)
	httpStatus := http.StatusInternalServerError
	switch st.Code() {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange:
		httpStatus = http.StatusBadRequest
	case codes.Unauthenticated:
		httpStatus = http.StatusUnauthorized
	case codes.PermissionDenied:
		httpStatus = http.StatusForbidden
	case codes.NotFound:
		httpStatus = http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted:
		httpStatus = http.StatusConflict
	case codes.Unimplemented:
		httpStatus = http.StatusNotImplemented
	case codes.Unavailable:
		httpStatus = http.StatusServiceUnavailable
	}
	sim.GCPError(w, httpStatus, st.Message(), st.Code().String())
}

func handleSpannerBatchCreateSessionsREST(w http.ResponseWriter, r *http.Request) {
	req := &sppb.BatchCreateSessionsRequest{}
	if !spannerReadRESTProto(w, r, req) {
		return
	}
	req.Database = spannerRESTDatabaseName(r)
	resp, err := spannerRESTData.BatchCreateSessions(r.Context(), req)
	if err != nil {
		spannerWriteRESTError(w, err)
		return
	}
	spannerWriteRESTProto(w, resp)
}

func handleSpannerSessionActionREST(w http.ResponseWriter, r *http.Request) {
	session, action := spannerRESTSessionName(r)
	switch action {
	case "executeSql", "executeStreamingSql":
		req := &sppb.ExecuteSqlRequest{}
		if !spannerReadRESTProto(w, r, req) {
			return
		}
		req.Session = session
		resp, err := spannerRESTData.ExecuteSql(r.Context(), req)
		if err != nil {
			spannerWriteRESTError(w, err)
			return
		}
		if action == "executeStreamingSql" {
			spannerWriteRESTProto(w, spannerResultSetToPartial(resp))
			return
		}
		spannerWriteRESTProto(w, resp)
	case "executeBatchDml":
		req := &sppb.ExecuteBatchDmlRequest{}
		if !spannerReadRESTProto(w, r, req) {
			return
		}
		req.Session = session
		resp, err := spannerRESTData.ExecuteBatchDml(r.Context(), req)
		if err != nil {
			spannerWriteRESTError(w, err)
			return
		}
		spannerWriteRESTProto(w, resp)
	case "read", "streamingRead":
		req := &sppb.ReadRequest{}
		if !spannerReadRESTProto(w, r, req) {
			return
		}
		req.Session = session
		resp, err := spannerRESTData.Read(r.Context(), req)
		if err != nil {
			spannerWriteRESTError(w, err)
			return
		}
		if action == "streamingRead" {
			spannerWriteRESTProto(w, spannerResultSetToPartial(resp))
			return
		}
		spannerWriteRESTProto(w, resp)
	case "beginTransaction":
		req := &sppb.BeginTransactionRequest{}
		if !spannerReadRESTProto(w, r, req) {
			return
		}
		req.Session = session
		resp, err := spannerRESTData.BeginTransaction(r.Context(), req)
		if err != nil {
			spannerWriteRESTError(w, err)
			return
		}
		spannerWriteRESTProto(w, resp)
	case "commit":
		req := &sppb.CommitRequest{}
		if !spannerReadRESTProto(w, r, req) {
			return
		}
		req.Session = session
		resp, err := spannerRESTData.Commit(r.Context(), req)
		if err != nil {
			spannerWriteRESTError(w, err)
			return
		}
		spannerWriteRESTProto(w, resp)
	case "rollback":
		req := &sppb.RollbackRequest{}
		if !spannerReadRESTProto(w, r, req) {
			return
		}
		req.Session = session
		resp, err := spannerRESTData.Rollback(r.Context(), req)
		if err != nil {
			spannerWriteRESTError(w, err)
			return
		}
		spannerWriteRESTProto(w, resp)
	case "partitionQuery":
		req := &sppb.PartitionQueryRequest{}
		if !spannerReadRESTProto(w, r, req) {
			return
		}
		req.Session = session
		resp, err := spannerRESTData.PartitionQuery(r.Context(), req)
		if err != nil {
			spannerWriteRESTError(w, err)
			return
		}
		spannerWriteRESTProto(w, resp)
	case "partitionRead":
		req := &sppb.PartitionReadRequest{}
		if !spannerReadRESTProto(w, r, req) {
			return
		}
		req.Session = session
		resp, err := spannerRESTData.PartitionRead(r.Context(), req)
		if err != nil {
			spannerWriteRESTError(w, err)
			return
		}
		spannerWriteRESTProto(w, resp)
	case "batchWrite":
		handleSpannerBatchWriteREST(w, r, session)
	case "adaptMessage":
		handleSpannerAdaptMessage(w, r, session)
	default:
		gcpMethodNotFound(w)
	}
}

// handleSpannerAdaptMessage carries one message of a PostgreSQL wire session
// through the adapter. The reply reports the session state after the message,
// and the payload it carries back is the adapter's — there is no PostgreSQL
// backend behind this session to answer for, so it says the message was taken
// and the session is at its end rather than inventing a result set.
func handleSpannerAdaptMessage(w http.ResponseWriter, r *http.Request, session string) {
	var req struct {
		Protocol string `json:"protocol"`
		Payload  string `json:"payload"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.Protocol == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"adaptMessage needs the protocol the message is in")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"stateUpdates": map[string]any{"session": session},
		"last":         true,
	})
}

// handleSpannerCreateAdapterSession opens a session a wire-protocol adapter
// drives. It is a session like any other — the adapter is how it is spoken to,
// not what it is — so it is created in the same store, and a database that does
// not exist cannot hold one.
func handleSpannerCreateAdapterSession(w http.ResponseWriter, r *http.Request, database string) {
	if _, ok := spannerDatabases.Get(database); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", database)
		return
	}
	name := fmt.Sprintf("%s/sessions/adapter-%s", database, generateUUID()[:8])
	sim.WriteJSON(w, http.StatusOK, map[string]any{"name": name})
}

func handleSpannerBatchWriteREST(w http.ResponseWriter, r *http.Request, session string) {
	req := &sppb.BatchWriteRequest{}
	if !spannerReadRESTProto(w, r, req) {
		return
	}
	req.Session = session
	dbName, err := spannerSessionDatabase(session)
	if err != nil {
		spannerWriteRESTError(w, err)
		return
	}
	b, err := spannerBackendFor(dbName)
	if err != nil {
		spannerWriteRESTError(w, err)
		return
	}
	if len(req.GetMutationGroups()) == 0 {
		spannerWriteRESTError(w, status.Error(codes.InvalidArgument, "at least one mutation group is required"))
		return
	}
	combined := spannerApplyMutationGroup(r.Context(), b, 0, req.GetMutationGroups()[0])
	for i := 1; i < len(req.GetMutationGroups()); i++ {
		next := spannerApplyMutationGroup(r.Context(), b, i, req.GetMutationGroups()[i])
		if next.GetStatus().GetCode() != int32(codes.OK) {
			combined = next
			break
		}
		combined.Indexes = append(combined.Indexes, next.GetIndexes()...)
		combined.CommitTimestamp = next.GetCommitTimestamp()
	}
	spannerWriteRESTProto(w, combined)
}
