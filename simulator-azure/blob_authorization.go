package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Authorization for the Azure Storage data plane.
//
// The plane verified nothing. `Authorization: SharedKey …` was read as a
// routing signal and never checked, a `sig=` query parameter was read as a
// routing signal and never checked, and a request carrying neither was served
// anyway — so every credential the simulator issues was decorative, and
// `webParseBackupStorageURL` could only assert that a Shared Access Signature
// had the right *parameters*, because verifying it would have refused URLs the
// plane it writes through accepts.
//
// Three credentials reach this plane, and each is now verified against material
// this simulator issued:
//
//   - **Shared Key.** `Authorization: SharedKey <account>:<signature>`, the
//     scheme every Azure Storage SDK uses with an account key. The signature is
//     the base64 HMAC-SHA256, under the base64-decoded account key, of the
//     string Microsoft documents in "Authorize with Shared Key": the verb, six
//     standard headers, the canonicalized `x-ms-` headers and the canonicalized
//     resource. `SharedKeyLite` is the shorter form of the same scheme.
//   - **Shared Access Signature.** The `sv`/`sr`/`sp`/`se`/`sig` query members,
//     verified as "Create a service SAS" documents: the permissions, the start
//     and expiry, the canonicalized resource, the identifier, the address
//     range, the protocol, the version and the response-header overrides,
//     joined by newlines and signed with the account key.
//   - **Anonymous.** Served only where the container's public access level
//     allows it — `container` for a listing, `blob` or `container` for a read —
//     which is what `x-ms-blob-public-access` means.
//
// A request that presents none of the three, or one that does not verify, is
// refused the way the service refuses it rather than served.

// blobAuthRequiredSASParams are the query members a Blob service Shared Access
// Signature must carry. A URL missing any of them is not a signature the
// service would have issued. The Queue and File services issue signatures with
// no signedResource member at all, so theirs require one less.
var blobAuthRequiredSASParams = []string{"sv", "sr", "sp", "se", "sig"}

var storageSASRequiredParamsNoResource = []string{"sv", "sp", "se", "sig"}

// storageAccountKeys returns the two keys `listKeys` serves for an account
// named on the data plane, and whether ARM knows that account at all. An
// account ARM has never seen has no keys, so nothing can have signed for it.
func storageAccountKeys(account string) ([]string, bool) {
	if azStorageAccounts == nil {
		return nil, false
	}
	// Every request into the data plane resolves its account's keys, so this is
	// a per-request lookup and must not decode the account store to make it. An
	// account name is a hostname, so it is unique and one row answers.
	stored, ok := azStorageAccountsByName.Lookup(azStorageAccounts, strings.ToLower(account),
		func(a StorageAccount) []string { return []string{strings.ToLower(a.Name)} })
	if !ok {
		return nil, false
	}
	return []string{
		azureKeyMaterial64(stored.ID, "key1"),
		azureKeyMaterial64(stored.ID, "key2"),
	}, true
}

// azStorageAccountsByName answers the per-request account lookup from an index
// keyed by the store's generation rather than by reading every row.
var azStorageAccountsByName sim.GenerationIndex[StorageAccount]

// azureStorageAccountByName resolves one storage account by its globally
// unique name through the same index — for every caller that used to decode
// the whole account store to find one row on a data-plane request.
func azureStorageAccountByName(account string) (StorageAccount, bool) {
	if azStorageAccounts == nil {
		return StorageAccount{}, false
	}
	return azStorageAccountsByName.Lookup(azStorageAccounts, strings.ToLower(account),
		func(a StorageAccount) []string { return []string{strings.ToLower(a.Name)} })
}

// blobSignatureVerifies reports whether signature is the base64 HMAC-SHA256 of
// stringToSign under either of the account's keys. Both slots are live at once,
// which is what makes the documented key rotation — move traffic to the
// secondary, then regenerate the primary — possible.
func blobSignatureVerifies(account, stringToSign, signature string) bool {
	keys, ok := storageAccountKeys(account)
	if !ok {
		return false
	}
	for _, key := range keys {
		material, err := base64.StdEncoding.DecodeString(key)
		if err != nil {
			continue
		}
		mac := hmac.New(sha256.New, material)
		_, _ = mac.Write([]byte(stringToSign))
		if hmac.Equal([]byte(signature), []byte(base64.StdEncoding.EncodeToString(mac.Sum(nil)))) {
			return true
		}
	}
	return false
}

