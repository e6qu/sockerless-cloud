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
