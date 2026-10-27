package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// storageOperation is the Azure RBAC permission a storage data-plane request
// needs from a Microsoft Entra caller, as "Authorize with Microsoft Entra ID"
// lists it for each operation: an action for the service, container, queue,
// share and table operations, a data action for the blob, message, file and
// entity ones.
type storageOperation struct {
	action string
	data   bool
	scope  string
}

// storageAccountRelativePath is the request's path within the account: a
// host-addressed request's whole path, a path-addressed one's after its
// /{service}/{account} or /{account} prefix.
func storageAccountRelativePath(r *http.Request, service, account string) string {
	hostname := strings.ToLower(r.Host)
	path := strings.TrimPrefix(r.URL.Path, "/")
	if strings.Contains(hostname, "."+service+".") {
		return path
	}
	lowerAccount := strings.ToLower(account)
	lower := strings.ToLower(path)
	for _, prefix := range []string{service + "/" + lowerAccount, lowerAccount} {
		if lower == prefix {
			return ""
		}
		if strings.HasPrefix(lower, prefix+"/") {
			return path[len(prefix)+1:]
		}
	}
	return path
}

// storageOperationOf names the permission a request to one of the account's
// data planes needs, and the scope Azure RBAC checks it at.
func storageOperationOf(r *http.Request, service, account string) storageOperation {
	stored, _ := azureStorageAccountByName(account)
	base := stored.ID
	q := r.URL.Query()
	comp, restype := q.Get("comp"), q.Get("restype")
	path := storageAccountRelativePath(r, service, account)
	first, rest, _ := strings.Cut(path, "/")
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	action := func(name, scope string) storageOperation {
		return storageOperation{action: "Microsoft.Storage/storageAccounts/" + name, scope: scope}
	}
	data := func(name, scope string) storageOperation {
		op := action(name, scope)
		op.data = true
		return op
	}
	verb := func() string {
		switch {
		case read:
			return "read"
		case r.Method == http.MethodDelete:
			return "delete"
		default:
			return "write"
		}
	}
	switch service {
	case "blob":
		svc := base + "/blobServices/default"
		if first == "" {
			switch {
			case comp == "userdelegationkey":
				return action("blobServices/generateUserDelegationKey/action", svc)
			case comp == "list":
				return action("blobServices/containers/read", svc)
			case comp == "blobs":
				return data("blobServices/containers/blobs/filter/action", svc)
			case restype == "account":
				return data("blobServices/containers/blobs/read", svc)
			}
			return action("blobServices/"+verb(), svc)
		}
		container := svc + "/containers/" + first
		if rest == "" && restype == "container" {
			switch comp {
			case "list":
				return data("blobServices/containers/blobs/read", container)
			case "blobs":
				return data("blobServices/containers/blobs/filter/action", container)
			case "acl":
				if read {
					return action("blobServices/containers/getAcl/action", container)
				}
				return action("blobServices/containers/setAcl/action", container)
			case "lease":
				return action("blobServices/containers/lease/action", container)
			}
			return action("blobServices/containers/"+verb(), container)
		}
		if rest == "" && restype == "account" {
			return data("blobServices/containers/blobs/read", container)
		}
		switch {
		case comp == "tags" && read:
			return data("blobServices/containers/blobs/tags/read", container)
		case comp == "tags":
			return data("blobServices/containers/blobs/tags/write", container)
		case comp == "query":
			return data("blobServices/containers/blobs/read", container)
		case r.Method == http.MethodDelete && strings.EqualFold(q.Get("deletetype"), "permanent"):
			return data("blobServices/containers/blobs/permanentDelete/action", container)
		}
		return data("blobServices/containers/blobs/"+verb(), container)
	case "queue":
		svc := base + "/queueServices/default"
		if first == "" {
			if comp == "list" {
				return action("queueServices/queues/read", svc)
			}
			return action("queueServices/"+verb(), svc)
		}
		queue := svc + "/queues/" + first
		if rest == "" {
			if comp == "acl" {
				if read {
					return action("queueServices/queues/getAcl/action", queue)
				}
				return action("queueServices/queues/setAcl/action", queue)
			}
			return action("queueServices/queues/"+verb(), queue)
		}
		messages := "queueServices/queues/messages/"
		switch {
		case r.Method == http.MethodPost:
			return data(messages+"add/action", queue)
		case read && strings.EqualFold(q.Get("peekonly"), "true"):
			return data(messages+"read", queue)
		case read:
			return data(messages+"process/action", queue)
		case r.Method == http.MethodDelete && strings.Contains(rest, "/"):
			return data(messages+"process/action", queue)
		case r.Method == http.MethodDelete:
			return data(messages+"delete", queue)
		}
		return data(messages+"write", queue)
	case "table":
		svc := base + "/tableServices/default"
		switch {
		case first == "":
			return action("tableServices/"+verb(), svc)
		case first == "Tables":
			return action("tableServices/tables/"+verb(), svc)
		case strings.HasPrefix(first, "Tables('"):
			name := strings.TrimSuffix(strings.TrimPrefix(first, "Tables('"), "')")
			return action("tableServices/tables/"+verb(), svc+"/tables/"+name)
		case first == "$batch":
			return data("tableServices/tables/entities/write", svc)
		}
		name, _, _ := strings.Cut(first, "(")
		table := svc + "/tables/" + name
		if comp == "acl" {
			if read {
				return action("tableServices/tables/getAcl/action", table)
			}
			return action("tableServices/tables/setAcl/action", table)
		}
		entities := "tableServices/tables/entities/"
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			return data(entities+"read", table)
		case http.MethodPost:
			return data(entities+"add/action", table)
		case http.MethodDelete:
			return data(entities+"delete", table)
		}
		return data(entities+"update/action", table)
	case "file":
		svc := base + "/fileServices/default"
		if first == "" {
			switch comp {
			case "userdelegationkey":
				return action("fileServices/generateUserDelegationKey/action", svc)
			case "list":
				return action("fileServices/shares/read", svc)
			}
			return action("fileServices/"+verb(), svc)
		}
		share := svc + "/fileshares/" + first
		if rest == "" && restype == "share" {
			return action("fileServices/shares/"+verb(), svc+"/shares/"+first)
		}
		return data("fileServices/fileshares/files/"+verb(), share)
	}
	return action(service+"Services/"+verb(), base)
}

