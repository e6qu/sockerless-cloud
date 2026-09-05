package main

import (
	"encoding/json"
	"fmt"
	"net/http"
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
