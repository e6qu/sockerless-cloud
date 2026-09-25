package main

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// gcsPreconditions are the generation and metageneration preconditions a JSON
// API request states in its query. A nil field is one the request did not
// state. Each holds or fails against the object as it is when the request is
// applied, which for a write is under the object's write lock.
// https://cloud.google.com/storage/docs/request-preconditions
type gcsPreconditions struct {
	GenerationMatch        *int64 `json:"ifGenerationMatch,omitempty"`
	GenerationNotMatch     *int64 `json:"ifGenerationNotMatch,omitempty"`
	MetagenerationMatch    *int64 `json:"ifMetagenerationMatch,omitempty"`
	MetagenerationNotMatch *int64 `json:"ifMetagenerationNotMatch,omitempty"`
}

// errGCSPreconditionFailed is a write refused because a precondition it stated
// did not hold.
var errGCSPreconditionFailed = errors.New("at least one of the pre-conditions you specified did not hold")

// parseGCSPreconditions reads the preconditions a request states on the object
// it addresses, or, with source set, on the source of a copy or rewrite
// (ifSourceGenerationMatch and its siblings).
func parseGCSPreconditions(query url.Values, source bool) (gcsPreconditions, error) {
	infix := ""
	if source {
		infix = "Source"
	}
	var p gcsPreconditions
	for name, field := range map[string]**int64{
		"if" + infix + "GenerationMatch":        &p.GenerationMatch,
		"if" + infix + "GenerationNotMatch":     &p.GenerationNotMatch,
		"if" + infix + "MetagenerationMatch":    &p.MetagenerationMatch,
		"if" + infix + "MetagenerationNotMatch": &p.MetagenerationNotMatch,
	} {
		raw := query.Get(name)
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return gcsPreconditions{}, fmt.Errorf("invalid value for %s: %q", name, raw)
		}
		*field = &value
	}
	return p, nil
}

// holds reports whether every precondition holds against the object. An
// ifGenerationMatch of 0 is the one precondition an absent object satisfies:
// it asks that no live object exist.
func (p gcsPreconditions) holds(obj GCSObject, exists bool) bool {
	generation, _ := strconv.ParseInt(obj.Generation, 10, 64)
	metageneration, _ := strconv.ParseInt(obj.Metageneration, 10, 64)
	if p.GenerationMatch != nil {
		if *p.GenerationMatch == 0 {
			if exists {
				return false
			}
		} else if !exists || generation != *p.GenerationMatch {
			return false
		}
	}
	if p.GenerationNotMatch != nil && exists && generation == *p.GenerationNotMatch {
		return false
	}
	if p.MetagenerationMatch != nil && (!exists || metageneration != *p.MetagenerationMatch) {
		return false
	}
	if p.MetagenerationNotMatch != nil && exists && metageneration == *p.MetagenerationNotMatch {
		return false
	}
	return true
}

// readOutcome is what a read's preconditions come to: the read goes ahead, is
// answered 304 because a NotMatch precondition names what the object is, or is
// refused 412.
func (p gcsPreconditions) readOutcome(obj GCSObject) int {
	generation, _ := strconv.ParseInt(obj.Generation, 10, 64)
	metageneration, _ := strconv.ParseInt(obj.Metageneration, 10, 64)
	if (p.GenerationMatch != nil && generation != *p.GenerationMatch) ||
		(p.MetagenerationMatch != nil && metageneration != *p.MetagenerationMatch) {
		return http.StatusPreconditionFailed
	}
	if (p.GenerationNotMatch != nil && generation == *p.GenerationNotMatch) ||
		(p.MetagenerationNotMatch != nil && metageneration == *p.MetagenerationNotMatch) {
		return http.StatusNotModified
	}
	return http.StatusOK
}

// gcsJSONReadPreconditionsMet evaluates the preconditions a JSON API read
// states, writing the 304 or 412 and returning false when the read does not go
// ahead.
func gcsJSONReadPreconditionsMet(w http.ResponseWriter, r *http.Request, obj GCSObject) bool {
	pre, err := parseGCSPreconditions(r.URL.Query(), false)
	if err != nil {
		GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
		return false
	}
	switch pre.readOutcome(obj) {
	case http.StatusPreconditionFailed:
		writeGCSPreconditionFailed(w)
		return false
	case http.StatusNotModified:
		w.WriteHeader(http.StatusNotModified)
		return false
	}
	return true
}

// writeGCSPreconditionFailed answers a JSON API request whose precondition
// failed, in the envelope the JSON API uses, whose errors[].reason clients
// branch on.
func writeGCSPreconditionFailed(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusPreconditionFailed)
	message := "At least one of the pre-conditions you specified did not hold."
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    http.StatusPreconditionFailed,
			"message": message,
			"errors": []map[string]string{{
				"message": message, "domain": "global", "reason": "conditionNotMet",
				"locationType": "header", "location": "If-Match",
			}},
		},
	})
}

