package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// AzureError writes the Azure Resource Manager error shape:
//
//	{"error": {"code": "ResourceNotFound", "message": "details"}}
func AzureError(w http.ResponseWriter, code string, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func AzureErrorf(w http.ResponseWriter, code string, statusCode int, format string, args ...any) {
	AzureError(w, code, fmt.Sprintf(format, args...), statusCode)
}
