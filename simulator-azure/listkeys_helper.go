package main

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
)

// simListKey32 derives a 44-character base64 32-byte SAS key from the resource
// ID and key kind, the shape of Service Bus, Event Hubs, Redis and Cosmos DB
// read-only keys, stable per resource across PUT, listKeys and connect.
func simListKey32(resourceID, kind string) string {
	sum := sha256.Sum256([]byte("sim-listkey-32|" + resourceID + "|" + kind))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// simListKey64 derives an 88-character base64 512-bit Storage account key,
// unique per resource.
func simListKey64(resourceID, kind string) string {
	sum := sha512.Sum512([]byte("sim-listkey-64|" + resourceID + "|" + kind))
	return base64.StdEncoding.EncodeToString(sum[:])
}