// blobCanonicalizedHeaders renders the `x-ms-` headers as Shared Key signs
// them: lower-cased names, sorted, one `name:value` line each.
func blobCanonicalizedHeaders(r *http.Request) string {
	var lines []string
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "x-ms-") {
			continue
		}
		lines = append(lines, lower+":"+strings.TrimSpace(strings.Join(values, ",")))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// blobCanonicalizedResource renders the resource as Shared Key signs it:
// `/<account><path>` followed by each query parameter, lower-cased and sorted,
// as `\n<name>:<value>`.
func blobCanonicalizedResource(account string, u *url.URL) string {
	// The escaped path, not the decoded one: a client signs the path exactly
	// as it puts it on the wire, and az percent-encodes the slash inside a
	// nested directory name — `build%2Fartifacts` — which the decoded path
	// collapses into a plain '/'. Both azblob and the az CLI sign the escaped
	// form; a verifier reading the decoded form agrees with them only until
	// the first path that needs escaping.
	resource := "/" + account + u.EscapedPath()
	query := u.Query()
	names := make([]string, 0, len(query))
	for name := range query {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(resource)
	for _, name := range names {
		values := query[name]
		if len(values) == 0 {
			for actual, actualValues := range query {
				if strings.EqualFold(actual, name) {
					values = actualValues
				}
			}
		}
		sorted := append([]string(nil), values...)
		sort.Strings(sorted)
		b.WriteString("\n" + name + ":" + strings.Join(sorted, ","))
	}
	return b.String()
}

// tableSharedKeyStringToSign renders the Table service's Shared Key string,
// which is shorter than the other services': the verb, two content headers and
// the date, then the resource carrying only a `comp` parameter — no
// canonicalized x-ms headers at all. The Lite form is shorter still.
func tableSharedKeyStringToSign(account string, r *http.Request, lite bool) string {
	date := r.Header.Get("Date")
	if xms := r.Header.Get("x-ms-date"); xms != "" {
		date = xms
	}
	resource := "/" + account + r.URL.EscapedPath()
	if comp := r.URL.Query().Get("comp"); comp != "" {
		resource += "?comp=" + comp
	}
	if lite {
		return date + "\n" + resource
	}
	return strings.Join([]string{
		r.Method,
		r.Header.Get("Content-MD5"),
		r.Header.Get("Content-Type"),
		date,
	}, "\n") + "\n" + resource
}

// blobSharedKeyStringToSign builds the string "Authorize with Shared Key"
// documents for the Blob, Queue and File services.
func blobSharedKeyStringToSign(account string, r *http.Request, lite bool) string {
	header := func(name string) string { return r.Header.Get(name) }
	// Content-Length is signed as an empty field when it is zero, and it is read
	// from the parsed request rather than the header map: Go's server moves it
	// out of Header into ContentLength, so a handler that reads the header sees
	// nothing and signs a different string than the client did.
	contentLength := ""
	if r.ContentLength > 0 {
		contentLength = strconv.FormatInt(r.ContentLength, 10)
	}
	if lite {
		return strings.Join([]string{
			r.Method,
			header("Content-MD5"),
			header("Content-Type"),
			header("Date"),
			blobCanonicalizedHeaders(r),
		}, "\n") + "\n" + blobCanonicalizedResource(account, r.URL)
	}
	return strings.Join([]string{
		r.Method,
		header("Content-Encoding"),
		header("Content-Language"),
		contentLength,
		header("Content-MD5"),
		header("Content-Type"),
		header("Date"),
		header("If-Modified-Since"),
		header("If-Match"),
		header("If-None-Match"),
		header("If-Unmodified-Since"),
		header("Range"),
		blobCanonicalizedHeaders(r),
	}, "\n") + "\n" + blobCanonicalizedResource(account, r.URL)
}

// blobSASStringToSign builds the string "Create a service SAS" documents for
// the Blob service, in the field order that document fixes. Members a signature
// did not carry are empty.
//
// Sixteen fields, which is the whole of it: the `saoid`, `suoid` and `scid`
// members Microsoft documents alongside these belong to a *user delegation*
// signature, and a service signature made with an account key does not carry
// them. Signing nineteen fields — the service ones plus those three, as the
// combined reference reads — produces a string no client ever signs, and every
// signature from a current SDK fails against it.
//
// The tenth field is the snapshot time for a blob-snapshot signature and the
// directory depth for a directory one; a container or blob signature carries
// neither.
func blobSASStringToSign(service, account, container, blob string, q url.Values) string {
	canonical := "/" + service + "/" + account + "/" + container
	if blob != "" {
		canonical += "/" + blob
	}
	// Each service signs its own layout, read out of the SDK that signs it
	// rather than out of the combined reference — which is how the Blob layout
	// was first implemented with three user-delegation members no service
	// signature carries, and every SDK signature failed against it.
	//
	// The Queue service signs eight fields and stops at the version. The File
	// service signs those eight plus the five response-header overrides. The
	// Blob service's string grew with the service version the signature itself
	// declares: signedResource and the snapshot time joined in 2018-11-09, the
	// signed encryption scope in 2020-12-06 — and a client signs the layout of
	// its own `sv`, so the verifier reconstructs the layout that version
	// defines, exactly as the service does.
	version := q.Get("sv")
	if service == "queue" || service == "file" {
		fields := []string{
			q.Get("sp"),
			q.Get("st"),
			q.Get("se"),
			canonical,
			q.Get("si"),
			q.Get("sip"),
			q.Get("spr"),
			version,
		}
		if service == "file" {
			fields = append(fields,
				q.Get("rscc"), q.Get("rscd"), q.Get("rsce"), q.Get("rscl"), q.Get("rsct"))
		}
		return strings.Join(fields, "\n")
	}
	fields := []string{
		q.Get("sp"),
		q.Get("st"),
		q.Get("se"),
		canonical,
		q.Get("si"),
		q.Get("sip"),
		q.Get("spr"),
		version,
	}
	if version >= "2018-11-09" {
		fields = append(fields, q.Get("sr"), blobSASSnapshotField(q))
	}
	if version >= "2020-12-06" {
		fields = append(fields, q.Get("ses"))
	}
	fields = append(fields,
		q.Get("rscc"),
		q.Get("rscd"),
		q.Get("rsce"),
		q.Get("rscl"),
		q.Get("rsct"),
	)
	return strings.Join(fields, "\n")
}

// storageSASTime parses the ISO 8601 forms Azure accepts in a signature's
// signedStart and signedExpiry: seconds, minutes — which is what
// `az storage container generate-sas --expiry 2026-01-02T15:04Z` emits — and a
// bare date.
func storageSASTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04Z07:00", "2006-01-02"} {
		if at, err := time.Parse(layout, value); err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}

