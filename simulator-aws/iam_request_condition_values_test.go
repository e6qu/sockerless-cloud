package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// requestConditionContext runs every populator registered for service, which
// is the path the gate takes, so a populator that is never registered fails
// here too.
func requestConditionContext(r *http.Request, service, operation, body string) map[string][]string {
	ctx := map[string][]string{}
	iamRunRequestConditionPopulators(r, service, operation, []byte(body), ctx)
	return ctx
}

func jsonConditionRequest(target string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", target)
	return r
}

func queryConditionRequest(form url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func assertConditionValues(t *testing.T, ctx map[string][]string, want map[string][]string) {
	t.Helper()
	for key, values := range want {
		if got := ctx[key]; !reflect.DeepEqual(got, values) {
			t.Errorf("%s = %v, want %v", key, got, values)
		}
	}
}

func assertConditionKeysAbsent(t *testing.T, ctx map[string][]string, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if values, ok := ctx[key]; ok {
			t.Errorf("%s = %v, want it absent", key, values)
		}
	}
}

func TestSetConditionValuesKeepsAKeyAlreadySet(t *testing.T) {
	ctx := map[string][]string{"svc:key": {"first"}}
	iamSetConditionValues(ctx, "svc:key", "second")
	iamSetConditionValues(ctx, "svc:other", "a", "", "b", "a")
	assertConditionValues(t, ctx, map[string][]string{
		"svc:key":   {"first"},
		"svc:other": {"a", "b"},
	})
	iamSetConditionValues(ctx, "svc:empty", "")
	assertConditionKeysAbsent(t, ctx, "svc:empty")
}
