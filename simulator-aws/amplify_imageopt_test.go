package main

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Docker-free image-optimization tests: format negotiation, remote-pattern
// matching, parameter validation, source-type detection, and the full
// hosted-request transform path served from in-memory stores.

func TestAmplifyNegotiateImageFormat(t *testing.T) {
	cases := []struct {
		name    string
		formats []string
		accept  string
		want    string
	}{
		{"webp negotiated", []string{"image/webp"}, "image/webp,*/*;q=0.8", "image/webp"},
		{"avif never wins without encoder", []string{"image/avif", "image/webp"}, "image/avif,image/webp", "image/webp"},
		{"wildcard alone is not a literal match", []string{"image/webp"}, "*/*", ""},
		{"empty accept keeps source format", []string{"image/webp"}, "", ""},
		{"q ordering", []string{"image/webp", "image/jpeg"}, "image/webp;q=0.1,image/jpeg;q=0.9", "image/jpeg"},
		{"q zero excluded", []string{"image/webp"}, "image/webp;q=0", ""},
		{"format not configured", []string{"image/webp"}, "image/png", ""},
		{"png configured and accepted", []string{"image/png"}, "image/png", "image/png"},
	}
	for _, tc := range cases {
		if got := amplifyNegotiateImageFormat(tc.formats, tc.accept); got != tc.want {
			t.Errorf("%s: negotiate(%v, %q) = %q, want %q", tc.name, tc.formats, tc.accept, got, tc.want)
		}
	}
}

func TestAmplifyRemotePatternMatch(t *testing.T) {
	mustURL := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		return u
	}
	cases := []struct {
		name    string
		pattern amplifyRemotePattern
		rawURL  string
		want    bool
	}{
		{"literal hostname", amplifyRemotePattern{Hostname: "example.com"}, "https://example.com/a.png", true},
		{"other hostname", amplifyRemotePattern{Hostname: "example.com"}, "https://evil.com/a.png", false},
		{"single-label wildcard", amplifyRemotePattern{Hostname: "*.example.com"}, "https://img.example.com/a.png", true},
		{"single-label wildcard rejects two labels", amplifyRemotePattern{Hostname: "*.example.com"}, "https://a.b.example.com/a.png", false},
		{"double wildcard spans labels", amplifyRemotePattern{Hostname: "**.example.com"}, "https://a.b.example.com/a.png", true},
		{"protocol mismatch", amplifyRemotePattern{Protocol: "https", Hostname: "example.com"}, "http://example.com/a.png", false},
		{"port mismatch", amplifyRemotePattern{Hostname: "example.com", Port: "8443"}, "https://example.com/a.png", false},
		{"port match", amplifyRemotePattern{Hostname: "example.com", Port: "8443"}, "https://example.com:8443/a.png", true},
		{"pathname wildcard", amplifyRemotePattern{Hostname: "example.com", Pathname: "/img/**"}, "https://example.com/img/a/b.png", true},
		{"pathname mismatch", amplifyRemotePattern{Hostname: "example.com", Pathname: "/img/**"}, "https://example.com/other/a.png", false},
		{"pathname single segment", amplifyRemotePattern{Hostname: "example.com", Pathname: "/img/*"}, "https://example.com/img/a/b.png", false},
	}
	for _, tc := range cases {
		if got := tc.pattern.matches(mustURL(tc.rawURL)); got != tc.want {
			t.Errorf("%s: %+v.matches(%s) = %v, want %v", tc.name, tc.pattern, tc.rawURL, got, tc.want)
		}
	}
}

