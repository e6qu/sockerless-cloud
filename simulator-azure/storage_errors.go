package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
)

// writeTableODataError writes an Azure Tables data-plane error in the OData
// envelope real Azure Tables uses: {"odata.error":{"code":...,"message":
// {"lang":"en-US","value":...}}}. The aztables SDK (via azcore) reads the code
// from odata.error.code, so the ARM {"error":{...}} envelope yields the wrong
// ErrorCode. The x-ms-error-code header is also set; azcore prefers it.
func writeTableODataError(w http.ResponseWriter, code, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-ms-error-code", code)
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"odata.error": map[string]any{
			"code": code,
			"message": map[string]string{
				"lang":  "en-US",
				"value": message,
			},
		},
	})
}

type storageErrorResponse struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func writeStorageError(w http.ResponseWriter, code, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("x-ms-error-code", code)
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(storageErrorResponse{
		Code:    code,
		Message: message,
	})
}

// writeStorageOperationNotImplemented declares that the simulator has no
// handler for the Azure Storage data-plane operation this request names.
//
// The Blob / Files / Queues data planes select an operation from the
// `restype` + `comp` query pair rather than from the path, and they dispatch
// inside a host-addressed middleware instead of through the mux — so "nothing
// is mounted here" has to be spoken in the response. Answering a sibling
// handler instead is how a `PUT /{container}/{blob}?comp=tier` (Set Blob Tier)
// ends up creating a blob and reporting 201 Created: the caller is told an
// operation succeeded that the simulator never performed.
//
// The answer is a storage-shaped XML error carrying the machine-readable code
// in `x-ms-error-code`, exactly like every other error these planes emit, so a
// real SDK surfaces it as a typed failure. The simulator does not distinguish a
// `comp` value Azure itself rejects from one Azure serves and the simulator does
// not — both are gaps from the caller's point of view, and the message names the
// exact coordinate that went unserved.
func writeStorageOperationNotImplemented(w http.ResponseWriter, r *http.Request, plane string) {
	q := r.URL.Query()
	var parts []string
	if v := q.Get("restype"); v != "" {
		parts = append(parts, "restype="+v)
	}
	if v := q.Get("comp"); v != "" {
		parts = append(parts, "comp="+v)
	}
	discriminator := "no restype/comp"
	if len(parts) > 0 {
		discriminator = strings.Join(parts, "&")
	}
	writeStorageError(w, "NotImplemented", fmt.Sprintf(
		"Azure Storage %s data plane: %s %s (%s) is not implemented by the simulator",
		plane, r.Method, r.URL.Path, discriminator), http.StatusNotImplemented)
}

func writeStorageXML(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(v)
}
