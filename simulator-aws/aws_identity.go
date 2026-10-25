package main

import (
	"net/http"
	"os"
)

// AWS identity helpers. Real AWS reads the region from the endpoint
// host (`<service>.<region>.amazonaws.com`) and the account from
// caller credentials (`sts:GetCallerIdentity`). The simulator listens
// on a single port for every region+service, so the region and account
// are operator-configurable via env vars; defaults match the
// long-standing sim placeholders so existing test fixtures keep
// working without explicit override.
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

// awsCallerArn is the principal a service records as a resource's creator or
// modifier: the identity that signed the request, or the account root for the
// simulator's own service credential.
func awsCallerArn(r *http.Request) string {
	if arn, _, ok := stsCallerIdentity(r); ok {
		return arn
	}
	return "arn:aws:iam::" + awsAccountID() + ":root"
}

// awsAvailabilityZones lists the configured Region's Availability Zones.
func awsAvailabilityZones() []string {
	region := awsRegion()
	return []string{region + "a", region + "b", region + "c"}
}

// awsAvailabilityZone returns the Region's first Availability Zone, where a
// resource lands when its request names none.
func awsAvailabilityZone() string {
	return awsAvailabilityZones()[0]
}
