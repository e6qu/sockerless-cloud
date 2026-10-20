package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestS3ExpressConditionKeysReadTheSessionMode(t *testing.T) {
	for _, tc := range []struct{ header, want string }{
		{"ReadOnly", "ReadOnly"},
		{"ReadWrite", "ReadWrite"},
		{"", "ReadWrite"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/?session", nil)
		if tc.header != "" {
			r.Header.Set("x-amz-create-session-mode", tc.header)
		}
		assertPopulatedConditionValues(t, populatedConditionContext(r, "s3express", "CreateSession", ""),
			map[string][]string{"s3express:SessionMode": {tc.want}})
	}
}
