package main

import (
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A reference under a pull-through cache rule names what the rule fetches, so
// the host runs `<upstream>/<path>`; Docker Hub's upstream is the engine's
// default registry. Other references resolve as the framework resolves them.
func ecrWorkloadImage(image string) string {
	idx := strings.Index(image, ".amazonaws.com/")
	if !strings.Contains(image, ".dkr.ecr.") || idx < 0 {
		return sim.ResolveLocalImage(image)
	}
	path := image[idx+len(".amazonaws.com/"):]
	prefix, rest, found := strings.Cut(path, "/")
	if !found {
		return sim.ResolveLocalImage(image)
	}
	rule, ok := ecrPullThroughCacheRules.Get(prefix)
	if !ok {
		return sim.ResolveLocalImage(image)
	}
	upstream := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(rule.UpstreamRegistryUrl, "https://"), "http://"), "/")
	switch upstream {
	case "registry-1.docker.io", "docker.io", "index.docker.io":
		return strings.TrimPrefix(rest, "library/")
	}
	return upstream + "/" + rest
}
