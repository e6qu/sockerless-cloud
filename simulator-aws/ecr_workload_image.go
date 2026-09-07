package main

import (
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ecrWorkloadImage is the image a workload host runs for an Amazon ECR
// reference. A reference under a registered pull-through cache rule —
// `<account>.dkr.ecr.<region>.amazonaws.com/<prefix>/<path>` — names what
// the rule fetches from its upstream on the first pull, so the host runs
// `<upstream>/<path>`; Docker Hub's upstream is the engine's default
// registry. Any other reference resolves as the framework resolves it.
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