// storageBearerAuthorizes reports whether Azure RBAC grants the caller the
// request's operation.
func storageBearerAuthorizes(r *http.Request, service, account string, caller rbacCaller) bool {
	op := storageOperationOf(r, service, account)
	return rbacAllows(caller, op.scope, op.action, op.data)
}

// storageSharedKeyAllowed reports whether the account accepts its keys:
// allowSharedKeyAccess false refuses Shared Key and every signature an
// account key made.
func storageSharedKeyAllowed(account string) bool {
	stored, ok := azureStorageAccountByName(account)
	return !ok || stored.Properties.AllowSharedKeyAccess == nil || *stored.Properties.AllowSharedKeyAccess
}

// storageBearerCaller is the Microsoft Entra principal a request's bearer
// authenticates, when it carries a valid one.
func storageBearerCaller(r *http.Request) (rbacCaller, bool) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, "Bearer ") {
		return rbacCaller{}, false
	}
	claims, err := verifyAzureSimJWT(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")))
	if err != nil {
		return rbacCaller{}, false
	}
	return rbacCallerFromClaims(claims), true
}

// storageUserDelegationStringToSign builds the string a user delegation
// signature signs, in the layout of the service version the signature
// declares.
func storageUserDelegationStringToSign(service, account, container, path string, q url.Values) string {
	canonical := "/" + service + "/" + account + "/" + container
	if path != "" {
		canonical += "/" + path
	}
	version := q.Get("sv")
	fields := []string{
		q.Get("sp"), q.Get("st"), q.Get("se"), canonical,
		q.Get("skoid"), q.Get("sktid"), q.Get("skt"), q.Get("ske"), q.Get("sks"), q.Get("skv"),
	}
	if version >= "2020-02-10" {
		fields = append(fields, q.Get("saoid"), q.Get("suoid"), q.Get("scid"))
	}
	if version >= "2025-07-05" {
		fields = append(fields, q.Get("skdutid"), q.Get("sduoid"))
	}
	fields = append(fields, q.Get("sip"), q.Get("spr"), version, q.Get("sr"))
	if service == "blob" {
		fields = append(fields, blobSASSnapshotField(q))
	} else {
		fields = append(fields, "")
	}
	if version >= "2020-12-06" {
		fields = append(fields, q.Get("ses"))
	}
	if service == "blob" && version >= "2026-04-06" {
		fields = append(fields, q.Get("srh"), q.Get("srq"))
	}
	fields = append(fields, q.Get("rscc"), q.Get("rscd"), q.Get("rsce"), q.Get("rscl"), q.Get("rsct"))
	return strings.Join(fields, "\n")
}

