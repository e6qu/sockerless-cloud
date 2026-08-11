package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HugoSmits86/nativewebp"
	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Amplify Hosting image optimization primitive. Deploy manifests route
// Next.js-style `/_next/image`-shaped endpoints to the ImageOptimization
// target kind; the service validates the request against the manifest's
// imageSettings, fetches the source image (the app's own static artifacts
// for relative URLs, a real HTTP fetch for allowed remote URLs), resizes it
// to the requested width, and re-encodes it in the Accept-negotiated output
// format. Amplify runs the Next.js image optimizer for this primitive, so
// query parameters (url, w, q), validation error strings, passthrough rules
// for SVG/animated/bypass types, and response headers mirror the Next.js
// image optimizer contract.

// ---------- imageSettings ----------

// amplifyImageSettings mirrors the deployment specification's ImageSettings
// object (deploy-manifest.json `imageSettings`).
type amplifyImageSettings struct {
	Sizes          []int                  `json:"sizes"`
	Domains        []string               `json:"domains"`
	RemotePatterns []amplifyRemotePattern `json:"remotePatterns"`
	Formats        []string               `json:"formats"`
	// AWS's deployment-specification document spells the cache-TTL key
	// `minimumCacheTTL` in its property table but `minumumCacheTTL` in its
	// type definition and example manifest; both spellings are honored, the
	// property-table spelling taking precedence.
	MinimumCacheTTL     *int `json:"minimumCacheTTL"`
	MinumumCacheTTL     *int `json:"minumumCacheTTL"`
	DangerouslyAllowSVG bool `json:"dangerouslyAllowSVG"`
}

func (s *amplifyImageSettings) minimumCacheTTL() int {
	if s.MinimumCacheTTL != nil {
		return *s.MinimumCacheTTL
	}
	if s.MinumumCacheTTL != nil {
		return *s.MinumumCacheTTL
	}
	return 0
}

// amplifyRemotePattern mirrors the deployment specification's RemotePattern
// object. Hostname wildcards follow the documented contract: `*` matches a
// single subdomain label, `**` matches any number of subdomains.
type amplifyRemotePattern struct {
	Protocol string `json:"protocol"`
	Hostname string `json:"hostname"`
	Port     string `json:"port"`
	Pathname string `json:"pathname"`
}

func amplifyParseImageSettings(raw json.RawMessage) (*amplifyImageSettings, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("deploy-manifest has no imageSettings (required for ImageOptimization routes)")
	}
	var settings amplifyImageSettings
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, fmt.Errorf("invalid imageSettings: %w", err)
	}
	return &settings, nil
}

// ---------- source URL allow-listing ----------

func (p amplifyRemotePattern) matches(u *url.URL) bool {
	if p.Protocol != "" && p.Protocol != u.Scheme {
		return false
	}
	if p.Port != u.Port() {
		return false
	}
	if !amplifyWildcardMatch(p.Hostname, u.Hostname(), ".") {
		return false
	}
	if p.Pathname != "" && !amplifyWildcardMatch(p.Pathname, u.Path, "/") {
		return false
	}
	return true
}

