package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"google.golang.org/genproto/googleapis/rpc/code"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GCPError writes a GCP-style JSON error response.
//
// GCP error format:
//
//	{"error": {"code": 404, "message": "details", "status": "NOT_FOUND", "details": []}}
func GCPError(w http.ResponseWriter, code int, message string, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"status":  status,
			"details": []any{},
		},
	})
}

// GCPErrorf writes a GCP-style error with a formatted message.
func GCPErrorf(w http.ResponseWriter, code int, status string, format string, args ...any) {
	GCPError(w, code, fmt.Sprintf(format, args...), status)
}

// gcpHTTPStatus is the HTTP status google.rpc.Code documents for each code.
var gcpHTTPStatus = map[codes.Code]int{
	codes.Canceled:           499,
	codes.Unknown:            http.StatusInternalServerError,
	codes.InvalidArgument:    http.StatusBadRequest,
	codes.DeadlineExceeded:   http.StatusGatewayTimeout,
	codes.NotFound:           http.StatusNotFound,
	codes.AlreadyExists:      http.StatusConflict,
	codes.PermissionDenied:   http.StatusForbidden,
	codes.Unauthenticated:    http.StatusUnauthorized,
	codes.ResourceExhausted:  http.StatusTooManyRequests,
	codes.FailedPrecondition: http.StatusBadRequest,
	codes.Aborted:            http.StatusConflict,
	codes.OutOfRange:         http.StatusBadRequest,
	codes.Unimplemented:      http.StatusNotImplemented,
	codes.Internal:           http.StatusInternalServerError,
	codes.Unavailable:        http.StatusServiceUnavailable,
	codes.DataLoss:           http.StatusInternalServerError,
}

// GCPStatusError writes a gRPC status error as the JSON error the same method
// answers over REST, so logic shared by both surfaces fails the same way on
// each.
func GCPStatusError(w http.ResponseWriter, err error) {
	st, ok := status.FromError(err)
	httpStatus, mapped := gcpHTTPStatus[st.Code()]
	if !ok || !mapped {
		GCPError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	GCPError(w, httpStatus, st.Message(), code.Code_name[int32(st.Code())])
}
