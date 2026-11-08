package sim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// OCIPulledManifest is one manifest a pull fetched, as the source served it.
type OCIPulledManifest struct {
	Digest      string
	ContentType string
	Data        []byte
}

// OCIPulledBlob is one blob a pull fetched.
type OCIPulledBlob struct {
	Digest      string
	ContentType string
	Data        []byte
}

// OCIPull is an image as a registry serves it: the manifest the reference
// names, any manifests an index lists, and every blob they reference.
type OCIPull struct {
	Root      OCIPulledManifest
	Manifests []OCIPulledManifest
	Blobs     []OCIPulledBlob
}

// ErrOCITooManyManifests is the pull of an image with more manifests than the
// caller allows.
var ErrOCITooManyManifests = errors.New("the image has more manifests than allowed")

var ociAcceptManifests = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// PullOCIImage fetches repo:reference from the registry at baseURL over the
// Registry v2 API, answering a bearer challenge with the credentials when
// given, and keeps every byte as served.
func PullOCIImage(ctx context.Context, client *http.Client, baseURL, repo, reference, username, password string, maxManifests int) (OCIPull, error) {
	p := &ociPuller{ctx: ctx, client: client, base: strings.TrimSuffix(baseURL, "/"), repo: repo, user: username, pass: password}
	root, err := p.manifest(reference)
	if err != nil {
		return OCIPull{}, err
	}
	out := OCIPull{Root: root}
	pending := []OCIPulledManifest{root}
	seenBlob := map[string]bool{}
	for len(pending) > 0 {
		m := pending[0]
		pending = pending[1:]
		out.Manifests = append(out.Manifests, m)
		if maxManifests > 0 && len(out.Manifests) > maxManifests {
			return OCIPull{}, ErrOCITooManyManifests
		}
		var doc struct {
			Manifests []struct {
				Digest string `json:"digest"`
			} `json:"manifests"`
			Config *struct {
				Digest    string `json:"digest"`
				MediaType string `json:"mediaType"`
			} `json:"config"`
			Layers []struct {
				Digest    string `json:"digest"`
				MediaType string `json:"mediaType"`
			} `json:"layers"`
		}
		if err := json.Unmarshal(m.Data, &doc); err != nil {
			return OCIPull{}, fmt.Errorf("decode manifest %s: %w", m.Digest, err)
		}
		for _, child := range doc.Manifests {
			cm, err := p.manifest(child.Digest)
			if err != nil {
				return OCIPull{}, err
			}
			pending = append(pending, cm)
		}
		var blobs []struct{ digest, mediaType string }
		if doc.Config != nil && doc.Config.Digest != "" {
			blobs = append(blobs, struct{ digest, mediaType string }{doc.Config.Digest, doc.Config.MediaType})
		}
		for _, l := range doc.Layers {
			blobs = append(blobs, struct{ digest, mediaType string }{l.Digest, l.MediaType})
		}
		for _, b := range blobs {
			if seenBlob[b.digest] {
				continue
			}
			seenBlob[b.digest] = true
			data, err := p.get("/blobs/"+b.digest, "")
			if err != nil {
				return OCIPull{}, err
			}
			if got := ociDigest(data.body); got != b.digest {
				return OCIPull{}, fmt.Errorf("blob %s arrived with digest %s", b.digest, got)
			}
			out.Blobs = append(out.Blobs, OCIPulledBlob{Digest: b.digest, ContentType: b.mediaType, Data: data.body})
		}
	}
	return out, nil
}

type ociPuller struct {
	ctx        context.Context
	client     *http.Client
	base, repo string
	user, pass string
	token      string
}

type ociResponse struct {
	body        []byte
	contentType string
}

func (p *ociPuller) manifest(reference string) (OCIPulledManifest, error) {
	resp, err := p.get("/manifests/"+reference, ociAcceptManifests)
	if err != nil {
		return OCIPulledManifest{}, err
	}
	digest := ociDigest(resp.body)
	if strings.HasPrefix(reference, "sha256:") && digest != reference {
		return OCIPulledManifest{}, fmt.Errorf("manifest %s arrived with digest %s", reference, digest)
	}
	return OCIPulledManifest{Digest: digest, ContentType: resp.contentType, Data: resp.body}, nil
}