// amplifyWildcardMatch matches a remote-pattern component: `**` matches any
// number of separator-delimited segments, `*` matches within one segment.
func amplifyWildcardMatch(pattern, value, separator string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); {
		switch {
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i += 2
		case pattern[i] == '*':
			b.WriteString("[^" + regexp.QuoteMeta(separator) + "]*")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(value)
}

func (s *amplifyImageSettings) remoteAllowed(u *url.URL) bool {
	for _, domain := range s.Domains {
		if strings.EqualFold(domain, u.Hostname()) {
			return true
		}
	}
	for _, pattern := range s.RemotePatterns {
		if pattern.matches(u) {
			return true
		}
	}
	return false
}

// ---------- format negotiation ----------

// amplifyEncodableImageFormats are the output formats the optimizer can
// produce. AVIF is a valid imageSettings entry but has no trustworthy pure-Go
// encoder, so it never wins negotiation — the client is served the next
// format it accepts, and Content-Type always matches the actual bytes.
var amplifyEncodableImageFormats = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// amplifyNegotiateImageFormat picks the output format the way the Next.js
// image optimizer does: the client's Accept media ranges in preference order
// (q-value descending, then listed order) intersected with the manifest's
// formats. Only media types spelled out literally in the Accept header count
// (the optimizer discards wildcard-derived matches); no match keeps the
// source format.
func amplifyNegotiateImageFormat(formats []string, accept string) string {
	type mediaRange struct {
		mime  string
		q     float64
		index int
	}
	var ranges []mediaRange
	for i, part := range strings.Split(accept, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		mime := strings.ToLower(strings.TrimSpace(fields[0]))
		if mime == "" {
			continue
		}
		q := 1.0
		for _, param := range fields[1:] {
			if value, found := strings.CutPrefix(strings.TrimSpace(param), "q="); found {
				if parsed, err := strconv.ParseFloat(value, 64); err == nil {
					q = parsed
				}
			}
		}
		ranges = append(ranges, mediaRange{mime: mime, q: q, index: i})
	}
	sort.SliceStable(ranges, func(i, k int) bool { return ranges[i].q > ranges[k].q })
	for _, r := range ranges {
		if r.q <= 0 {
			continue
		}
		for _, format := range formats {
			if r.mime == strings.ToLower(format) && amplifyEncodableImageFormats[r.mime] {
				return r.mime
			}
		}
	}
	return ""
}

// ---------- source type detection ----------

// amplifyDetectImageType sniffs the source's real content type from magic
// bytes (the optimizer's detectContentType), never trusting extensions or
// upstream headers.
func amplifyDetectImageType(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg"
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return "image/png"
	case len(data) >= 4 && bytes.Equal(data[:4], []byte("GIF8")):
		return "image/gif"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	case len(data) >= 12 && bytes.Equal(data[4:12], []byte("ftypavif")):
		return "image/avif"
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x00, 0x00, 0x01, 0x00}):
		return "image/x-icon"
	case len(data) >= 4 && bytes.Equal(data[:4], []byte("icns")):
		return "image/x-icns"
	case len(data) >= 2 && bytes.Equal(data[:2], []byte("BM")):
		return "image/bmp"
	case len(data) >= 4 && (bytes.Equal(data[:4], []byte{'I', 'I', 0x2a, 0x00}) || bytes.Equal(data[:4], []byte{'M', 'M', 0x00, 0x2a})):
		return "image/tiff"
	}
	trimmed := bytes.TrimLeft(data, " \t\r\n\xef\xbb\xbf")
	if bytes.HasPrefix(trimmed, []byte("<?xml")) || bytes.HasPrefix(trimmed, []byte("<svg")) {
		return "image/svg+xml"
	}
	return ""
}

// amplifyImagePassthroughType reports whether the optimizer serves this type
// unmodified (the optimizer's BYPASS_TYPES; SVG reaches here only when
// dangerouslyAllowSVG admitted it).
func amplifyImagePassthroughType(contentType string) bool {
	switch contentType {
	case "image/svg+xml", "image/x-icon", "image/x-icns", "image/bmp", "image/tiff", "image/avif":
		return true
	}
	return false
}

// amplifyImageIsAnimated reports whether an animatable source (GIF, APNG,
// animated WebP) actually animates — animated sources are served unmodified.
func amplifyImageIsAnimated(contentType string, data []byte) bool {
	switch contentType {
	case "image/gif":
		g, err := gif.DecodeAll(bytes.NewReader(data))
		return err == nil && len(g.Image) > 1
	case "image/png":
		// APNG: an acTL chunk appears before the first IDAT.
		for offset := 8; offset+8 <= len(data); {
			length := int(binary.BigEndian.Uint32(data[offset : offset+4]))
			chunkType := string(data[offset+4 : offset+8])
			if chunkType == "acTL" {
				return true
			}
			if chunkType == "IDAT" || chunkType == "IEND" {
				return false
			}
			offset += 12 + length
		}
		return false
	case "image/webp":
		// The VP8X extended-format chunk's animation flag.
		return len(data) >= 21 && bytes.Equal(data[12:16], []byte("VP8X")) && data[20]&0x02 != 0
	}
	return false
}

// ---------- transformed-output cache ----------

// amplifyStoredOptimizedImage is one cached transform, keyed by (deployment,
// source URL, width, quality, negotiated format) so a new deployment or a
// different negotiation never serves stale bytes.
type amplifyStoredOptimizedImage struct {
	Key         string
	AppId       string
	BranchName  string
	Data        []byte
	ContentType string
	ETag        string
	MaxAge      int
	// ExpiresAt bounds remote-source entries by their max-age (the remote
	// may change); zero marks deployment-artifact entries, immutable for the
	// job they are keyed to.
	ExpiresAt int64
}

var amplifyOptimizedImages sim.Store[amplifyStoredOptimizedImage]

