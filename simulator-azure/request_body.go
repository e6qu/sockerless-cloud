package main

import (
	"io"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// io.EOF refuses an empty body, which sim.ReadJSON would read as an empty
// request.
func azureReadRequiredJSON(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return io.EOF
	}
	return sim.ReadJSON(r, v)
}