// gcsObjectWriteLocks serializes the writes to one object. Cloud Storage
// evaluates a write's preconditions and applies the write as one step — of two
// writers stating ifGenerationMatch=0, exactly one creates the object — so the
// simulator holds an object's lock from reading it to storing what replaces it.
// An object's entry lives only while a writer holds or awaits it.
type gcsObjectWriteLocks struct {
	mu   sync.Mutex
	held map[string]*gcsObjectWriteLock
}

type gcsObjectWriteLock struct {
	sync.Mutex
	users int
}

var gcsObjectWriters = &gcsObjectWriteLocks{held: map[string]*gcsObjectWriteLock{}}

// lock takes the write lock of bucket/object and returns its release.
func (l *gcsObjectWriteLocks) lock(bucket, object string) func() {
	key := bucket + "/" + object
	l.mu.Lock()
	entry := l.held[key]
	if entry == nil {
		entry = &gcsObjectWriteLock{}
		l.held[key] = entry
	}
	entry.users++
	l.mu.Unlock()

	entry.Lock()
	return func() {
		entry.Unlock()
		l.mu.Lock()
		entry.users--
		if entry.users == 0 {
			delete(l.held, key)
		}
		l.mu.Unlock()
	}
}

// gcsGenerations issues object generations. Cloud Storage never gives two
// versions of an object name the same generation, a deleted and recreated
// object included — a client that holds a generation as a compare-and-swap
// token relies on it — and its generations are microsecond timestamps. Each
// generation here is the current microsecond or one past the last issued,
// whichever is later, and never at or below one the stores already held when
// they were opened, so none repeats across a restart either.
var gcsGenerations struct {
	mu   sync.Mutex
	last int64
}

// seedGCSGenerations raises the generation floor past every generation the
// stores hold, live and soft-deleted, when the stores are opened.
func seedGCSGenerations() {
	gcsGenerations.mu.Lock()
	defer gcsGenerations.mu.Unlock()
	for _, obj := range gcsObjects.List() {
		if generation, err := strconv.ParseInt(obj.Generation, 10, 64); err == nil {
			gcsGenerations.last = max(gcsGenerations.last, generation)
		}
	}
	for _, retired := range gcsSoftDeletedObjects.List() {
		if generation, err := strconv.ParseInt(retired.Object.Generation, 10, 64); err == nil {
			gcsGenerations.last = max(gcsGenerations.last, generation)
		}
	}
}

func gcsNextGeneration() int64 {
	gcsGenerations.mu.Lock()
	defer gcsGenerations.mu.Unlock()
	gcsGenerations.last = max(time.Now().UnixMicro(), gcsGenerations.last+1)
	return gcsGenerations.last
}

// serveGCSObjectMedia answers a download of the object — the XML API's GET
// Object, or the JSON API's alt=media — with the headers Cloud Storage
// documents for it, honoring a Range.
// https://cloud.google.com/storage/docs/xml-api/get-object-download
// https://cloud.google.com/storage/docs/xml-api/reference-headers
func serveGCSObjectMedia(w http.ResponseWriter, r *http.Request, obj GCSObject, body []byte) {
	h := w.Header()
	setGCSObjectResponseHeaders(h, obj, len(body))
	h.Set("ETag", gcsXMLETag(obj))
	if updated, err := time.Parse(time.RFC3339Nano, obj.Updated); err == nil {
		h.Set("Last-Modified", updated.UTC().Format(http.TimeFormat))
	}
	h.Set("x-goog-generation", defaultStr(obj.Generation, "1"))
	h.Set("x-goog-metageneration", defaultStr(obj.Metageneration, "1"))
	h.Set("x-goog-stored-content-length", strconv.Itoa(len(body)))
	h.Set("x-goog-stored-content-encoding", defaultStr(obj.ContentEncoding, "identity"))
	h.Set("x-goog-storage-class", defaultStr(obj.StorageClass, "STANDARD"))
	h.Add("x-goog-hash", "crc32c="+obj.Crc32c)
	if obj.Md5Hash != "" {
		h.Add("x-goog-hash", "md5="+obj.Md5Hash)
	}
	h.Set("Accept-Ranges", "bytes")

	requested := r.Header.Get("Range")
	if requested == "" {
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
		return
	}
	start, end, ok := parseGCSByteRange(requested, int64(len(body)))
	if !ok {
		h.Del("Content-Length")
		h.Set("Content-Range", fmt.Sprintf("bytes */%d", len(body)))
		writeGCSXMLError(w, http.StatusRequestedRangeNotSatisfiable, "InvalidRange",
			"The requested range cannot be satisfied.")
		return
	}
	h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
	w.WriteHeader(http.StatusPartialContent)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body[start : end+1])
	}
}

