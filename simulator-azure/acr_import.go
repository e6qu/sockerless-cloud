package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Import as container-registry-import-images describes it: the image a source
// names, with every manifest an index lists (50 at most), is copied into the
// target repositories under each target tag, or by digest alone. A source in
// this simulator is read from its stores; any other is pulled over the
// Registry v2 API with the given credentials.

type acrImportImageParameters struct {
	Source struct {
		ResourceID  string `json:"resourceId"`
		RegistryURI string `json:"registryUri"`
		Credentials *struct {
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"credentials"`
		SourceImage string `json:"sourceImage"`
	} `json:"source"`
	TargetTags                 []string `json:"targetTags"`
	UntaggedTargetRepositories []string `json:"untaggedTargetRepositories"`
	Mode                       string   `json:"mode"`
}

const (
	acrImportMaxManifests = 50
	acrImportTimeout      = 10 * time.Minute
)

// acrSplitImage splits "repo:tag" or "repo@digest".
func acrSplitImage(image string) (repo, ref string) {
	if at := strings.Index(image, "@"); at >= 0 {
		return image[:at], image[at+1:]
	}
	if colon := strings.LastIndex(image, ":"); colon >= 0 && !strings.Contains(image[colon:], "/") {
		return image[:colon], image[colon+1:]
	}
	return image, "latest"
}

// acrRegistryEndpoint is the HTTPS base of a registry host; Docker Hub serves
// its registry API at registry-1.docker.io.
func acrRegistryEndpoint(host string) string {
	host = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://"), "/")
	if host == "docker.io" || host == "index.docker.io" {
		host = "registry-1.docker.io"
	}
	return "https://" + host
}

func acrImportImage(ctx context.Context, oci *sim.OCIRegistry, targetRegID string, req acrImportImageParameters) error {
	if req.Source.SourceImage == "" {
		return errors.New("source.sourceImage is required")
	}
	if req.Source.ResourceID == "" && req.Source.RegistryURI == "" {
		return errors.New("source.resourceId or source.registryUri is required")
	}
	repo, ref := acrSplitImage(req.Source.SourceImage)

	var pull sim.OCIPull
	var err error
	sourceRegID := ""
	switch {
	case req.Source.ResourceID != "":
		source, ok := acrRegistries.Get(req.Source.ResourceID)
		if !ok {
			for _, candidate := range acrRegistries.List() {
				if strings.EqualFold(candidate.ID, req.Source.ResourceID) {
					source, ok = candidate, true
				}
			}
		}
		if !ok {
			return fmt.Errorf("the source registry %s was not found", req.Source.ResourceID)
		}
		sourceRegID = source.ID
	default:
		if source, ok := acrRegistryForHost(req.Source.RegistryURI); ok {
			sourceRegID = source.ID
		}
	}
	if sourceRegID != "" {
		pull, err = oci.ExportImage(sourceRegID, repo, ref, acrImportMaxManifests)
	} else {
		user, pass := "", ""
		if c := req.Source.Credentials; c != nil {
			user, pass = c.Username, c.Password
		}
		pullCtx, cancel := context.WithTimeout(ctx, acrImportTimeout)
		defer cancel()
		pull, err = sim.PullOCIImage(pullCtx, &http.Client{Timeout: acrImportTimeout}, acrRegistryEndpoint(req.Source.RegistryURI), repo, ref, user, pass, acrImportMaxManifests)
	}
	if errors.Is(err, sim.ErrOCITooManyManifests) {
		return fmt.Errorf("the image %s has more than the %d manifests an import copies", req.Source.SourceImage, acrImportMaxManifests)
	}
	if err != nil {
		return fmt.Errorf("could not read %s from the source registry: %w", req.Source.SourceImage, err)
	}

	type target struct{ repo, tag string }
	var targets []target
	for _, t := range req.TargetTags {
		r, tag := acrSplitImage(t)
		targets = append(targets, target{r, tag})
	}
	for _, r := range req.UntaggedTargetRepositories {
		targets = append(targets, target{r, ""})
	}
	if len(targets) == 0 {
		tag := ref
		if strings.HasPrefix(ref, "sha256:") {
			tag = ""
		}
		targets = append(targets, target{repo, tag})
	}
	if !strings.EqualFold(req.Mode, "Force") {
		for _, t := range targets {
			if t.tag == "" {
				continue
			}
			if held, ok := oci.GetManifest(targetRegID, t.repo, t.tag); ok && held.Digest != pull.Root.Digest {
				return fmt.Errorf("the target tag %s:%s already exists; import with mode Force to overwrite it", t.repo, t.tag)
			}
		}
	}
	for _, t := range targets {
		var tags []string
		if t.tag != "" {
			tags = []string{t.tag}
		}
		oci.ImportImage(targetRegID, t.repo, pull, tags)
	}
	return nil
}
