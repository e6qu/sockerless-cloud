package aws_sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/amplify"
	amplifytypes "github.com/aws/aws-sdk-go-v2/service/amplify/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "golang.org/x/image/webp"
)

// Amplify Hosting image optimization e2e: a deployed WEB_COMPUTE bundle
// whose manifest routes `/_next/image` to the ImageOptimization primitive,
// exercised through plain hosted HTTP requests exactly as a browser issues
// them.

func amplifyImageOptPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 7), G: uint8(y * 11), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func amplifyImageOptJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(y * 5), G: 200, B: uint8(x * 3), A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}))
	return buf.Bytes()
}

func amplifyImageOptManifestJSON(t *testing.T, imageOptCacheControl string, dangerouslyAllowSVG bool) []byte {
	t.Helper()
	imageOptTarget := map[string]string{"kind": "ImageOptimization"}
	if imageOptCacheControl != "" {
		imageOptTarget["cacheControl"] = imageOptCacheControl
	}
	manifest, err := json.Marshal(map[string]any{
		"version":   1,
		"framework": map[string]string{"name": "custom", "version": "1.0.0"},
		"routes": []map[string]any{
			{"path": "/_next/image", "target": imageOptTarget},
			{"path": "/*.*", "target": map[string]string{"kind": "Static"}},
			{"path": "/*", "target": map[string]string{"kind": "Compute", "src": "default"}},
		},
		"computeResources": []map[string]string{
			{"name": "default", "runtime": "nodejs20.x", "entrypoint": "index.js"},
		},
		"imageSettings": map[string]any{
			"sizes":          []int{64, 128},
			"domains":        []string{"127.0.0.1"},
			"remotePatterns": []any{},
			"formats":        []string{"image/avif", "image/webp"},
			// The floor for the emitted Cache-Control max-age.
			"minimumCacheTTL":     123,
			"dangerouslyAllowSVG": dangerouslyAllowSVG,
		},
	})
	require.NoError(t, err)
	return manifest
}

