package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// S3 Express One Zone: directory buckets, the regional control endpoint that
// lists them, and the session a client establishes before reading or writing
// one.
//
// A directory bucket is a bucket placed in a single Availability Zone, named
// for the zone it sits in — "<base>--<zone-id>--x-s3" — and reached at a zonal
// endpoint. Its objects are ordinary objects and are stored the way every other
// bucket's are; what differs is placement, how the bucket is listed, and how a
// caller authenticates to it.
//
// Authentication is the part that makes this more than a name. A client does
// not sign each Zonal endpoint request with its own credentials: it calls
// CreateSession once with them, and Amazon S3 returns temporary credentials
// scoped to that one bucket, which the client signs with and echoes in the
// x-amz-s3session-token header. The credentials here are minted into the same
// store the AWS Security Token Service mints into, so they authenticate as the
// caller who asked for them and carry that caller's policies — and they are
// scoped: a session for one bucket does not open another, a ReadOnly session
// refuses the writes the model says it refuses, and an expired one is refused.

// s3ExpressSessionTokenHeader carries the session token on a Zonal endpoint
// request. Its presence is what tells the data plane a session, rather than the
// caller's own credential, is authorizing the request.
const s3ExpressSessionTokenHeader = "x-amz-s3session-token"

// s3DirectoryBucketSuffix ends every directory bucket's name.
const s3DirectoryBucketSuffix = "--x-s3"

// S3ExpressSession is one established session: the credential material, the
// bucket it opens, and what it may do there.
type S3ExpressSession struct {
	AccessKeyID string `json:"accessKeyId"`
	Bucket      string `json:"bucket"`
	// Mode is "ReadWrite" or "ReadOnly". A ReadOnly session is constrained to
	// the read operations CreateSession's own documentation lists.
	Mode    string `json:"mode"`
	Expires string `json:"expires"`
}

var s3ExpressSessions sim.Store[S3ExpressSession]

// registerS3Express mounts the regional control endpoint's listing. The zonal
// operations — CreateSession and the object calls a session authorizes — arrive
// on the bucket routes S3 already serves and are dispatched there.
func registerS3Express(srv *sim.Server) {
	s3ExpressSessions = sim.MakeStore[S3ExpressSession](srv.DB(), "s3_express_sessions")
}

// s3DirectoryBucketZone reads the Availability Zone id out of a directory
// bucket's name. Amazon S3 requires the name to carry it — "<base>--<zone-id>--x-s3"
// — which is what makes the bucket addressable at its zonal endpoint without a
// lookup. It returns "" for a name that is not a directory bucket's.
func s3DirectoryBucketZone(name string) string {
	if !strings.HasSuffix(name, s3DirectoryBucketSuffix) {
		return ""
	}
	stem := strings.TrimSuffix(name, s3DirectoryBucketSuffix)
	i := strings.LastIndex(stem, "--")
	if i <= 0 {
		return ""
	}
	zone := stem[i+2:]
	if zone == "" {
		return ""
	}
	return zone
}

// s3IsExpressControlHost reports whether the request arrived at the regional
// control endpoint for directory buckets. Amazon S3 separates the two listings
// by host — ListBuckets at the S3 endpoint, ListDirectoryBuckets at
// s3express-control.<region> — and both are GET on the service root, so the
// host is the only thing that distinguishes them.
func s3IsExpressControlHost(host string) bool {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	label, _, _ := strings.Cut(host, ".")
	return label == "s3express-control"
}

// s3ServeListDirectoryBuckets reports whether this GET on the service root is
// ListDirectoryBuckets rather than ListBuckets, and serves it if so.
//
// Two real clients spell it two ways: the AWS SDK for Go marks the request with
// the x-id it puts in the operation's URI, and the AWS CLI sends a bare GET.
// Both reach the regional control endpoint, so either signal identifies the
// operation.
func s3ServeListDirectoryBuckets(w http.ResponseWriter, r *http.Request) bool {
	if !s3IsExpressControlHost(r.Host) && r.URL.Query().Get("x-id") != "ListDirectoryBuckets" {
		return false
	}
	handleS3ListDirectoryBuckets(w, r)
	return true
}

// s3ListDirectoryBucketsResult is the listing's response body. Its buckets
// carry the same Name and CreationDate a general purpose bucket does; the model
// notes BucketRegion is not part of this response.
type s3ListDirectoryBucketsResult struct {
	XMLName           struct{}  `xml:"ListAllMyDirectoryBucketsResult"`
	Xmlns             string    `xml:"xmlns,attr"`
	Buckets           s3Buckets `xml:"Buckets"`
	ContinuationToken string    `xml:"ContinuationToken,omitempty"`
}

