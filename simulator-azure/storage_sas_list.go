package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ListAccountSas and ListServiceSas: Azure Resource Manager signs a Shared
// Access Signature with one of the account's keys from the parameters the
// caller names, so a caller holding only an ARM token can reach the data
// plane. The token is the query string a client appends to a data-plane URL;
// the data plane verifies it like any SAS the account's keys signed.

// storageListSASVersion is the service version ARM signs its tokens at.
const storageListSASVersion = "2022-11-02"

func storageListSASTime(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "", fmt.Errorf("the time %q is not an ISO 8601 date-time", raw)
	}
	return at.UTC().Format("2006-01-02T15:04:05Z"), nil
}

// storageSignWithKey signs a string-to-sign with the account key keyToSign
// names, key1 when it names none.
func storageSignWithKey(acctID, keyToSign, stringToSign string) (string, error) {
	keyName := "key1"
	if keyToSign != "" {
		keyName = strings.ToLower(keyToSign)
	}
	if keyName != "key1" && keyName != "key2" {
		return "", fmt.Errorf("keyToSign must be key1 or key2, not %q", keyToSign)
	}
	material, err := base64.StdEncoding.DecodeString(azureKeyMaterial64(acctID, keyName))
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, material)
	_, _ = mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

func storageSASInvalid(w http.ResponseWriter, err error) {
	AzureError(w, "InvalidValuesForRequestParameters", err.Error(), http.StatusBadRequest)
}

func handleStorageListAccountSAS(w http.ResponseWriter, r *http.Request) {
	acctID, account, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	var params struct {
		SignedServices      string `json:"signedServices"`
		SignedResourceTypes string `json:"signedResourceTypes"`
		SignedPermission    string `json:"signedPermission"`
		SignedIP            string `json:"signedIp"`
		SignedProtocol      string `json:"signedProtocol"`
		SignedStart         string `json:"signedStart"`
		SignedExpiry        string `json:"signedExpiry"`
		KeyToSign           string `json:"keyToSign"`
	}
	if err := sim.ReadJSON(r, &params); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if params.SignedServices == "" || params.SignedResourceTypes == "" || params.SignedPermission == "" || params.SignedExpiry == "" {
		storageSASInvalid(w, fmt.Errorf("signedServices, signedResourceTypes, signedPermission and signedExpiry are required"))
		return
	}
	start, err := storageListSASTime(params.SignedStart)
	if err != nil {
		storageSASInvalid(w, err)
		return
	}
	expiry, err := storageListSASTime(params.SignedExpiry)
	if err != nil {
		storageSASInvalid(w, err)
		return
	}
	q := url.Values{}
	q.Set("sv", storageListSASVersion)
	q.Set("ss", params.SignedServices)
	q.Set("srt", params.SignedResourceTypes)
	q.Set("sp", params.SignedPermission)
	q.Set("se", expiry)
	if start != "" {
		q.Set("st", start)
	}
	if params.SignedIP != "" {
		q.Set("sip", params.SignedIP)
	}
	if params.SignedProtocol != "" {
		q.Set("spr", params.SignedProtocol)
	}
	sig, err := storageSignWithKey(acctID, params.KeyToSign, blobAccountSASStringToSign(strings.ToLower(account), q))
	if err != nil {
		storageSASInvalid(w, err)
		return
	}
	q.Set("sig", sig)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"accountSasToken": q.Encode()})
}

func handleStorageListServiceSAS(w http.ResponseWriter, r *http.Request) {
	acctID, account, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	var params struct {
		CanonicalizedResource string `json:"canonicalizedResource"`
		SignedResource        string `json:"signedResource"`
		SignedPermission      string `json:"signedPermission"`
		SignedIP              string `json:"signedIp"`
		SignedProtocol        string `json:"signedProtocol"`
		SignedStart           string `json:"signedStart"`
		SignedExpiry          string `json:"signedExpiry"`
		SignedIdentifier      string `json:"signedIdentifier"`
		KeyToSign             string `json:"keyToSign"`
		CacheControl          string `json:"rscc"`
		ContentDisposition    string `json:"rscd"`
		ContentEncoding       string `json:"rsce"`
		ContentLanguage       string `json:"rscl"`
		ContentType           string `json:"rsct"`
	}
	if err := sim.ReadJSON(r, &params); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	// The canonicalized resource is /{service}/{account}/{container or share
	// or queue}[/{path}].
	parts := strings.SplitN(strings.TrimPrefix(params.CanonicalizedResource, "/"), "/", 4)
	if len(parts) < 3 || !strings.EqualFold(parts[1], account) {
		storageSASInvalid(w, fmt.Errorf("canonicalizedResource %q must name /{service}/%s/{container}", params.CanonicalizedResource, account))
		return
	}
	service := strings.ToLower(parts[0])
	if service != "blob" && service != "file" && service != "queue" {
		storageSASInvalid(w, fmt.Errorf("canonicalizedResource names the service %q, which a service SAS cannot sign", parts[0]))
		return
	}
	if params.SignedExpiry == "" && params.SignedIdentifier == "" {
		storageSASInvalid(w, fmt.Errorf("signedExpiry is required unless signedIdentifier names a stored access policy"))
		return
	}
	start, err := storageListSASTime(params.SignedStart)
	if err != nil {
		storageSASInvalid(w, err)
		return
	}
	expiry, err := storageListSASTime(params.SignedExpiry)
	if err != nil {
		storageSASInvalid(w, err)
		return
	}
	q := url.Values{}
	q.Set("sv", storageListSASVersion)
	set := func(key, value string) {
		if value != "" {
			q.Set(key, value)
		}
	}
	set("sr", params.SignedResource)
	set("sp", params.SignedPermission)
	set("st", start)
	set("se", expiry)
	set("si", params.SignedIdentifier)
	set("sip", params.SignedIP)
	set("spr", params.SignedProtocol)
	set("rscc", params.CacheControl)
	set("rscd", params.ContentDisposition)
	set("rsce", params.ContentEncoding)
	set("rscl", params.ContentLanguage)
	set("rsct", params.ContentType)
	path := ""
	if len(parts) == 4 {
		path = parts[3]
	}
	sig, err := storageSignWithKey(acctID, params.KeyToSign, blobSASStringToSign(service, strings.ToLower(account), parts[2], path, q))
	if err != nil {
		storageSASInvalid(w, err)
		return
	}
	q.Set("sig", sig)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"serviceSasToken": q.Encode()})
}
