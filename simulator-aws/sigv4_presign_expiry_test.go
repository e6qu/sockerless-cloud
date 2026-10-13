package main

import (
	"net/url"
	"testing"
	"time"
)

func TestSigV4PresignedExpiredCountsFromXAmzDate(t *testing.T) {
	t.Parallel()
	signed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	q := url.Values{
		"X-Amz-Signature": {"0123"},
		"X-Amz-Date":      {signed.Format("20060102T150405Z")},
		"X-Amz-Expires":   {"60"},
	}
	if sigv4PresignedExpired(q, signed.Add(60*time.Second)) {
		t.Fatal("a URL is valid through the last second of its lifetime")
	}
	if !sigv4PresignedExpired(q, signed.Add(61*time.Second)) {
		t.Fatal("a URL past X-Amz-Date plus X-Amz-Expires has expired")
	}
	header := url.Values{"X-Amz-Date": q["X-Amz-Date"], "X-Amz-Expires": q["X-Amz-Expires"]}
	if sigv4PresignedExpired(header, signed.Add(time.Hour)) {
		t.Fatal("only a query-signed request carries a presigned lifetime")
	}
}