func TestAmplifyHostingImageOptimizationE2E(t *testing.T) {
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// A real remote origin for the absolute-URL path, with an upstream
	// Cache-Control the optimizer must honor over the minimumCacheTTL floor.
	remoteJPEG := amplifyImageOptJPEG(t, 150, 90)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/photos/team.jpg" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=600")
		_, _ = w.Write(remoteJPEG)
	}))
	defer remote.Close()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name:     aws.String("imgopt-app-" + time.Now().Format("150405.000000")),
		Platform: amplifytypes.PlatformWebCompute,
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() { _, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)}) }()
	_, err = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)
	host := "main." + appID + ".amplifyapp.com"

	sourcePNG := amplifyImageOptPNG(t, 200, 120)
	entrypoint := []byte("const http = require('http');\nhttp.createServer((req, res) => res.end('ok')).listen(process.env.PORT);\n")
	amplifyDeployZip(t, ctx, c, appID, "main", amplifyZipBytes(t, map[string][]byte{
		"deploy-manifest.json":     amplifyImageOptManifestJSON(t, "", false),
		"compute/default/index.js": entrypoint,
		"static/img/photo.png":     sourcePNG,
		"static/img/vector.svg":    []byte("<svg xmlns='http://www.w3.org/2000/svg'></svg>"),
	}))

	imageOptGet := func(query string, mutate func(*http.Request)) (*http.Response, []byte) {
		return amplifyHostGet(t, host, "/_next/image?"+query, mutate)
	}

	// (a) Resize a real PNG artifact to an allowed width: no Accept header
	// keeps the source format; dimensions and format verified by decoding
	// the actual response bytes.
	resp, body := imageOptGet("url=%2Fimg%2Fphoto.png&w=64&q=75", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "resize: %s", body)
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
	decoded, format, err := image.Decode(bytes.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, "png", format)
	assert.Equal(t, 64, decoded.Bounds().Dx())
	assert.Equal(t, 38, decoded.Bounds().Dy(), "aspect ratio preserved for 200x120 at w=64")
	// (f) Cache-Control floor from minimumCacheTTL.
	assert.Equal(t, "public, max-age=123, must-revalidate", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "Accept", resp.Header.Get("Vary"))
	etag := resp.Header.Get("ETag")
	require.NotEmpty(t, etag)

	// (e) ETag revisit answers 304 with no body.
	resp, body = imageOptGet("url=%2Fimg%2Fphoto.png&w=64&q=75", func(r *http.Request) {
		r.Header.Set("If-None-Match", etag)
	})
	assert.Equal(t, http.StatusNotModified, resp.StatusCode)
	assert.Empty(t, body)

	// (d) Format negotiation via Accept honoring imageSettings.formats:
	// image/avif has no pure-Go encoder so image/webp is the negotiated
	// output, and the Content-Type matches the real bytes.
	resp, body = imageOptGet("url=%2Fimg%2Fphoto.png&w=128&q=75", func(r *http.Request) {
		r.Header.Set("Accept", "image/avif,image/webp,*/*;q=0.8")
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, "negotiate: %s", body)
	assert.Equal(t, "image/webp", resp.Header.Get("Content-Type"))
	decoded, format, err = image.Decode(bytes.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, "webp", format, "Content-Type must match the actual encoded bytes")
	assert.Equal(t, 128, decoded.Bounds().Dx())

	// (b) Disallowed width.
	resp, body = imageOptGet("url=%2Fimg%2Fphoto.png&w=100&q=75", nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, string(body), `"w" parameter (width) of 100 is not allowed`)

	// (c) Disallowed remote domain — rejected without fetching.
	resp, body = imageOptGet("url="+url.QueryEscape("https://evil.example.com/x.png")+"&w=64&q=75", nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, string(body), `"url" parameter is not allowed`)

	// An allowed remote source is really fetched, resized, and its upstream
	// Cache-Control max-age (600) wins over the smaller minimumCacheTTL.
	resp, body = imageOptGet("url="+url.QueryEscape(remote.URL+"/photos/team.jpg")+"&w=64&q=75", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "remote fetch: %s", body)
	assert.Equal(t, "image/jpeg", resp.Header.Get("Content-Type"))
	decoded, format, err = image.Decode(bytes.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, "jpeg", format)
	assert.Equal(t, 64, decoded.Bounds().Dx())
	assert.Equal(t, 38, decoded.Bounds().Dy(), "aspect ratio preserved for 150x90 at w=64")
	assert.Equal(t, "public, max-age=600, must-revalidate", resp.Header.Get("Cache-Control"))

	// (g) SVG is rejected while dangerouslyAllowSVG is false.
	resp, body = imageOptGet("url=%2Fimg%2Fvector.svg&w=64&q=75", nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, string(body), `"url" parameter is valid but image type is not allowed`)

	// A new deployment allowing SVG passes it through unmodified, and the
	// route target's cacheControl governs the 200 response.
	svgSource := "<svg xmlns='http://www.w3.org/2000/svg'><rect width='4' height='4'/></svg>"
	amplifyDeployZip(t, ctx, c, appID, "main", amplifyZipBytes(t, map[string][]byte{
		"deploy-manifest.json":     amplifyImageOptManifestJSON(t, "public, max-age=3600, immutable", true),
		"compute/default/index.js": entrypoint,
		"static/img/vector.svg":    []byte(svgSource),
	}))
	resp, body = imageOptGet("url=%2Fimg%2Fvector.svg&w=64&q=75", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "svg passthrough: %s", body)
	assert.Equal(t, "image/svg+xml", resp.Header.Get("Content-Type"))
	assert.Equal(t, svgSource, string(body))
	assert.Equal(t, "public, max-age=3600, immutable", resp.Header.Get("Cache-Control"))

	// Missing url parameter carries the optimizer's exact error string.
	resp, body = imageOptGet("w=64&q=75", nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, string(body), `"url" parameter is required`)

	require.False(t, strings.Contains(resp.Header.Get("Content-Type"), "image/"), "error responses are not images")
}
