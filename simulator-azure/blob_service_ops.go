package main

import (
	"encoding/xml"
	"net/http"
	"strings"
	"time"
)

// Account-wide Blob service operations: the service properties document, the
// geo-replication statistics, the user delegation key, and Get Account
// Information (which the specification addresses at three levels — service,
// container and blob — all answering the same account facts).

func handleGetBlobServiceProperties(w http.ResponseWriter, r *http.Request, account string) {
	writeStorageXML(w, http.StatusOK, blobServiceProperties(account))
}

func handleSetBlobServiceProperties(w http.ResponseWriter, r *http.Request, account string) {
	defer r.Body.Close()
	var props BlobServiceProperties
	if err := xml.NewDecoder(r.Body).Decode(&props); err != nil {
		writeStorageError(w, "InvalidXmlDocument",
			"XML specified is not syntactically valid.", http.StatusBadRequest)
		return
	}
	merged := mergeBlobServiceProperties(blobServiceProperties(account), props)
	blobServicePropsStore.Put(account, BlobServiceConfig{Account: account, Properties: merged})
	w.WriteHeader(http.StatusAccepted)
}

// blobServiceStats is the StorageServiceStats document Get Blob Service
// Statistics returns from the secondary endpoint.
type blobServiceStats struct {
	XMLName        xml.Name           `xml:"StorageServiceStats"`
	GeoReplication blobGeoReplication `xml:"GeoReplication"`
}

type blobGeoReplication struct {
	Status       string `xml:"Status"`
	LastSyncTime string `xml:"LastSyncTime"`
}

func handleGetBlobServiceStatistics(w http.ResponseWriter, r *http.Request, account string) {
	// The simulator serves one replica, so every primary write is immediately
	// readable and the last sync time is now.
	writeStorageXML(w, http.StatusOK, blobServiceStats{
		GeoReplication: blobGeoReplication{
			Status:       "live",
			LastSyncTime: time.Now().UTC().Format(http.TimeFormat),
		},
	})
}

// handleBlobGetAccountInfo answers Get Account Information at any of the three
// coordinates the specification defines it at.
func handleBlobGetAccountInfo(w http.ResponseWriter, r *http.Request, account string) {
	w.Header().Set("x-ms-sku-name", "Standard_LRS")
	w.Header().Set("x-ms-account-kind", "StorageV2")
	w.Header().Set("x-ms-is-hns-enabled", "false")
	w.WriteHeader(http.StatusOK)
}

// blobKeyInfoDocument is the <KeyInfo> body Get User Delegation Key takes.
type blobKeyInfoDocument struct {
	XMLName xml.Name `xml:"KeyInfo"`
	Start   string   `xml:"Start"`
	Expiry  string   `xml:"Expiry"`
}

// blobUserDelegationKeyDocument is the <UserDelegationKey> the operation
// returns.
type blobUserDelegationKeyDocument struct {
	XMLName       xml.Name `xml:"UserDelegationKey"`
	SignedOid     string   `xml:"SignedOid"`
	SignedTid     string   `xml:"SignedTid"`
	SignedStart   string   `xml:"SignedStart"`
	SignedExpiry  string   `xml:"SignedExpiry"`
	SignedService string   `xml:"SignedService"`
	SignedVersion string   `xml:"SignedVersion"`
	Value         string   `xml:"Value"`
}

// handleGetUserDelegationKey issues a user delegation key for the Microsoft
// Entra identity the request authenticates as. Real Azure derives the key's
// identity claims from the OAuth token — a shared-key request is refused —
// so the simulator reads the same claims out of the bearer it minted, and
// retains the issued key so the same coordinates read the same key back.
func handleGetUserDelegationKey(w http.ResponseWriter, r *http.Request, account string) {
	defer r.Body.Close()
	var info blobKeyInfoDocument
	if err := xml.NewDecoder(r.Body).Decode(&info); err != nil {
		writeStorageError(w, "InvalidXmlDocument",
			"XML specified is not syntactically valid.", http.StatusBadRequest)
		return
	}
	if info.Start == "" || info.Expiry == "" {
		writeStorageError(w, "InvalidXmlNodeValue",
			"The value for one of the XML nodes is not in the correct format: Start/Expiry.",
			http.StatusBadRequest)
		return
	}

	key, ok := storageIssueUserDelegationKey(w, r, account, "b", info.Start, info.Expiry)
	if !ok {
		return
	}

	writeStorageXML(w, http.StatusOK, blobUserDelegationKeyDocument{
		SignedOid:     key.SignedOID,
		SignedTid:     key.SignedTID,
		SignedStart:   key.SignedStart,
		SignedExpiry:  key.SignedExpiry,
		SignedService: key.SignedService,
		SignedVersion: key.SignedVersion,
		Value:         key.Value,
	})
}

// storageIssueUserDelegationKey issues a user delegation key for the Microsoft
// Entra identity the request authenticates as, bound to the service named by
// its letter and to the requested window, and retains it so the same
// coordinates read the same key back and a signature made with it verifies.
func storageIssueUserDelegationKey(w http.ResponseWriter, r *http.Request, account, service, start, expiry string) (BlobUserDelegationKey, bool) {
	caller, ok := storageBearerCaller(r)
	if !ok || caller.oid == "" {
		writeStorageError(w, "AuthenticationFailed",
			"Get User Delegation Key requires OAuth authentication with a Microsoft Entra token.",
			http.StatusUnauthorized)
		return BlobUserDelegationKey{}, false
	}
	version := r.Header.Get("x-ms-version")
	key := BlobUserDelegationKey{
		Account:       account,
		SignedOID:     caller.oid,
		SignedTID:     caller.tid,
		SignedStart:   start,
		SignedExpiry:  expiry,
		SignedService: service,
		SignedVersion: version,
	}
	storeKey := strings.Join([]string{account, caller.oid, caller.tid, start, expiry, version, service}, "|")
	if existing, ok := blobDelegationKeys.Get(storeKey); ok {
		key.Value = existing.Value
	} else {
		key.Value = blobRandomKeyMaterial()
		blobDelegationKeys.Put(storeKey, key)
	}
	return key, true
}