// parseGCSByteRange resolves one byte range — first-last, first-, or -suffix —
// against an object of size bytes. A range that begins at or past the end is
// not satisfiable; one that runs past it is cut at the end.
func parseGCSByteRange(header string, size int64) (start, end int64, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	first, last, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	if first == "" {
		suffix, err := strconv.ParseInt(last, 10, 64)
		if err != nil || suffix <= 0 || size == 0 {
			return 0, 0, false
		}
		return max(size-suffix, 0), size - 1, true
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	end = size - 1
	if last != "" {
		stated, err := strconv.ParseInt(last, 10, 64)
		if err != nil || stated < start {
			return 0, 0, false
		}
		end = min(stated, size-1)
	}
	return start, end, true
}

// gcsXMLETag is the entity tag the XML API gives an object: the hex MD5 of its
// bytes, quoted, for an object that has one; a composite object has no MD5 and
// is tagged by its CRC32C and generation instead.
func gcsXMLETag(obj GCSObject) string {
	if raw, err := base64.StdEncoding.DecodeString(obj.Md5Hash); err == nil && len(raw) == md5.Size {
		return `"` + hex.EncodeToString(raw) + `"`
	}
	return `"` + hex.EncodeToString([]byte(obj.Crc32c+"-"+obj.Generation)) + `"`
}

// gcsXMLReadConditionsMet evaluates the conditions an XML API read carries —
// Cloud Storage's own x-goog-if-generation-match and
// x-goog-if-metageneration-match, and HTTP's If-Match, If-None-Match,
// If-Modified-Since and If-Unmodified-Since — writing the refusal or the 304
// and returning false when the read does not go ahead.
func gcsXMLReadConditionsMet(w http.ResponseWriter, r *http.Request, obj GCSObject) bool {
	failed := func() bool {
		writeGCSXMLError(w, http.StatusPreconditionFailed, "PreconditionFailed",
			"At least one of the pre-conditions you specified did not hold.")
		return false
	}
	if want := r.Header.Get("x-goog-if-generation-match"); want != "" && want != defaultStr(obj.Generation, "1") {
		return failed()
	}
	if want := r.Header.Get("x-goog-if-metageneration-match"); want != "" && want != defaultStr(obj.Metageneration, "1") {
		return failed()
	}
	etag := gcsXMLETag(obj)
	updated, _ := time.Parse(time.RFC3339Nano, obj.Updated)
	if match := r.Header.Get("If-Match"); match != "" {
		if !gcsETagListNames(match, etag) {
			return failed()
		}
	} else if since, err := http.ParseTime(r.Header.Get("If-Unmodified-Since")); err == nil && updated.Truncate(time.Second).After(since) {
		return failed()
	}
	unchanged := false
	if noneMatch := r.Header.Get("If-None-Match"); noneMatch != "" {
		unchanged = gcsETagListNames(noneMatch, etag)
	} else if since, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil {
		unchanged = !updated.Truncate(time.Second).After(since)
	}
	if unchanged {
		w.Header().Set("ETag", etag)
		w.Header().Set("x-goog-generation", defaultStr(obj.Generation, "1"))
		w.WriteHeader(http.StatusNotModified)
		return false
	}
	return true
}

func gcsETagListNames(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.Trim(candidate, `"`) == strings.Trim(etag, `"`) {
			return true
		}
	}
	return false
}

type gcsXMLErrorBody struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
	Details string   `xml:"Details,omitempty"`
}

// writeGCSXMLError answers an XML API request with the XML error document that
// API uses.
func writeGCSXMLError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(gcsXMLErrorBody{Code: code, Message: message})
}

// gcsSignedURLMaxExpiry is the longest a V4 signed URL may live.
const gcsSignedURLMaxExpiry = 7 * 24 * time.Hour

