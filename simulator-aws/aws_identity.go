package main

import "os"

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

// awsAvailabilityZone returns the canonical first AZ for the configured
// region. Real AWS exposes one AZ per (region, AZ-letter) suffix; for
// the sim's single-AZ default we return `<region>a`.
func awsAvailabilityZone() string {
	return awsRegion() + "a"
}