// blobSASSnapshotField is the tenth signed field: a blob snapshot signs the
// time it addresses, a directory signs its depth, and everything else signs
// nothing there.
func blobSASSnapshotField(q url.Values) string {
	if snapshot := q.Get("sst"); snapshot != "" {
		return snapshot
	}
	return q.Get("sdd")
}

// blobSASAuthorizes reports whether a Shared Access Signature on the request
// authorizes it, and the reason when it does not.
func blobSASAuthorizes(service, account, container, blob string, q url.Values, method string) (bool, string) {
	// An account SAS carries the services and resource-types pair instead of a
	// signedResource — `data.azurerm_storage_account_sas`, whose output the
	// azurerm provider's own App Service backup example feeds straight into
	// `storage_account_url`, issues exactly that shape and real Azure accepts
	// it anywhere a service SAS is accepted.
	accountSAS := q.Get("ss") != "" && q.Get("srt") != "" && q.Get("sr") == ""
	required := blobAuthRequiredSASParams
	if service == "queue" || service == "file" {
		required = storageSASRequiredParamsNoResource
	}
	if accountSAS {
		required = storageSASRequiredParamsNoResource
	}
	for _, name := range required {
		if q.Get(name) == "" {
			return false, "Missing mandatory parameters for valid Shared Access Signature."
		}
	}
	if start := q.Get("st"); start != "" {
		if at, ok := storageSASTime(start); ok && time.Now().UTC().Before(at) {
			return false, "Signature not valid in the specified time frame."
		}
	}
	expiry, ok := storageSASTime(q.Get("se"))
	if !ok {
		return false, "Signature not valid in the specified time frame."
	}
	if time.Now().UTC().After(expiry) {
		return false, "Signature not valid in the specified time frame."
	}
	if !blobSASPermits(q.Get("sp"), method) {
		return false, "Signature did not match. String to sign used was " + q.Get("sp")
	}
	if accountSAS {
		if !blobAccountSASCovers(service, container, blob, q) {
			return false, "The signature does not cover this service and resource type."
		}
		if !blobSignatureVerifies(account, blobAccountSASStringToSign(account, q), q.Get("sig")) {
			return false, "Signature did not match. String to sign used was " +
				strings.ReplaceAll(blobAccountSASStringToSign(account, q), "\n", `\n`)
		}
		return true, ""
	}
	// A signature over the blob authorizes the blob; one over the container
	// authorizes everything under it, which is what `sr=c` means for the Blob
	// service and `sr=s` for a share on the File service. The Queue service
	// signs the queue itself and nothing deeper.
	signed := blob
	if strings.EqualFold(q.Get("sr"), "c") || strings.EqualFold(q.Get("sr"), "s") || service == "queue" {
		signed = ""
	}
	if !blobSignatureVerifies(account, blobSASStringToSign(service, account, container, signed, q), q.Get("sig")) {
		return false, "Signature did not match. String to sign used was " +
			strings.ReplaceAll(blobSASStringToSign(service, account, container, signed, q), "\n", `\n`)
	}
	return true, ""
}

