package main

import (
	"net/url"
	"testing"
)

func TestS3ObjectURLEscapesTheKey(t *testing.T) {
	got := s3ObjectURL("172.17.0.1:4566", "source-bucket", "reports/2026 q3/100%#final?.csv")
	want := "http://172.17.0.1:4566/source-bucket/reports/2026%20q3/100%25%23final%3F.csv"
	if got != want {
		t.Fatalf("s3ObjectURL = %q, want %q", got, want)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/source-bucket/reports/2026 q3/100%#final?.csv" {
		t.Fatalf("round-tripped path = %q", parsed.Path)
	}
}