func amplifyOptimizedImageKey(appID, branch, jobID, srcURL string, width, quality int, format string) string {
	hash := sha256.Sum256(fmt.Appendf(nil, "%s\x1f%s\x1f%s\x1f%s\x1f%d\x1f%d\x1f%s", appID, branch, jobID, srcURL, width, quality, format))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func amplifyPurgeOptimizedImages(appID, branch string) {
	for _, entry := range amplifyOptimizedImages.List() {
		if entry.AppId == appID && (branch == "" || entry.BranchName == branch) {
			amplifyOptimizedImages.Delete(entry.Key)
		}
	}
}

// ---------- request handling ----------

func amplifyImageOptError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	http.Error(w, message, status)
}

// amplifyServeImageOptimization serves one ImageOptimization-routed request:
// validate → cache lookup → fetch source → transform → respond + cache.
// Reports whether the request was handled: with interceptNotFound, a source
// that would produce a 404 writes nothing so the route's fallback target
// applies.
func amplifyServeImageOptimization(w http.ResponseWriter, r *http.Request, app AmplifyApp, br AmplifyBranch, content *amplifyHostedContent, target *amplifyManifestTarget, interceptNotFound bool) bool {
	settings, err := amplifyParseImageSettings(content.Manifest.ImageSettings)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return true
	}
	query := r.URL.Query()

	srcURL, errMessage := amplifyValidateImageURL(query["url"], content.Manifest, settings)
	if errMessage != "" {
		amplifyImageOptError(w, http.StatusBadRequest, errMessage)
		return true
	}
	width, errMessage := amplifyValidateImageWidth(query["w"], settings.Sizes)
	if errMessage != "" {
		amplifyImageOptError(w, http.StatusBadRequest, errMessage)
		return true
	}
	quality, errMessage := amplifyValidateImageQuality(query["q"])
	if errMessage != "" {
		amplifyImageOptError(w, http.StatusBadRequest, errMessage)
		return true
	}
	format := amplifyNegotiateImageFormat(settings.Formats, r.Header.Get("Accept"))

	cacheKey := amplifyOptimizedImageKey(app.AppId, br.BranchName, content.JobID, srcURL, width, quality, format)
	if entry, ok := amplifyOptimizedImages.Get(cacheKey); ok {
		if entry.ExpiresAt == 0 || time.Now().Unix() < entry.ExpiresAt {
			amplifyWriteOptimizedImage(w, r, target, srcURL, entry)
			return true
		}
		amplifyOptimizedImages.Delete(cacheKey)
	}

	source, upstreamMaxAge, fetchStatus, fetchErrMessage := amplifyFetchImageSource(content, srcURL)
	if fetchErrMessage != "" {
		if interceptNotFound && fetchStatus == http.StatusNotFound {
			return false
		}
		amplifyImageOptError(w, fetchStatus, fetchErrMessage)
		return true
	}
	sourceType := amplifyDetectImageType(source)
	if sourceType == "" {
		amplifyImageOptError(w, http.StatusBadRequest, "The requested resource isn't a valid image.")
		return true
	}
	if sourceType == "image/svg+xml" && !settings.DangerouslyAllowSVG {
		amplifyImageOptError(w, http.StatusBadRequest, `"url" parameter is valid but image type is not allowed`)
		return true
	}

	maxAge := settings.minimumCacheTTL()
	if upstreamMaxAge > maxAge {
		maxAge = upstreamMaxAge
	}
	output, outputType := source, sourceType
	if !amplifyImagePassthroughType(sourceType) && !amplifyImageIsAnimated(sourceType, source) {
		if transformed, transformedType, err := amplifyTransformImage(source, sourceType, width, quality, format); err == nil {
			output, outputType = transformed, transformedType
		}
		// A source that decodes as none of the transformable formats falls
		// back to the unmodified upstream bytes, the optimizer's own
		// fallback when it cannot optimize.
	}

	hash := sha256.Sum256(output)
	entry := amplifyStoredOptimizedImage{
		Key:         cacheKey,
		AppId:       app.AppId,
		BranchName:  br.BranchName,
		Data:        output,
		ContentType: outputType,
		ETag:        `"` + base64.RawURLEncoding.EncodeToString(hash[:]) + `"`,
		MaxAge:      maxAge,
	}
	if strings.HasPrefix(srcURL, "http://") || strings.HasPrefix(srcURL, "https://") {
		entry.ExpiresAt = time.Now().Unix() + int64(maxAge)
	}
	amplifyOptimizedImages.Put(cacheKey, entry)
	amplifyWriteOptimizedImage(w, r, target, srcURL, entry)
	return true
}