// blobAccountSASStringToSign renders the account Shared Access Signature
// string, read out of azblob's own account signer: the account name, the
// permissions, the services, the resource types, the window, the address
// range, the protocol, the version, the encryption scope from 2020-12-06 on —
// and a terminating newline the account form alone carries.
func blobAccountSASStringToSign(account string, q url.Values) string {
	fields := []string{
		account,
		q.Get("sp"),
		q.Get("ss"),
		q.Get("srt"),
		q.Get("st"),
		q.Get("se"),
		q.Get("sip"),
		q.Get("spr"),
		q.Get("sv"),
	}
	if q.Get("sv") >= "2020-12-06" {
		fields = append(fields, q.Get("ses"))
	}
	fields = append(fields, "")
	return strings.Join(fields, "\n")
}

// blobAccountSASCovers reports whether an account signature's signed services
// and resource types reach the resource this request addresses: the service by
// its letter, and the service root, a container or an object by theirs.
func blobAccountSASCovers(service, container, blob string, q url.Values) bool {
	letter := map[string]string{"blob": "b", "file": "f", "queue": "q", "table": "t"}[service]
	if letter == "" || !strings.Contains(strings.ToLower(q.Get("ss")), letter) {
		return false
	}
	types := strings.ToLower(q.Get("srt"))
	switch {
	case container == "":
		return strings.Contains(types, "s")
	case blob == "":
		return strings.Contains(types, "c")
	default:
		return strings.Contains(types, "o")
	}
}

// blobSASPermits reports whether the signed permission set covers the request's
// method, following the permission letters "Create a service SAS" defines.
func blobSASPermits(permissions, method string) bool {
	has := func(letters string) bool { return strings.ContainsAny(strings.ToLower(permissions), letters) }
	switch method {
	case http.MethodGet, http.MethodHead:
		return has("rl")
	case http.MethodPut, http.MethodPost, http.MethodPatch:
		return has("wca")
	case http.MethodDelete:
		return has("d")
	default:
		return false
	}
}

// blobAnonymousAllowed reports whether a container's public access level serves
// this request without a credential. `blob` exposes the blobs and not the
// listing; `container` exposes both. Anything else exposes nothing.
func blobAnonymousAllowed(account, container, blob string) bool {
	if container == "" {
		return false
	}
	stored, ok := blobContainersData.Get(blobContainerKey(account, container))
	if !ok {
		return false
	}
	switch strings.ToLower(stored.PublicAccess) {
	case "container":
		return true
	case "blob":
		return blob != ""
	default:
		return false
	}
}

// RequireStorageOAuthBearer gates the operations real Azure serves only under
// Microsoft Entra authorization — Get User Delegation Key is one — refusing a
// request that authenticated with the account key instead: the delegation key
// exists to delegate an Entra identity, and an account-key caller has no
// identity to delegate.
func RequireStorageOAuthBearer(w http.ResponseWriter, r *http.Request) bool {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(authorization, "Bearer ") {
		claims, err := verifyAzureSimJWT(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")))
		if err == nil && strings.TrimSuffix(azureTokenAudience(claims), "/") == "https://storage.azure.com" {
			return true
		}
	}
	blobAuthorizationError(w, http.StatusUnauthorized, "AuthenticationFailed",
		"Server failed to authenticate the request. Only Microsoft Entra (Bearer) authorization with the storage resource is supported for this operation.")
	return false
}

// storageCredentialAccount is the account a Shared Key credential names: its
// last path segment, because a path-style client's account string carries the
// service prefix it parsed out of its endpoint.
func storageCredentialAccount(named string) string {
	if i := strings.LastIndexByte(named, '/'); i >= 0 {
		return named[i+1:]
	}
	return named
}

// blobAuthorizationError writes the refusal the Blob service writes, in the
// XML envelope its clients parse.
func blobAuthorizationError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("x-ms-error-code", code)
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w,
		"<?xml version=\"1.0\" encoding=\"utf-8\"?><Error><Code>%s</Code><Message>%s</Message></Error>",
		code, message)
}