func TestAmplifyValidateImageParams(t *testing.T) {
	manifest := &amplifyDeployManifest{Routes: []amplifyManifestRoute{
		{Path: "/_next/image", Target: &amplifyManifestTarget{Kind: "ImageOptimization"}},
		{Path: "/*", Target: &amplifyManifestTarget{Kind: "Static"}},
	}}
	settings := &amplifyImageSettings{Domains: []string{"allowed.example.com"}}

	urlCases := []struct {
		name   string
		values []string
		want   string
	}{
		{"missing", nil, `"url" parameter is required`},
		{"empty", []string{""}, `"url" parameter is required`},
		{"array", []string{"/a.png", "/b.png"}, `"url" parameter cannot be an array`},
		{"too long", []string{"/" + strings.Repeat("a", 3072)}, `"url" parameter is too long`},
		{"protocol relative", []string{"//cdn.example.com/a.png"}, `"url" parameter cannot be a protocol-relative URL (//)`},
		{"disallowed remote", []string{"https://evil.example.com/a.png"}, `"url" parameter is not allowed`},
		{"allowed remote", []string{"https://allowed.example.com/a.png"}, ""},
		{"relative without slash", []string{"a.png"}, `"url" parameter is invalid`},
		{"recursive", []string{"/_next/image?url=%2Fa.png&w=64&q=75"}, `"url" parameter cannot be recursive`},
		{"relative ok", []string{"/img/a.png"}, ""},
	}
	for _, tc := range urlCases {
		if _, got := amplifyValidateImageURL(tc.values, manifest, settings); got != tc.want {
			t.Errorf("url %s: got %q, want %q", tc.name, got, tc.want)
		}
	}

	widthCases := []struct {
		name   string
		values []string
		want   string
	}{
		{"missing", nil, `"w" parameter (width) is required`},
		{"array", []string{"64", "128"}, `"w" parameter (width) cannot be an array`},
		{"not a number", []string{"abc"}, `"w" parameter (width) must be an integer greater than 0`},
		{"decimal", []string{"64.5"}, `"w" parameter (width) must be an integer greater than 0`},
		{"zero", []string{"0"}, `"w" parameter (width) must be an integer greater than 0`},
		{"not in sizes", []string{"100"}, `"w" parameter (width) of 100 is not allowed`},
		{"allowed", []string{"64"}, ""},
	}
	for _, tc := range widthCases {
		if _, got := amplifyValidateImageWidth(tc.values, []int{64, 128}); got != tc.want {
			t.Errorf("width %s: got %q, want %q", tc.name, got, tc.want)
		}
	}

	qualityCases := []struct {
		name   string
		values []string
		want   string
	}{
		{"missing", nil, `"q" parameter (quality) is required`},
		{"array", []string{"75", "80"}, `"q" parameter (quality) cannot be an array`},
		{"not a number", []string{"high"}, `"q" parameter (quality) must be an integer between 1 and 100`},
		{"zero", []string{"0"}, `"q" parameter (quality) must be an integer between 1 and 100`},
		{"over 100", []string{"101"}, `"q" parameter (quality) must be an integer between 1 and 100`},
		{"valid", []string{"75"}, ""},
	}
	for _, tc := range qualityCases {
		if _, got := amplifyValidateImageQuality(tc.values); got != tc.want {
			t.Errorf("quality %s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func amplifyTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 251), G: uint8(y * 241), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestAmplifyDetectImageTypeAndAnimation(t *testing.T) {
	pngBytes := amplifyTestPNG(t, 4, 4)
	if got := amplifyDetectImageType(pngBytes); got != "image/png" {
		t.Errorf("png detected as %q", got)
	}
	if got := amplifyDetectImageType([]byte("  <svg xmlns='http://www.w3.org/2000/svg'/>")); got != "image/svg+xml" {
		t.Errorf("svg detected as %q", got)
	}
	if got := amplifyDetectImageType([]byte("not an image")); got != "" {
		t.Errorf("junk detected as %q", got)
	}

	frame := image.NewPaletted(image.Rect(0, 0, 4, 4), []color.Color{color.Black, color.White})
	var oneFrame, twoFrames bytes.Buffer
	if err := gif.EncodeAll(&oneFrame, &gif.GIF{Image: []*image.Paletted{frame}, Delay: []int{10}}); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	if err := gif.EncodeAll(&twoFrames, &gif.GIF{Image: []*image.Paletted{frame, frame}, Delay: []int{10, 10}}); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	if amplifyImageIsAnimated("image/gif", oneFrame.Bytes()) {
		t.Error("single-frame gif reported animated")
	}
	if !amplifyImageIsAnimated("image/gif", twoFrames.Bytes()) {
		t.Error("two-frame gif not reported animated")
	}
	if amplifyImageIsAnimated("image/png", pngBytes) {
		t.Error("plain png reported animated")
	}
}

func amplifyImageOptManifest(extraSettings string) string {
	return `{
		"version": 1,
		"framework": {"name": "custom", "version": "1.0.0"},
		"routes": [
			{"path": "/_next/image", "target": {"kind": "ImageOptimization"}},
			{"path": "/*", "target": {"kind": "Static"}}
		],
		"imageSettings": {
			"sizes": [16, 64],
			"domains": [],
			"remotePatterns": [],
			"formats": ["image/avif", "image/webp"],
			"minimumCacheTTL": 60,
			"dangerouslyAllowSVG": false` + extraSettings + `
		}
	}`
}

func TestAmplifyHostingImageOptimizationServing(t *testing.T) {
	amplifyResetHostingState()
	amplifySeedApp("imgapp1", "main")
	amplifyApps.Update("imgapp1", func(a *amplifyStoredApp) { a.App.Platform = "WEB_COMPUTE" })
	pngBytes := amplifyTestPNG(t, 100, 60)
	amplifySeedDeployment(t, "imgapp1", "main", "job-img-1", map[string]string{
		"deploy-manifest.json":  amplifyImageOptManifest(""),
		"static/img/photo.png":  string(pngBytes),
		"static/img/vector.svg": "<svg xmlns='http://www.w3.org/2000/svg'></svg>",
	})
	host := "main.imgapp1.amplifyapp.com"

	// Resize to an allowed width, source format kept without Accept.
	rec := amplifyHostingGet(t, host, "/_next/image?url=%2Fimg%2Fphoto.png&w=64&q=75", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("resize: status %d body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("resize content-type %q", got)
	}
	decoded, format, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode resized: %v", err)
	}
	if format != "png" || decoded.Bounds().Dx() != 64 || decoded.Bounds().Dy() != 38 {
		t.Errorf("resized to %s %dx%d, want png 64x38", format, decoded.Bounds().Dx(), decoded.Bounds().Dy())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60, must-revalidate" {
		t.Errorf("cache-control %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept" {
		t.Errorf("vary %q", got)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	// If-None-Match revisit answers 304 without a body.
	rec = amplifyHostingGet(t, host, "/_next/image?url=%2Fimg%2Fphoto.png&w=64&q=75", http.Header{"If-None-Match": {etag}})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("revisit: status %d body %d bytes", rec.Code, rec.Body.Len())
	}

	// Never upscale: w=64 on a 100px-wide source is fine, but a 16px request
	// upscaled is not a thing — request w=64 against a 16px-wide source.
	amplifySeedDeployment(t, "imgapp1", "main", "job-img-2", map[string]string{
		"deploy-manifest.json": amplifyImageOptManifest(""),
		"static/img/tiny.png":  string(amplifyTestPNG(t, 16, 16)),
	})
	rec = amplifyHostingGet(t, host, "/_next/image?url=%2Fimg%2Ftiny.png&w=64&q=75", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("upscale guard: status %d body %s", rec.Code, rec.Body.String())
	}
	decoded, _, err = image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode upscale-guard image: %v", err)
	}
	if decoded.Bounds().Dx() != 16 || decoded.Bounds().Dy() != 16 {
		t.Errorf("upscaled to %dx%d, want 16x16", decoded.Bounds().Dx(), decoded.Bounds().Dy())
	}

	// Webp negotiation via Accept (avif has no encoder, webp is next).
	amplifySeedDeployment(t, "imgapp1", "main", "job-img-3", map[string]string{
		"deploy-manifest.json": amplifyImageOptManifest(""),
		"static/img/photo.png": string(pngBytes),
	})
	rec = amplifyHostingGet(t, host, "/_next/image?url=%2Fimg%2Fphoto.png&w=64&q=75", http.Header{"Accept": {"image/avif,image/webp,*/*;q=0.8"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("negotiate: status %d body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/webp" {
		t.Errorf("negotiated content-type %q", got)
	}
	if _, format, err = image.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil || format != "webp" {
		t.Errorf("negotiated bytes decode as %q (%v), want webp", format, err)
	}

	// Disallowed width and quality.
	rec = amplifyHostingGet(t, host, "/_next/image?url=%2Fimg%2Fphoto.png&w=48&q=75", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"w" parameter (width) of 48 is not allowed`) {
		t.Errorf("width: status %d body %q", rec.Code, rec.Body.String())
	}
	rec = amplifyHostingGet(t, host, "/_next/image?url=%2Fimg%2Fphoto.png&w=64&q=0", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"q" parameter (quality) must be an integer between 1 and 100`) {
		t.Errorf("quality: status %d body %q", rec.Code, rec.Body.String())
	}

	// Disallowed remote domain — rejected before any fetch.
	rec = amplifyHostingGet(t, host, "/_next/image?url="+url.QueryEscape("https://evil.example.com/a.png")+"&w=64&q=75", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"url" parameter is not allowed`) {
		t.Errorf("remote: status %d body %q", rec.Code, rec.Body.String())
	}

	// SVG is rejected without dangerouslyAllowSVG…
	amplifySeedDeployment(t, "imgapp1", "main", "job-img-4", map[string]string{
		"deploy-manifest.json":  amplifyImageOptManifest(""),
		"static/img/vector.svg": "<svg xmlns='http://www.w3.org/2000/svg'></svg>",
	})
	rec = amplifyHostingGet(t, host, "/_next/image?url=%2Fimg%2Fvector.svg&w=64&q=75", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"url" parameter is valid but image type is not allowed`) {
		t.Errorf("svg blocked: status %d body %q", rec.Code, rec.Body.String())
	}

	// …and passed through unmodified with it.
	svgSource := "<svg xmlns='http://www.w3.org/2000/svg'><rect width='4' height='4'/></svg>"
	amplifySeedDeployment(t, "imgapp1", "main", "job-img-5", map[string]string{
		"deploy-manifest.json": strings.Replace(amplifyImageOptManifest(""),
			`"dangerouslyAllowSVG": false`, `"dangerouslyAllowSVG": true`, 1),
		"static/img/vector.svg": svgSource,
	})
	rec = amplifyHostingGet(t, host, "/_next/image?url=%2Fimg%2Fvector.svg&w=64&q=75", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != svgSource {
		t.Errorf("svg passthrough: status %d body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Errorf("svg content-type %q", got)
	}
}

func TestAmplifyImageSettingsTTLSpellings(t *testing.T) {
	// The deployment-specification document uses both spellings of the
	// cache-TTL key; both parse, the property-table spelling winning.
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{`{"sizes":[64],"minimumCacheTTL":120}`, 120},
		{`{"sizes":[64],"minumumCacheTTL":90}`, 90},
		{`{"sizes":[64],"minimumCacheTTL":120,"minumumCacheTTL":90}`, 120},
		{`{"sizes":[64]}`, 0},
	} {
		settings, err := amplifyParseImageSettings([]byte(tc.raw))
		if err != nil {
			t.Fatalf("parse %s: %v", tc.raw, err)
		}
		if got := settings.minimumCacheTTL(); got != tc.want {
			t.Errorf("%s: TTL %d, want %d", tc.raw, got, tc.want)
		}
	}
	if _, err := amplifyParseImageSettings(nil); err == nil {
		t.Error("missing imageSettings must be an error for ImageOptimization routes")
	}
}

func TestAmplifyCacheControlMaxAge(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   int
	}{
		{"", 0},
		{"public, max-age=600", 600},
		{`public, s-maxage=900, max-age=600`, 900},
		{`max-age="300"`, 300},
		{"no-store", 0},
	} {
		if got := amplifyCacheControlMaxAge(tc.header); got != tc.want {
			t.Errorf("maxAge(%q) = %d, want %d", tc.header, got, tc.want)
		}
	}
}

func TestAmplifyHostingImageOptimizationFallback(t *testing.T) {
	amplifyResetHostingState()
	amplifySeedApp("imgfall1", "main")
	amplifyApps.Update("imgfall1", func(a *amplifyStoredApp) { a.App.Platform = "WEB_COMPUTE" })
	manifest := `{
		"version": 1,
		"framework": {"name": "custom", "version": "1.0.0"},
		"routes": [
			{"path": "/_next/image", "target": {"kind": "ImageOptimization"}, "fallback": {"kind": "Static"}},
			{"path": "/*", "target": {"kind": "Static"}}
		],
		"imageSettings": {
			"sizes": [64],
			"domains": [],
			"remotePatterns": [],
			"formats": ["image/webp"],
			"minimumCacheTTL": 60,
			"dangerouslyAllowSVG": false
		}
	}`
	amplifySeedDeployment(t, "imgfall1", "main", "job-imgfall-1", map[string]string{
		"deploy-manifest.json": manifest,
		"static/_next/image":   "fallback static body",
	})
	host := "main.imgfall1.amplifyapp.com"

	// The optimizer's 404 (missing source artifact) falls back to the
	// route's declared fallback target.
	rec := amplifyHostingGet(t, host, "/_next/image?url=%2Fmissing.png&w=64&q=75", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "fallback static body" {
		t.Errorf("fallback: status %d body %q", rec.Code, rec.Body.String())
	}

	// Without a fallback the optimizer's own 404 error surface is served.
	amplifySeedDeployment(t, "imgfall1", "main", "job-imgfall-2", map[string]string{
		"deploy-manifest.json": strings.Replace(manifest, `, "fallback": {"kind": "Static"}`, "", 1),
		"static/_next/image":   "fallback static body",
	})
	rec = amplifyHostingGet(t, host, "/_next/image?url=%2Fmissing.png&w=64&q=75", nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"url" parameter is valid but internal response is invalid`) {
		t.Errorf("no fallback: status %d body %q", rec.Code, rec.Body.String())
	}
}