// storageUserDelegationKeyFor finds the key a user delegation signature names
// among the keys the account issued.
func storageUserDelegationKeyFor(account string, q url.Values) (BlobUserDelegationKey, bool) {
	start, okStart := storageSASTime(q.Get("skt"))
	expiry, okExpiry := storageSASTime(q.Get("ske"))
	if !okStart || !okExpiry {
		return BlobUserDelegationKey{}, false
	}
	for _, row := range blobDelegationKeys.ListPrefix(account + "|") {
		key := row.Item
		if !strings.EqualFold(key.SignedOID, q.Get("skoid")) || !strings.EqualFold(key.SignedTID, q.Get("sktid")) ||
			key.SignedService != q.Get("sks") || key.SignedVersion != q.Get("skv") {
			continue
		}
		keyStart, ok1 := storageSASTime(key.SignedStart)
		keyExpiry, ok2 := storageSASTime(key.SignedExpiry)
		if ok1 && ok2 && keyStart.Equal(start) && keyExpiry.Equal(expiry) {
			return key, true
		}
	}
	return BlobUserDelegationKey{}, false
}

// storageUserDelegationSASAuthorizes verifies a user delegation signature:
// the key it names must be one the account issued and still within its
// validity window, the signature must be that key's HMAC over the signed
// fields, and the key's owner must hold the operation's permission when the
// signature is used. It names the refusal's status and code when it does not.
func storageUserDelegationSASAuthorizes(r *http.Request, service, account, container, blob string) (int, string, string) {
	q := r.URL.Query()
	for _, name := range []string{"sv", "sp", "se", "sig", "skoid", "sktid", "skt", "ske", "sks", "skv", "sr"} {
		if q.Get(name) == "" {
			return http.StatusForbidden, "AuthenticationFailed", "Missing mandatory parameters for valid Shared Access Signature."
		}
	}
	now := time.Now().UTC()
	if start := q.Get("st"); start != "" {
		if at, ok := storageSASTime(start); ok && now.Before(at) {
			return http.StatusForbidden, "AuthenticationFailed", "Signature not valid in the specified time frame."
		}
	}
	expiry, ok := storageSASTime(q.Get("se"))
	if !ok || now.After(expiry) {
		return http.StatusForbidden, "AuthenticationFailed", "Signature not valid in the specified time frame."
	}
	keyStart, okStart := storageSASTime(q.Get("skt"))
	keyExpiry, okExpiry := storageSASTime(q.Get("ske"))
	if !okStart || !okExpiry || now.Before(keyStart) || now.After(keyExpiry) {
		return http.StatusForbidden, "AuthenticationFailed", "Signed key not valid in the specified time frame."
	}
	if !blobSASPermits(q.Get("sp"), r.Method) {
		return http.StatusForbidden, "AuthorizationPermissionMismatch", "This request is not authorized to perform this operation using this permission."
	}
	signed := blob
	if strings.EqualFold(q.Get("sr"), "c") || strings.EqualFold(q.Get("sr"), "s") {
		signed = ""
	}
	stringToSign := storageUserDelegationStringToSign(service, account, container, signed, q)
	key, found := storageUserDelegationKeyFor(account, q)
	material, err := base64.StdEncoding.DecodeString(key.Value)
	verified := false
	if found && err == nil {
		mac := hmac.New(sha256.New, material)
		_, _ = mac.Write([]byte(stringToSign))
		verified = hmac.Equal([]byte(q.Get("sig")), []byte(base64.StdEncoding.EncodeToString(mac.Sum(nil))))
	}
	if !verified {
		return http.StatusForbidden, "AuthenticationFailed",
			"Signature did not match. String to sign used was " + strings.ReplaceAll(stringToSign, "\n", `\n`)
	}
	if !storageBearerAuthorizes(r, service, account, rbacCallerForObject(key.SignedOID)) {
		return http.StatusForbidden, "AuthorizationPermissionMismatch", "This request is not authorized to perform this operation using this permission."
	}
	return 0, "", ""
}