func (p *ociPuller) get(path, accept string) (ociResponse, error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(p.ctx, http.MethodGet, p.base+"/v2/"+p.repo+path, nil)
		if err != nil {
			return ociResponse{}, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		switch {
		case p.token != "":
			req.Header.Set("Authorization", "Bearer "+p.token)
		case p.user != "":
			req.SetBasicAuth(p.user, p.pass)
		}
		resp, err := p.client.Do(req)
		if err != nil {
			return ociResponse{}, err
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return ociResponse{}, readErr
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && p.token == "" {
			if err := p.authenticate(resp.Header.Get("Www-Authenticate")); err != nil {
				return ociResponse{}, err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return ociResponse{}, fmt.Errorf("GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
		}
		return ociResponse{body: body, contentType: resp.Header.Get("Content-Type")}, nil
	}
	return ociResponse{}, fmt.Errorf("GET %s: the registry refused the credentials", path)
}

// authenticate answers a Bearer challenge at its realm; a Basic challenge
// leaves the credentials on each request.
func (p *ociPuller) authenticate(challenge string) error {
	scheme, params, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		if p.user == "" {
			return fmt.Errorf("the registry asks for %s credentials and none were given", scheme)
		}
		return nil
	}
	fields := map[string]string{}
	for _, part := range strings.Split(params, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			fields[strings.ToLower(k)] = strings.Trim(v, `"`)
		}
	}
	realm := fields["realm"]
	if realm == "" {
		return errors.New("the registry's bearer challenge names no realm")
	}
	q := url.Values{}
	if s := fields["service"]; s != "" {
		q.Set("service", s)
	}
	scope := fields["scope"]
	if scope == "" {
		scope = "repository:" + p.repo + ":pull"
	}
	q.Set("scope", scope)
	req, err := http.NewRequestWithContext(p.ctx, http.MethodGet, realm+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if p.user != "" {
		req.SetBasicAuth(p.user, p.pass)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("token request: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return fmt.Errorf("decode the token response: %w", err)
	}
	p.token = tok.Token
	if p.token == "" {
		p.token = tok.AccessToken
	}
	if p.token == "" {
		return errors.New("the token response carries no token")
	}
	return nil
}

// ExportImage reads repo:reference out of the registry as a pull would fetch
// it, from the stores directly.
func (reg *OCIRegistry) ExportImage(scope, repo, reference string, maxManifests int) (OCIPull, error) {
	read := func(ref string) (OCIPulledManifest, error) {
		m, ok := reg.Manifests.Get(ociManifestKey(scope, repo, ref))
		if !ok {
			return OCIPulledManifest{}, fmt.Errorf("manifest %s:%s is not in the registry", repo, ref)
		}
		return OCIPulledManifest{Digest: m.Digest, ContentType: m.ContentType, Data: m.Data}, nil
	}
	root, err := read(reference)
	if err != nil {
		return OCIPull{}, err
	}
	out := OCIPull{Root: root}
	pending := []OCIPulledManifest{root}
	seenBlob := map[string]bool{}
	for len(pending) > 0 {
		m := pending[0]
		pending = pending[1:]
		out.Manifests = append(out.Manifests, m)
		if maxManifests > 0 && len(out.Manifests) > maxManifests {
			return OCIPull{}, ErrOCITooManyManifests
		}
		var doc struct {
			Manifests []struct {
				Digest string `json:"digest"`
			} `json:"manifests"`
			Config *struct {
				Digest string `json:"digest"`
			} `json:"config"`
			Layers []struct {
				Digest string `json:"digest"`
			} `json:"layers"`
		}
		if err := json.Unmarshal(m.Data, &doc); err != nil {
			return OCIPull{}, fmt.Errorf("decode manifest %s: %w", m.Digest, err)
		}
		for _, child := range doc.Manifests {
			cm, err := read(child.Digest)
			if err != nil {
				return OCIPull{}, err
			}
			pending = append(pending, cm)
		}
		digests := []string{}
		if doc.Config != nil && doc.Config.Digest != "" {
			digests = append(digests, doc.Config.Digest)
		}
		for _, l := range doc.Layers {
			digests = append(digests, l.Digest)
		}
		for _, d := range digests {
			if seenBlob[d] {
				continue
			}
			seenBlob[d] = true
			b, ok := reg.Blobs.Get(ociBlobKey(scope, repo, d))
			if !ok {
				return OCIPull{}, fmt.Errorf("blob %s of %s is not in the registry", d, repo)
			}
			out.Blobs = append(out.Blobs, OCIPulledBlob{Digest: d, ContentType: b.ContentType, Data: b.Data})
		}
	}
	return out, nil
}

// ImportImage stores a pulled image in repo under each tag, or by digest
// alone when no tag is given, as a push of the same bytes would.
func (reg *OCIRegistry) ImportImage(scope, repo string, pull OCIPull, tags []string) {
	for _, b := range pull.Blobs {
		reg.PutBlob(scope, repo, b.Digest, b.ContentType, b.Data)
	}
	for _, m := range pull.Manifests {
		reg.putManifestRaw(nil, scope, repo, m.Digest, m.ContentType, m.Data)
	}
	for _, tag := range tags {
		reg.putManifestRaw(nil, scope, repo, tag, pull.Root.ContentType, pull.Root.Data)
	}
}