// gcsSignedURLAuthorized reports whether a request is a V4 signed URL a
// service account of this simulator's IAM signed, and still in force. A
// request without X-Goog-Signature is not a signed URL and reports false
// without answering; a signed URL that does not verify is refused here.
// https://cloud.google.com/storage/docs/authentication/signatures
func gcsSignedURLAuthorized(w http.ResponseWriter, r *http.Request) (signed, authorized bool) {
	query := r.URL.Query()
	if !query.Has("X-Goog-Signature") {
		return false, false
	}
	malformed := func(detail string) (bool, bool) {
		writeGCSXMLError(w, http.StatusBadRequest, "AuthorizationQueryParametersError", detail)
		return true, false
	}
	if query.Get("X-Goog-Algorithm") != "GOOG4-RSA-SHA256" {
		return malformed("The X-Goog-Algorithm parameter must be GOOG4-RSA-SHA256.")
	}
	credential := query.Get("X-Goog-Credential")
	email, scope, found := strings.Cut(credential, "/")
	scopeParts := strings.Split(scope, "/")
	if !found || email == "" || len(scopeParts) != 4 || scopeParts[2] != "storage" || scopeParts[3] != "goog4_request" {
		return malformed("The X-Goog-Credential parameter is malformed.")
	}
	stamp := query.Get("X-Goog-Date")
	issued, err := time.Parse("20060102T150405Z", stamp)
	if err != nil || scopeParts[0] != stamp[:8] {
		return malformed("The X-Goog-Date parameter is malformed or does not match the credential scope.")
	}
	seconds, err := strconv.ParseInt(query.Get("X-Goog-Expires"), 10, 64)
	if err != nil || seconds < 1 || time.Duration(seconds)*time.Second > gcsSignedURLMaxExpiry {
		return malformed("The X-Goog-Expires parameter must be between 1 and 604800 seconds.")
	}
	now := time.Now()
	if now.Before(issued) {
		writeGCSXMLError(w, http.StatusBadRequest, "ExpiredToken", "Request is not valid yet.")
		return true, false
	}
	if now.After(issued.Add(time.Duration(seconds) * time.Second)) {
		writeGCSXMLError(w, http.StatusBadRequest, "ExpiredToken", "The provided token has expired.")
		return true, false
	}
	signature, err := hex.DecodeString(query.Get("X-Goog-Signature"))
	if err != nil {
		return malformed("The X-Goog-Signature parameter is not hex.")
	}

	account := fmt.Sprintf("projects/%s/serviceAccounts/%s", gcpProjectFromEmail(email), email)
	sa, ok := iamServiceAccounts.Get(account)
	if ok && !sa.Disabled {
		// Cloud Storage is reached on its default port, so the host a client
		// signs never carries one there; reached on another, client libraries
		// disagree on whether the signed host does (Google's Go library signs
		// the hostname alone), and a URL either one signs is accepted.
		hosts := []string{r.Host}
		if hostname, _, err := net.SplitHostPort(r.Host); err == nil {
			hosts = append(hosts, hostname)
		}
		for _, host := range hosts {
			digest := gcsSignedURLDigest(r, query, host, stamp, scope)
			if serviceAccountSigned(account, "", digest, signature) {
				return true, true
			}
		}
	}
	writeGCSXMLError(w, http.StatusForbidden, "SignatureDoesNotMatch",
		"Access denied. The request signature we calculated does not match the signature you provided.")
	return true, false
}

// gcsSignedURLDigest is the SHA-256 of a V4 signed URL's string to sign, for
// the request as addressed to host.
func gcsSignedURLDigest(r *http.Request, query url.Values, host, stamp, scope string) []byte {
	signedHeaders := strings.Split(query.Get("X-Goog-SignedHeaders"), ";")
	sort.Strings(signedHeaders)
	var canonicalHeaders strings.Builder
	for _, name := range signedHeaders {
		value := r.Header.Get(name)
		if name == "host" {
			value = host
		}
		canonicalHeaders.WriteString(name + ":" + strings.Join(strings.Fields(value), " ") + "\n")
	}
	names := make([]string, 0, len(query))
	for name := range query {
		if name != "X-Goog-Signature" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var canonicalQuery []string
	for _, name := range names {
		for _, value := range query[name] {
			canonicalQuery = append(canonicalQuery, gcsSigningEscape(name, false)+"="+gcsSigningEscape(value, false))
		}
	}
	canonicalRequest := strings.Join([]string{
		r.Method,
		gcsSigningEscape(r.URL.Path, true),
		strings.Join(canonicalQuery, "&"),
		canonicalHeaders.String(),
		strings.Join(signedHeaders, ";"),
		"UNSIGNED-PAYLOAD",
	}, "\n")
	hashed := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{"GOOG4-RSA-SHA256", stamp, scope, hex.EncodeToString(hashed[:])}, "\n")
	digest := sha256.Sum256([]byte(stringToSign))
	return digest[:]
}

// gcsSigningEscape percent-encodes s as a V4 canonical request does: every byte
// but the unreserved characters, and, in a path, the slashes between segments.
func gcsSigningEscape(s string, path bool) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '.', c == '_', c == '~':
			out.WriteByte(c)
		case c == '/' && path:
			out.WriteByte(c)
		default:
			fmt.Fprintf(&out, "%%%02X", c)
		}
	}
	return out.String()
}