// storageAuthorizedContextKey marks a request the dispatcher already
// authorized. A path-style request is verified against the path the client
// signed — the one still carrying the account segment — and then rewritten for
// the handler, whose own authorization would otherwise recompute a different
// string and refuse it.
type storageAuthorizedContextKeyType struct{}

var storageAuthorizedContextKey storageAuthorizedContextKeyType

// StorageMarkAuthorized returns the request marked as already authorized.
func StorageMarkAuthorized(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), storageAuthorizedContextKey, true))
}

// AuthorizeStorageDataPlane verifies the credential on a storage data-plane
// request, refusing one the account's keys did not sign. service names the
// plane — "blob", "file", "queue" or "table" — because the Table service signs
// a different string than the other three and a Shared Access Signature's
// canonicalized resource carries the service name.
//
// It reports true when the request may proceed. An account ARM has never seen
// has no keys to verify against and no public access level to consult, so a
// request naming one is refused as the service refuses a request to an account
// that does not exist.
func AuthorizeStorageDataPlane(w http.ResponseWriter, r *http.Request, service, account, container, blob string) bool {
	if account == "" {
		return true // not an account-addressed request; the caller routes it.
	}
	if authorized, _ := r.Context().Value(storageAuthorizedContextKey).(bool); authorized {
		return true
	}
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	query := r.URL.Query()

	switch {
	case strings.HasPrefix(authorization, "SharedKey "), strings.HasPrefix(authorization, "SharedKeyLite "):
		lite := strings.HasPrefix(authorization, "SharedKeyLite ")
		_, credential, _ := strings.Cut(authorization, " ")
		named, signature, ok := strings.Cut(strings.TrimSpace(credential), ":")
		// A client on a path-style endpoint derives its account string from the
		// endpoint's path — the az CLI's storage SDK turns
		// `http://host/queue/<account>/` into the literal `queue/<account>` and
		// signs with that string in the header and the resource alike. The
		// credential still names this account (its last segment), and the
		// signature is still verified against this account's keys; the string
		// the client signed is reconstructed with the client's own spelling,
		// which is how Azurite verifies the same requests.
		if !ok || !strings.EqualFold(storageCredentialAccount(named), account) {
			blobAuthorizationError(w, http.StatusForbidden, "AuthenticationFailed",
				"Server failed to authenticate the request. Make sure the value of Authorization header is formed correctly including the signature.")
			return false
		}
		stringToSign := blobSharedKeyStringToSign(named, r, lite)
		if service == "table" {
			stringToSign = tableSharedKeyStringToSign(named, r, lite)
		}
		if !blobSignatureVerifies(account, stringToSign, signature) {
			blobAuthorizationError(w, http.StatusForbidden, "AuthenticationFailed",
				"Server failed to authenticate the request. Make sure the value of Authorization header is formed correctly including the signature.")
			return false
		}
		return true

	case query.Get("sig") != "":
		if ok, reason := blobSASAuthorizes(service, account, container, blob, query, r.Method); !ok {
			blobAuthorizationError(w, http.StatusForbidden, "AuthenticationFailed", reason)
			return false
		}
		return true

	case strings.HasPrefix(authorization, "Bearer "):
		// A Microsoft Entra token is the third credential the service accepts —
		// and only one whose audience is Azure Storage. A token minted for
		// Azure Resource Manager does not authorize a data-plane read on real
		// Azure, however valid it is, and accepting it here would let every
		// ARM-authenticated test reach storage it never presented a storage
		// credential for.
		claims, err := verifyAzureSimJWT(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")))
		if err != nil {
			blobAuthorizationError(w, http.StatusUnauthorized, "InvalidAuthenticationInfo",
				"Server failed to authenticate the request. Please refer to the information in the www-authenticate header.")
			return false
		}
		audience := strings.TrimSuffix(azureTokenAudience(claims), "/")
		if audience != "https://storage.azure.com" {
			blobAuthorizationError(w, http.StatusForbidden, "AuthenticationFailed",
				"Server failed to authenticate the request. The access token was not issued for the storage resource.")
			return false
		}
		return true

	default:
		if blobAnonymousAllowed(account, container, blob) {
			return true
		}
		blobAuthorizationError(w, http.StatusForbidden, "AuthenticationFailed",
			"Server failed to authenticate the request. Make sure the value of Authorization header is formed correctly including the signature.")
		return false
	}
}