// amplifyValidateImageURL validates the url parameter and reports the source
// URL, or the optimizer's 400 error string.
func amplifyValidateImageURL(values []string, manifest *amplifyDeployManifest, settings *amplifyImageSettings) (string, string) {
	if len(values) == 0 || values[0] == "" {
		return "", `"url" parameter is required`
	}
	if len(values) > 1 {
		return "", `"url" parameter cannot be an array`
	}
	srcURL := values[0]
	if len(srcURL) > 3072 {
		return "", `"url" parameter is too long`
	}
	if strings.HasPrefix(srcURL, "//") {
		return "", `"url" parameter cannot be a protocol-relative URL (//)`
	}
	if strings.HasPrefix(srcURL, "http://") || strings.HasPrefix(srcURL, "https://") {
		parsed, err := url.Parse(srcURL)
		if err != nil || parsed.Hostname() == "" {
			return "", `"url" parameter is invalid`
		}
		if !settings.remoteAllowed(parsed) {
			return "", `"url" parameter is not allowed`
		}
		return srcURL, ""
	}
	if !strings.HasPrefix(srcURL, "/") {
		return "", `"url" parameter is invalid`
	}
	parsed, err := url.Parse(srcURL)
	if err != nil {
		return "", `"url" parameter is invalid`
	}
	for _, route := range manifest.Routes {
		if route.Target != nil && route.Target.Kind == "ImageOptimization" && manifest.routeMatches(route.Path, parsed.Path) {
			return "", `"url" parameter cannot be recursive`
		}
	}
	return srcURL, ""
}

var amplifyImageParamDigits = regexp.MustCompile(`^[0-9]+$`)

func amplifyValidateImageWidth(values []string, sizes []int) (int, string) {
	if len(values) == 0 || values[0] == "" {
		return 0, `"w" parameter (width) is required`
	}
	if len(values) > 1 {
		return 0, `"w" parameter (width) cannot be an array`
	}
	if !amplifyImageParamDigits.MatchString(values[0]) {
		return 0, `"w" parameter (width) must be an integer greater than 0`
	}
	width, err := strconv.Atoi(values[0])
	if err != nil || width <= 0 {
		return 0, `"w" parameter (width) must be an integer greater than 0`
	}
	for _, size := range sizes {
		if size == width {
			return width, ""
		}
	}
	return 0, fmt.Sprintf(`"w" parameter (width) of %d is not allowed`, width)
}

func amplifyValidateImageQuality(values []string) (int, string) {
	if len(values) == 0 || values[0] == "" {
		return 0, `"q" parameter (quality) is required`
	}
	if len(values) > 1 {
		return 0, `"q" parameter (quality) cannot be an array`
	}
	if !amplifyImageParamDigits.MatchString(values[0]) {
		return 0, `"q" parameter (quality) must be an integer between 1 and 100`
	}
	quality, err := strconv.Atoi(values[0])
	if err != nil || quality < 1 || quality > 100 {
		return 0, `"q" parameter (quality) must be an integer between 1 and 100`
	}
	return quality, ""
}

// amplifyFetchImageSource resolves the source bytes: relative URLs from the
// deployment's own static artifacts, absolute URLs by real HTTP fetch. The
// remote's Cache-Control (s-maxage, then max-age) rides back so the response
// honors the larger of it and minimumCacheTTL.
func amplifyFetchImageSource(content *amplifyHostedContent, srcURL string) (data []byte, upstreamMaxAge, errStatus int, errMessage string) {
	if strings.HasPrefix(srcURL, "http://") || strings.HasPrefix(srcURL, "https://") {
		client := http.Client{Timeout: 30 * time.Second}
		resp, err := client.Get(srcURL)
		if err != nil {
			return nil, 0, http.StatusBadRequest, `"url" parameter is valid but upstream response is invalid`
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, 0, resp.StatusCode, `"url" parameter is valid but upstream response is invalid`
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, 0, http.StatusBadRequest, `"url" parameter is valid but upstream response is invalid`
		}
		return body, amplifyCacheControlMaxAge(resp.Header.Get("Cache-Control")), 0, ""
	}
	parsed, err := url.Parse(srcURL)
	if err != nil {
		return nil, 0, http.StatusBadRequest, `"url" parameter is invalid`
	}
	key, ok := amplifyResolveFile(content.Files, "static"+path.Clean("/"+parsed.Path))
	if !ok {
		return nil, 0, http.StatusNotFound, `"url" parameter is valid but internal response is invalid`
	}
	return content.Files[key], 0, 0, ""
}

