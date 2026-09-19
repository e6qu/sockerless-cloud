package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// BUG-3030. aws-sdk-go-v2 1.47 replaced the generated JSON deserializers with
// schema-driven ones that match the modelled member exactly, where the old
// ones matched the key case-insensitively. The vendored models spell the
// message member "message" on 760 exception shapes and "Message" on 677, so
// one spelling leaves half of AWS reading an empty message. Both are written
// until the spelling is looked up per service.
func TestAWSError_CarriesTheMessageUnderBothModelledSpellings(t *testing.T) {
	recorder := httptest.NewRecorder()
	AWSError(recorder, "InvalidParameterException", "PasswordLength must be between 4 and 4096", 400)
	if recorder.Code != 400 {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v", err)
	}
	if body["__type"] != "InvalidParameterException" {
		t.Fatalf("__type = %q", body["__type"])
	}
	for _, spelling := range []string{"message", "Message"} {
		if body[spelling] != "PasswordLength must be between 4 and 4096" {
			t.Fatalf("%s = %q, want the message", spelling, body[spelling])
		}
	}
}
