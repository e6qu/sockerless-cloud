package main

import (
	"io"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// azureReadRequiredJSON decodes the body of an operation that requires one,
// refusing an empty body with io.EOF, which sim.ReadJSON reads as an empty
// request.
func azureReadRequiredJSON(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return io.EOF
	}
	return sim.ReadJSON(r, v)
}