// amplifyCacheControlMaxAge extracts s-maxage (preferred) or max-age from an
// upstream Cache-Control header; absent or unparsable directives are 0.
func amplifyCacheControlMaxAge(header string) int {
	values := map[string]string{}
	for _, directive := range strings.Split(header, ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(directive), "=")
		values[strings.ToLower(name)] = strings.Trim(value, `"`)
	}
	for _, name := range []string{"s-maxage", "max-age"} {
		if age, err := strconv.Atoi(values[name]); err == nil {
			return age
		}
	}
	return 0
}

// amplifyTransformImage decodes the source, resizes it to the target width
// preserving aspect ratio (never enlarging beyond the source width), and
// re-encodes it in the negotiated format — the source's own format when
// negotiation picked none.
func amplifyTransformImage(source []byte, sourceType string, width, quality int, format string) ([]byte, string, error) {
	src, _, err := image.Decode(bytes.NewReader(source))
	if err != nil {
		return nil, "", err
	}
	bounds := src.Bounds()
	targetWidth := width
	if targetWidth > bounds.Dx() {
		targetWidth = bounds.Dx()
	}
	targetHeight := (bounds.Dy()*targetWidth + bounds.Dx()/2) / bounds.Dx()
	if targetHeight < 1 {
		targetHeight = 1
	}
	scaled := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), src, bounds, draw.Src, nil)

	if format == "" {
		format = sourceType
	}
	var buf bytes.Buffer
	switch format {
	case "image/jpeg":
		err = jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: quality})
	case "image/png":
		err = png.Encode(&buf, scaled)
	case "image/gif":
		err = gif.Encode(&buf, scaled, nil)
	case "image/webp":
		err = nativewebp.Encode(&buf, scaled, nil)
	default:
		return nil, "", fmt.Errorf("no encoder for %s", format)
	}
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), format, nil
}

var amplifyImageExtensions = map[string]string{
	"image/jpeg":    "jpeg",
	"image/png":     "png",
	"image/gif":     "gif",
	"image/webp":    "webp",
	"image/avif":    "avif",
	"image/svg+xml": "svg",
	"image/x-icon":  "ico",
	"image/x-icns":  "icns",
	"image/bmp":     "bmp",
	"image/tiff":    "tiff",
}

// amplifyWriteOptimizedImage emits one optimized-image response: negotiated
// Content-Type, Cache-Control (the route target's cacheControl for 200s when
// the manifest sets one, otherwise the optimizer's
// `public, max-age=…, must-revalidate` with the minimumCacheTTL floor),
// Vary: Accept, ETag with If-None-Match revalidation, and the optimizer's
// standing security headers.
func amplifyWriteOptimizedImage(w http.ResponseWriter, r *http.Request, target *amplifyManifestTarget, srcURL string, entry amplifyStoredOptimizedImage) {
	header := w.Header()
	header.Set("Vary", "Accept")
	if target.CacheControl != "" {
		header.Set("Cache-Control", target.CacheControl)
	} else {
		header.Set("Cache-Control", fmt.Sprintf("public, max-age=%d, must-revalidate", entry.MaxAge))
	}
	header.Set("ETag", entry.ETag)
	header.Set("Content-Security-Policy", "script-src 'none'; frame-src 'none'; sandbox;")
	header.Set("X-Content-Type-Options", "nosniff")
	if amplifyIfNoneMatchHit(r.Header.Get("If-None-Match"), entry.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	fileName := "image"
	if parsed, err := url.Parse(srcURL); err == nil {
		if base := strings.TrimSuffix(path.Base(parsed.Path), path.Ext(parsed.Path)); base != "" && base != "." && base != "/" {
			fileName = base
		}
	}
	if ext := amplifyImageExtensions[entry.ContentType]; ext != "" {
		fileName += "." + ext
	}
	header.Set("Content-Type", entry.ContentType)
	header.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	header.Set("Content-Length", strconv.Itoa(len(entry.Data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.Data)
}

func amplifyIfNoneMatchHit(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" {
		return false
	}
	if strings.TrimSpace(ifNoneMatch) == "*" {
		return true
	}
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == etag {
			return true
		}
	}
	return false
}
