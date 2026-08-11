package main

// Fuzz targets for untrusted-input Azure parsers that previously had no fuzz
// coverage: a blob copy-source URL (x-ms-copy-source header), an Azure Container
// Registry image reference, and an AMQP Event Hub address. Contract: never
// panic on any input.

import "testing"

func FuzzParseBlobCopySource(f *testing.F) {
	for _, s := range []string{
		"", "https://acct.blob.core.windows.net/cont/blob",
		"https://acct.blob.core.windows.net/cont/path/to/blob.txt?sig=x",
		"http://x", "://", "https://", "not a url", "https://a.blob.core.windows.net/",
		"https://a.blob.core.windows.net/c", "/cont/blob",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		_, _, _, _ = parseBlobCopySource(raw)
	})
}

func FuzzParseACRImage(f *testing.F) {
	for _, s := range []string{
		"", "img", "img:tag", "repo/img:tag", "acr.azurecr.io/repo:tag",
		"acr.azurecr.io/a/b/c@sha256:" + "a", "@", ":", "/", "a@b:c", "a:b:c",
		"repo@sha256:0123456789abcdef",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, img string) {
		_ = parseACRImage(img)
	})
}

func FuzzEHParseEventHubAddress(f *testing.F) {
	for _, s := range []string{
		"", "hub", "hub/ConsumerGroups/$Default/Partitions/0",
		"hub/Partitions/3", "/", "//", "a/b/c/d/e/f", "hub/Partitions/",
		"hub/Partitions/-1", "hub/Partitions/999999999999999999999",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, address string) {
		_, _, _ = ehAMQPParseEventHubAddress(address)
		_, _, _ = ehAMQPParseConsumerAddress(address)
	})
}
