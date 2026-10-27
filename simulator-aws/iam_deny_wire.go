package main

import (
	"net/http"
)

// awsJson serialises a member under the name the model declares, and AWS
// services disagree on the access-denied message member: about half spell it
// `message`, half `Message`, and a few vary by exception. A client reads its own
// model's spelling, so a denial under the other one arrives with no message.
// TestIAMDenyMessageKeysMatchTheVendoredModels holds this table to the vendored
// models.
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

// iamDenyMessageKey falls back to `message`, the spelling most AWS models
// declare, for a service whose model the project does not vendor.
func iamDenyMessageKey(service string) string {
	if key, ok := iamDenyMessageKeys[service]; ok {
		return key
	}
	return "message"
}

func iamWriteJSONDeny(w http.ResponseWriter, service, code, message string) {
	awsWriteJSONErrorBody(w, awsJSON11, http.StatusForbidden, code, map[string]string{
		"__type":                   code,
		iamDenyMessageKey(service): message,
	})
}