func handleS3ListDirectoryBuckets(w http.ResponseWriter, r *http.Request) {
	var directory []S3Bucket
	for _, b := range s3Buckets_.List() {
		if b.DirectoryZone != "" {
			directory = append(directory, b)
		}
	}
	sort.Slice(directory, func(i, j int) bool { return directory[i].Name < directory[j].Name })

	q := r.URL.Query()
	if after := q.Get("continuation-token"); after != "" {
		for len(directory) > 0 && directory[0].Name <= after {
			directory = directory[1:]
		}
	}
	next := ""
	if maximum := q.Get("max-directory-buckets"); maximum != "" {
		n, err := strconv.Atoi(maximum)
		if err != nil || n < 0 {
			sim.S3ErrorXML(w, "InvalidArgument", "max-directory-buckets must be a non-negative integer",
				"/", sim.RequestID(r.Context()), http.StatusBadRequest)
			return
		}
		if n < len(directory) {
			next = directory[n-1].Name
			directory = directory[:n]
		}
	}
	if directory == nil {
		directory = []S3Bucket{}
	}

	sim.WriteXML(w, http.StatusOK, s3ListDirectoryBucketsResult{
		Xmlns:             "http://s3.amazonaws.com/doc/2006-03-01/",
		Buckets:           s3Buckets{Bucket: directory},
		ContinuationToken: next,
	})
}

// s3CreateSessionResult is the response CreateSession returns. The credentials
// are the session's own; everything else the output declares describes
// encryption the caller asked for, which a request that asks for none omits.
type s3CreateSessionResult struct {
	XMLName     struct{}             `xml:"CreateSessionResult"`
	Xmlns       string               `xml:"xmlns,attr"`
	Credentials s3SessionCredentials `xml:"Credentials"`
}

type s3SessionCredentials struct {
	AccessKeyID     string `xml:"AccessKeyId"`
	SecretAccessKey string `xml:"SecretAccessKey"`
	SessionToken    string `xml:"SessionToken"`
	Expiration      string `xml:"Expiration"`
}

// s3ExpressSessionDuration is how long a session lasts. Amazon S3 documents
// five minutes, and the SDKs refresh on that interval.
const s3ExpressSessionDuration = 5 * time.Minute

