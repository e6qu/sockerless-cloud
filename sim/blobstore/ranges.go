package blobstore

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// RangeOpts is the byte-range grammar a service accepts beyond first-last.
type RangeOpts struct {
	// AllowSuffix accepts "-N", the last N bytes.
	AllowSuffix bool
	// AllowOpenEnd accepts "N-", everything from N.
	AllowOpenEnd bool
	// ClampEnd cuts a range that runs past the end at the end; without it
	// such a range is unsatisfiable.
	ClampEnd bool
}

// ByteRange is one parsed "bytes=" range, not yet resolved against a size.
type ByteRange struct {
	first, last int64
	suffix      bool
	openEnd     bool
	clamp       bool
}

// ErrMalformedRange reports a range header that does not parse under the
// grammar the service accepts.
var ErrMalformedRange = errors.New("malformed byte range")

// ParseRange parses a single "bytes=first-last" range under opts. It rejects
// a list of ranges, which none of the storage services serves.
func ParseRange(header string, opts RangeOpts) (ByteRange, error) {
	spec, found := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !found || strings.Contains(spec, ",") {
		return ByteRange{}, ErrMalformedRange
	}
	lo, hi, found := strings.Cut(spec, "-")
	if !found {
		return ByteRange{}, ErrMalformedRange
	}
	lo, hi = strings.TrimSpace(lo), strings.TrimSpace(hi)
	number := func(s string) (int64, bool) {
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil && n >= 0 && !strings.HasPrefix(s, "+")
	}
	out := ByteRange{clamp: opts.ClampEnd}
	switch {
	case lo == "":
		n, ok := number(hi)
		if !opts.AllowSuffix || !ok {
			return ByteRange{}, ErrMalformedRange
		}
		out.suffix, out.last = true, n
	case hi == "":
		n, ok := number(lo)
		if !opts.AllowOpenEnd || !ok {
			return ByteRange{}, ErrMalformedRange
		}
		out.openEnd, out.first = true, n
	default:
		first, okFirst := number(lo)
		last, okLast := number(hi)
		if !okFirst || !okLast || last < first {
			return ByteRange{}, ErrMalformedRange
		}
		out.first, out.last = first, last
	}
	return out, nil
}

// Bounds returns the offsets a first-last range names, before any size
// bounds them, and false for a suffix or open-ended range.
func (b ByteRange) Bounds() (first, last int64, ok bool) {
	return b.first, b.last, !b.suffix && !b.openEnd
}

// Resolve places the range in a resource of size bytes, returning its first
// and last offsets, or false when no byte of the resource is in it.
func (b ByteRange) Resolve(size int64) (start, end int64, ok bool) {
	if b.suffix {
		if b.last == 0 || size == 0 {
			return 0, 0, false
		}
		return max(size-b.last, 0), size - 1, true
	}
	if b.first >= size {
		return 0, 0, false
	}
	if b.openEnd {
		return b.first, size - 1, true
	}
	if b.last >= size {
		if !b.clamp {
			return 0, 0, false
		}
		return b.first, size - 1, true
	}
	return b.first, b.last, true
}

// ServeRange answers with bytes start through end of body, a resource of size
// bytes, as 206 Partial Content, and writes no body to a HEAD. It returns an
// error, having written nothing, when body cannot be positioned.
func ServeRange(w http.ResponseWriter, r *http.Request, body io.ReadSeeker, start, end, size int64) error {
	if _, err := body.Seek(start, io.SeekStart); err != nil {
		return fmt.Errorf("seek to byte %d: %w", start, err)
	}
	h := w.Header()
	h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	w.WriteHeader(http.StatusPartialContent)
	if r.Method != http.MethodHead {
		_, _ = io.CopyN(w, body, end-start+1)
	}
	return nil
}

// ServeWhole answers with all size bytes of body as 200 OK, and writes no body
// to a HEAD.
func ServeWhole(w http.ResponseWriter, r *http.Request, body io.Reader, size int64) {
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.CopyN(w, body, size)
	}
}
