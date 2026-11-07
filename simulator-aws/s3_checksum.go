package main

import (
	"crypto/sha1" //nolint:gosec // SHA-1 is one of the checksum algorithms S3 offers, not a security control.
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

type hashWriter = hash.Hash

func newSHA256Writer() hashWriter { return sha256.New() }

// The additional checksum algorithms of s3-checking-object-integrity-upload.
// CRC64NVME is what S3 computes when a request names none.
var s3ChecksumAlgorithms = map[string]func() hash.Hash{
	"CRC32":     func() hash.Hash { return crc32.NewIEEE() },
	"CRC32C":    func() hash.Hash { return crc32.New(crc32.MakeTable(crc32.Castagnoli)) },
	"CRC64NVME": func() hash.Hash { return crc64.New(s3CRC64NVMETable) },
	"SHA1":      sha1.New,
	"SHA256":    sha256.New,
}

// CRC-64/NVME is the reflected polynomial 0xad93d23594c93659; crc64 takes its
// bit-reversed form and applies the all-ones initial value and final XOR.
var s3CRC64NVMETable = crc64.MakeTable(0x9a6c9329ac4bc9b5)

// s3ChecksumValue is the base64 of a checksum's big-endian digest, the form the
// x-amz-checksum-* headers carry.
func s3ChecksumValue(h hash.Hash) string {
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// s3ObjectChecksum is the additional checksum S3 keeps with an object.
type s3ObjectChecksum struct {
	Algorithm string `json:",omitempty"`
	Value     string `json:",omitempty"`
	// Type is FULL_OBJECT or COMPOSITE.
	Type string `json:",omitempty"`
}

// errS3BadDigest is a checksum the request stated that does not match the
// payload S3 received.
var errS3BadDigest = errors.New("the checksum does not match the payload")

// errS3MultipleChecksums is a request that supplies more than one checksum
// header: "If you supply multiple checksum headers, this request will fail."
var errS3MultipleChecksums = errors.New("multiple checksum headers")

// s3RequestedChecksum reads the algorithm a request names and the value it
// states in a header, or names in x-amz-trailer for the trailer to carry.
func s3RequestedChecksum(h http.Header) (algorithm, headerValue string, inTrailer bool, err error) {
	for name := range s3ChecksumAlgorithms {
		if v := h.Get("x-amz-checksum-" + strings.ToLower(name)); v != "" {
			if algorithm != "" {
				return "", "", false, errS3MultipleChecksums
			}
			algorithm, headerValue = name, v
		}
	}
	if trailer := strings.ToLower(strings.TrimSpace(h.Get("x-amz-trailer"))); strings.HasPrefix(trailer, "x-amz-checksum-") {
		name := strings.ToUpper(strings.TrimPrefix(trailer, "x-amz-checksum-"))
		if _, ok := s3ChecksumAlgorithms[name]; ok {
			if algorithm != "" && algorithm != name {
				return "", "", false, errS3MultipleChecksums
			}
			algorithm, inTrailer = name, true
		}
	}
	if algorithm == "" {
		if name := strings.ToUpper(h.Get("x-amz-sdk-checksum-algorithm")); name != "" {
			if _, ok := s3ChecksumAlgorithms[name]; ok {
				algorithm = name
			}
		}
	}
	return algorithm, headerValue, inTrailer, nil
}

// s3ChecksumReader hashes the payload as it is read and, at its end, compares
// the checksum with the value the request stated, in a header or the trailer
// the chunked reader collected; a mismatch ends the read with errS3BadDigest,
// so the object is not stored.
type s3ChecksumReader struct {
	r         io.Reader
	algorithm string
	// namedAlgorithm is the algorithm the request itself named, if any.
	namedAlgorithm string
	stated         string
	inTrailer      bool
	chunked        *awsChunkedReader
	h              hash.Hash
	Result         s3ObjectChecksum
}

func newS3ChecksumReader(r io.Reader, h http.Header, chunked *awsChunkedReader) (*s3ChecksumReader, error) {
	algorithm, stated, inTrailer, err := s3RequestedChecksum(h)
	if err != nil {
		return nil, err
	}
	named := algorithm
	if algorithm == "" {
		algorithm = "CRC64NVME"
	}
	return &s3ChecksumReader{r: r, algorithm: algorithm, namedAlgorithm: named, stated: stated, inTrailer: inTrailer,
		chunked: chunked, h: s3ChecksumAlgorithms[algorithm]()}, nil
}

// use hashes the payload with algorithm, the one a multipart upload named,
// when the part's request named none.
func (c *s3ChecksumReader) use(algorithm string) {
	if newHash, ok := s3ChecksumAlgorithms[algorithm]; ok && c.namedAlgorithm == "" {
		c.algorithm, c.h = algorithm, newHash()
	}
}

func (c *s3ChecksumReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	_, _ = c.h.Write(p[:n])
	if errors.Is(err, io.EOF) {
		got := s3ChecksumValue(c.h)
		stated := c.stated
		if c.inTrailer && c.chunked != nil {
			stated = c.chunked.Trailers["x-amz-checksum-"+strings.ToLower(c.algorithm)]
		}
		if stated != "" && stated != got {
			return n, errS3BadDigest
		}
		c.Result = s3ObjectChecksum{Algorithm: c.algorithm, Value: got, Type: "FULL_OBJECT"}
	}
	return n, err
}

// s3UploadBody unwraps a request's aws-chunked framing, verifying its chunk
// signatures when the upload is signed chunk by chunk, and hashes the payload
// for its additional checksum.
func s3UploadBody(r *http.Request) (*s3ChecksumReader, error) {
	var body io.Reader = r.Body
	var chunked *awsChunkedReader
	if isAWSChunkedRequest(r.Header) {
		signer, err := s3ChunkSigner(r)
		if err != nil {
			return nil, err
		}
		chunked = newSignedAWSChunkedReader(r.Body, signer)
		body = chunked
	}
	return newS3ChecksumReader(body, r.Header, chunked)
}

// s3ChunkSigner is the signer a signed streaming upload's chunks chain from:
// the request's own signature seeds it, under the key and scope the request
// was signed with.
func s3ChunkSigner(r *http.Request) (*awsChunkSigner, error) {
	payload := r.Header.Get("x-amz-content-sha256")
	signedChunks := payload == "STREAMING-AWS4-HMAC-SHA256-PAYLOAD" || payload == "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
	if !signedChunks {
		return nil, nil
	}
	cred, _, seed, ok := parseAuthzHeader(r.Header.Get("Authorization"))
	if !ok {
		return nil, errAWSChunkSignature
	}
	secret, serr := sigv4SecretFor(cred.accessKeyID, sigv4PresentedSessionToken(r))
	if serr != nil {
		return nil, errAWSChunkSignature
	}
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(cred.date))
	kRegion := hmacSHA256(kDate, []byte(cred.region))
	kService := hmacSHA256(kRegion, []byte(cred.service))
	return &awsChunkSigner{
		key:           hmacSHA256(kService, []byte("aws4_request")),
		amzDate:       r.Header.Get("X-Amz-Date"),
		scope:         cred.date + "/" + cred.region + "/" + cred.service + "/aws4_request",
		previous:      strings.ToLower(seed),
		signedChunks:  true,
		signedTrailer: payload == "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER",
	}, nil
}

// s3WriteChecksumHeaders answers x-amz-checksum-mode: ENABLED with the
// object's checksum and its type.
func s3WriteChecksumHeaders(w http.ResponseWriter, r *http.Request, c s3ObjectChecksum) {
	if c.Algorithm == "" || !strings.EqualFold(r.Header.Get("x-amz-checksum-mode"), "ENABLED") {
		return
	}
	w.Header().Set("x-amz-checksum-"+strings.ToLower(c.Algorithm), c.Value)
	w.Header().Set("x-amz-checksum-type", c.Type)
}

// s3CompositeChecksum is a multipart upload's checksum of its parts' checksums:
// the algorithm over the concatenated raw part checksums, suffixed with the
// part count.
func s3CompositeChecksum(algorithm string, parts []string) (string, bool) {
	newHash, ok := s3ChecksumAlgorithms[algorithm]
	if !ok {
		return "", false
	}
	h := newHash()
	for _, p := range parts {
		raw, err := base64.StdEncoding.DecodeString(p)
		if err != nil {
			return "", false
		}
		_, _ = h.Write(raw)
	}
	return s3ChecksumValue(h) + "-" + strconv.Itoa(len(parts)), true
}

// s3UploadBodyRefused answers an upload whose payload S3 refuses: a checksum
// that does not match is BadDigest, a chunk whose signature does not verify
// is SignatureDoesNotMatch, and more than one checksum header is refused.
func s3UploadBodyRefused(w http.ResponseWriter, r *http.Request, key string, err error) bool {
	switch {
	case errors.Is(err, errS3BadDigest):
		S3ErrorXML(w, "BadDigest", "The checksum you specified did not match the calculated checksum.", key, sim.RequestID(r.Context()), http.StatusBadRequest)
	case errors.Is(err, errAWSChunkSignature):
		S3ErrorXML(w, "SignatureDoesNotMatch", sigMsgMismatch, key, sim.RequestID(r.Context()), http.StatusForbidden)
	case errors.Is(err, errS3MultipleChecksums):
		S3ErrorXML(w, "InvalidRequest", "Expecting a single x-amz-checksum- header. Multiple checksum Types are not allowed.", key, sim.RequestID(r.Context()), http.StatusBadRequest)
	default:
		return false
	}
	return true
}

// s3PartChecksum is a stored part's checksum under its upload's algorithm.
func s3PartChecksum(uploadID, ref string) string {
	upload, ok := s3MultipartUploads.Get(uploadID)
	if !ok {
		return ""
	}
	newHash, ok := s3ChecksumAlgorithms[upload.ChecksumAlgorithm]
	if !ok {
		return ""
	}
	data, err := s3Bodies.Read(ref)
	if err != nil {
		return ""
	}
	h := newHash()
	_, _ = h.Write(data)
	return s3ChecksumValue(h)
}

// s3MultipartChecksum is a completed upload's checksum: of the whole object
// for the full object type, which CRC64NVME, the algorithm S3 uses when the
// upload named none, always is; of the parts' checksums for the composite
// type, the default for the others.
func s3MultipartChecksum(upload S3MultipartUpload, parts []struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}, assembled string) (s3ObjectChecksum, error) {
	algorithm, kind := upload.ChecksumAlgorithm, upload.ChecksumType
	if algorithm == "" {
		algorithm = "CRC64NVME"
	}
	if kind == "" {
		kind = "COMPOSITE"
		if algorithm == "CRC64NVME" {
			kind = "FULL_OBJECT"
		}
	}
	if kind == "COMPOSITE" {
		values := make([]string, 0, len(parts))
		for _, p := range parts {
			values = append(values, upload.Parts[p.PartNumber].Checksum)
		}
		if value, ok := s3CompositeChecksum(algorithm, values); ok {
			return s3ObjectChecksum{Algorithm: algorithm, Value: value, Type: kind}, nil
		}
		return s3ObjectChecksum{}, nil
	}
	data, err := s3Bodies.Read(assembled)
	if err != nil {
		return s3ObjectChecksum{}, err
	}
	h := s3ChecksumAlgorithms[algorithm]()
	_, _ = h.Write(data)
	return s3ObjectChecksum{Algorithm: algorithm, Value: s3ChecksumValue(h), Type: "FULL_OBJECT"}, nil
}
