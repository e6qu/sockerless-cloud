package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The gate reads a body it can use, and never holds an upload larger than it
// would ever read — the handler still gets every byte.
func TestRequestBodyIsHeldOnlyUpToTheLimit(t *testing.T) {
	small := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"TableName":"t"}`))
	if got := string(iamRequestBody(small)); got != `{"TableName":"t"}` {
		t.Fatalf("held %q, want the whole body", got)
	}
	if rest, _ := io.ReadAll(small.Body); string(rest) != `{"TableName":"t"}` {
		t.Fatalf("the handler would read %q", rest)
	}

	upload := httptest.NewRequest(http.MethodPut, "/bucket/key", strings.NewReader(strings.Repeat("x", iamConditionBodyLimit+10)))
	if held := iamRequestBody(upload); held != nil {
		t.Fatalf("held %d bytes of an upload past the limit", len(held))
	}
	rest, err := io.ReadAll(upload.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != iamConditionBodyLimit+10 {
		t.Fatalf("the handler would read %d bytes, want %d", len(rest), iamConditionBodyLimit+10)
	}
}
