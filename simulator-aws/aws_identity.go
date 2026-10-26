package main

import (
	"net/http"
	"os"
)

// AWS reads the Region from the endpoint host and the account from the
// credentials. The simulator serves every Region on one port, so both come
// from configuration.
func awsRegion() string {
	if r := os.Getenv("SOCKERLESS_AWS_REGION"); r != "" {
		return r
	}
	return "us-east-1"
}

func awsAccountID() string {
	if a := os.Getenv("SOCKERLESS_AWS_ACCOUNT_ID"); a != "" {
		return a
	}
	return "123456789012"
}

// The simulator's own service credential maps to the account root.
func awsCallerArn(r *http.Request) string {
	if arn, _, ok := stsCallerIdentity(r); ok {
		return arn
	}
	return "arn:aws:iam::" + awsAccountID() + ":root"
}

func awsAvailabilityZones() []string {
	region := awsRegion()
	return []string{region + "a", region + "b", region + "c"}
}

// A resource whose request names no Availability Zone lands in the first.
func awsAvailabilityZone() string {
	return awsAvailabilityZones()[0]
}