// handleS3CreateSession establishes a session on a directory bucket.
func handleS3CreateSession(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	held, ok := s3Buckets_.Get(bucket)
	if !ok {
		sim.S3ErrorXML(w, "NoSuchBucket", "The specified bucket does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	// CreateSession is a Zonal endpoint operation, and only a directory bucket
	// has one.
	if held.DirectoryZone == "" {
		sim.S3ErrorXML(w, "InvalidRequest",
			"The specified bucket is not a directory bucket, and only a directory bucket supports a session.",
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}

	mode := r.Header.Get("x-amz-create-session-mode")
	switch mode {
	case "":
		// The default attempts the greater privilege first, and this caller is
		// already authorized for the bucket, so it is ReadWrite.
		mode = "ReadWrite"
	case "ReadWrite", "ReadOnly":
	default:
		sim.S3ErrorXML(w, "InvalidArgument",
			fmt.Sprintf("The session mode %q is not one of ReadWrite or ReadOnly.", mode),
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}

	// The session authenticates as the caller who asked for it: the credential
	// goes into the same store the Security Token Service mints into, carrying
	// the caller's principal, so every later request signed with it resolves to
	// that principal and is evaluated against that principal's policies.
	principalArn, _, userName, known := iamPrincipalForAccessKey(iamAccessKeyIDFromRequest(r))
	if !known {
		principalArn = fmt.Sprintf("arn:aws:iam::%s:user/simulator", awsAccountID())
	}
	akid, secret, token := stsMintTempCred()
	expires := time.Now().UTC().Add(s3ExpressSessionDuration)
	iamTempCreds.Put(akid, IAMTempCred{
		AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token,
		UserName: userName, PrincipalArn: principalArn,
		Expiration: expires.Format(time.RFC3339),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	})
	s3ExpressSessions.Put(akid, S3ExpressSession{
		AccessKeyID: akid, Bucket: bucket, Mode: mode,
		Expires: expires.Format(time.RFC3339),
	})

	sim.WriteXML(w, http.StatusOK, s3CreateSessionResult{
		Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/",
		Credentials: s3SessionCredentials{
			AccessKeyID:     akid,
			SecretAccessKey: secret,
			SessionToken:    token,
			Expiration:      expires.Format(time.RFC3339),
		},
	})
}

// s3ExpressReadOnlyOperations are the Zonal endpoint operations a ReadOnly
// session may perform, as CreateSession's own documentation lists them.
var s3ExpressReadOnlyOperations = map[string]bool{
	"GetObject":            true,
	"HeadObject":           true,
	"ListObjectsV2":        true,
	"GetObjectAttributes":  true,
	"ListParts":            true,
	"ListMultipartUploads": true,
}

// s3EnforceExpressSession refuses a Zonal endpoint request whose session does
// not authorize it, and reports whether it wrote the refusal.
//
// A session is scoped to one bucket and to one mode, and it expires. Each of
// those is a real refusal a caller can provoke, so each is enforced here rather
// than described.
func s3EnforceExpressSession(w http.ResponseWriter, r *http.Request, operation, bucket string) bool {
	if r.Header.Get(s3ExpressSessionTokenHeader) == "" {
		return false
	}
	session, ok := s3ExpressSessions.Get(iamAccessKeyIDFromRequest(r))
	if !ok {
		sim.S3ErrorXML(w, "InvalidRequest",
			"The session token presented does not belong to a session this endpoint established.",
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return true
	}
	if expires, err := time.Parse(time.RFC3339, session.Expires); err == nil && time.Now().UTC().After(expires) {
		sim.S3ErrorXML(w, "ExpiredToken",
			"The session credentials presented have expired. Call CreateSession again for a new session.",
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return true
	}
	if session.Bucket != bucket {
		sim.S3ErrorXML(w, "AccessDenied",
			fmt.Sprintf("The session presented was established for bucket %q, and a session opens only the bucket it was created for.", session.Bucket),
			bucket, sim.RequestID(r.Context()), http.StatusForbidden)
		return true
	}
	if session.Mode == "ReadOnly" && !s3ExpressReadOnlyOperations[operation] {
		sim.S3ErrorXML(w, "AccessDenied",
			fmt.Sprintf("A ReadOnly session does not authorize %s.", operation),
			bucket, sim.RequestID(r.Context()), http.StatusForbidden)
		return true
	}
	return false
}

// s3ApplyDirectoryBucketConfiguration validates a CreateBucket that asks for a
// directory bucket and records the placement on the bucket. It reports whether
// the bucket may be created; on a refusal it has already written the error.
//
// The name is the constraint that matters: Amazon S3 requires a directory
// bucket to be named for the zone it is placed in, because that is what makes
// it addressable at its zonal endpoint. A name that does not carry the zone in
// the Location, or does not end in the directory-bucket suffix, is refused.
func s3ApplyDirectoryBucketConfiguration(
	w http.ResponseWriter, r *http.Request, b *S3Bucket, locationType, zone, redundancy string,
) bool {
	switch locationType {
	case "AvailabilityZone", "LocalZone":
	case "":
		sim.S3ErrorXML(w, "InvalidRequest",
			"A directory bucket requires a Location naming the zone it is placed in.",
			b.Name, sim.RequestID(r.Context()), http.StatusBadRequest)
		return false
	default:
		sim.S3ErrorXML(w, "InvalidArgument",
			fmt.Sprintf("The location type %q is not one of AvailabilityZone or LocalZone.", locationType),
			b.Name, sim.RequestID(r.Context()), http.StatusBadRequest)
		return false
	}
	named := s3DirectoryBucketZone(b.Name)
	if named == "" {
		sim.S3ErrorXML(w, "InvalidBucketName",
			"A directory bucket is named <base>--<zone-id>--x-s3, and this name does not carry a zone.",
			b.Name, sim.RequestID(r.Context()), http.StatusBadRequest)
		return false
	}
	if zone != "" && named != zone {
		sim.S3ErrorXML(w, "InvalidBucketName",
			fmt.Sprintf("The bucket name places it in zone %q while the Location names %q; they must agree.", named, zone),
			b.Name, sim.RequestID(r.Context()), http.StatusBadRequest)
		return false
	}
	if redundancy == "" {
		redundancy = "SingleAvailabilityZone"
		if locationType == "LocalZone" {
			redundancy = "SingleLocalZone"
		}
	}
	b.DirectoryZone = named
	b.DataRedundancy = redundancy
	return true
}

// s3SignedPathContextKey carries the request path the client signed, when the
// path the router sees is not it.
type s3SignedPathContextKey struct{}

// s3ZonalHostBucket reads the directory bucket out of a zonal endpoint's
// hostname — "<bucket>.s3express-<zone-id>.<region>.amazonaws.com" — and
// returns "" for any other host.
//
// A directory bucket is addressed virtual-hosted style and only that way:
// Amazon S3's own documentation says path-style requests are not supported at
// the Zonal endpoint. So the bucket arrives in the host and the path carries
// the key alone.
func s3ZonalHostBucket(host string) string {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	label, rest, found := strings.Cut(host, ".")
	if !found || label == "" {
		return ""
	}
	if !strings.HasPrefix(rest, "s3express-") || strings.HasPrefix(rest, "s3express-control.") {
		return ""
	}
	if s3DirectoryBucketZone(label) == "" {
		return ""
	}
	return label
}

// s3RewriteZonalRequest maps a virtual-hosted request on a directory bucket's
// zonal endpoint onto the path the router works in, and records the path the
// client signed so the signature is still verified against what was sent.
func s3RewriteZonalRequest(r *http.Request) {
	bucket := s3ZonalHostBucket(r.Host)
	if bucket == "" {
		return
	}
	signed := r.URL.Path
	if signed == "" {
		signed = "/"
	}
	*r = *r.WithContext(context.WithValue(r.Context(), s3SignedPathContextKey{}, signed))
	r.URL.Path = "/" + bucket + strings.TrimSuffix(signed, "/")
	if r.URL.RawPath != "" {
		r.URL.RawPath = "/" + bucket + strings.TrimSuffix(r.URL.RawPath, "/")
	}
}

// s3SignedPath returns the path the client signed: the one it sent, which for a
// virtual-hosted request is not the one the router was given.
func s3SignedPath(r *http.Request) (string, bool) {
	signed, ok := r.Context().Value(s3SignedPathContextKey{}).(string)
	return signed, ok
}
