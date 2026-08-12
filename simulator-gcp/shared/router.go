package simulator

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxQueryJSONBody bounds a control-plane JSON request body so a
// maliciously large payload can't OOM the simulator. Control-plane requests
// are small; this is generous headroom, matching the data-plane cap's intent.
const maxQueryJSONBody = 64 << 20 // 64 MiB

// ReadJSON reads and decodes a JSON request body into the given value. The read
// is capped at maxQueryJSONBody so an unbounded body can't exhaust memory.
func ReadJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxQueryJSONBody+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > maxQueryJSONBody {
		return fmt.Errorf("request body exceeds %d bytes", maxQueryJSONBody)
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}

// PathParam extracts a path parameter from the request using Go 1.22+ routing.
func PathParam(r *http.Request, name string) string {
	return r.PathValue(name)
}
