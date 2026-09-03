package main

import (
	"encoding/json"
	"net/http"
)

// The wire name of the message member on a service's access-denied exception.
//
// awsJson serialises a member under the name the model declares for it, and
// AWS's services do not agree on that name: about half spell the member
// `message` and half `Message`, and a few services spell it differently on
// different exceptions. A client reads the name its own model declares, so a
// denial written under the other spelling arrives with no message at all —
// which is what the AWS SDK for Go showed for AWS Organizations, an
// AccessDeniedException whose reason was on the wire and unreadable.
//
// The table holds the spelling each service's access-denied exception declares.
// TestIAMDenyMessageKeysMatchTheVendoredModels reads the models and fails if an
// entry disagrees with one, or if a service the simulator denies for is missing.
var iamDenyMessageKeys = map[string]string{
	"acm":           "Message",
	"amplify":       "message",
	"apigateway":    "message",
	"budgets":       "Message",
	"cloudfront":    "Message",
	"cloudtrail":    "Message",
	"cloudwatch":    "Message",
	"logs":          "message",
	"ecs":           "message",
	"events":        "message",
	"glue":          "Message",
	"kinesis":       "message",
	"lambda":        "Message",
	"organizations": "Message",
	"states":        "message",
	"sns":           "message",
	"sqs":           "message",
	"ssm":           "Message",
}

// iamDenyMessageKey is the member name a service's access-denied exception
// carries its message under. Services whose model this project does not vendor
// take `message`, which is the spelling the majority of AWS's models declare.
func iamDenyMessageKey(service string) string {
	if key, ok := iamDenyMessageKeys[service]; ok {
		return key
	}
	return "message"
}

// iamWriteJSONDeny writes an awsJson access-denied response under the member
// name the service's own model declares.
func iamWriteJSONDeny(w http.ResponseWriter, service, code, message string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"__type":                   code,
		iamDenyMessageKey(service): message,
	})
}
