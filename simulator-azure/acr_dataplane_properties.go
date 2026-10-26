package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Azure Container Registry data plane's properties APIs. Every figure is
// read from what the registry holds; the only state they add is the four
// changeable attributes, which the registry enforces.
//
// Each method mounts once on a greedy path: `/acr/v1/<repo>/_tags/<tag>` and
// `/acr/v1/<repo>` differ only by a suffix a repository name may contain.

// acrAttributes all default to true, what a freshly pushed artifact permits.
type acrAttributes struct {
	Key           string `json:"key"`
	DeleteEnabled *bool  `json:"deleteEnabled,omitempty"`
	WriteEnabled  *bool  `json:"writeEnabled,omitempty"`
	ListEnabled   *bool  `json:"listEnabled,omitempty"`
	ReadEnabled   *bool  `json:"readEnabled,omitempty"`
}

var acrChangeableAttributes sim.Store[acrAttributes]

type acrWriteableAttributes struct {
	DeleteEnabled *bool `json:"deleteEnabled"`
	WriteEnabled  *bool `json:"writeEnabled"`
	ListEnabled   *bool `json:"listEnabled"`
	ReadEnabled   *bool `json:"readEnabled"`
}

func registerACRDataPlaneProperties(srv *sim.Server, reg *sim.OCIRegistry) {
	acrChangeableAttributes = sim.MakeStore[acrAttributes](srv.DB(), "acr_changeable_attributes")

	srv.HandleFunc("PATCH /acr/v1/{path...}", func(w http.ResponseWriter, r *http.Request) {
		acrDispatchProperties(w, r, reg, http.MethodPatch)
	})
	srv.HandleFunc("DELETE /acr/v1/{path...}", func(w http.ResponseWriter, r *http.Request) {
		acrDispatchProperties(w, r, reg, http.MethodDelete)
	})
}

type acrPropertiesTarget struct {
	repo   string
	kind   string // "repository", "manifests", "manifest", "tag"
	digest string
	tag    string
}

// acrParsePropertiesPath looks for the suffix from the right: a repository
// legitimately named "a/_tags" would otherwise swallow the tag.
func acrParsePropertiesPath(path string) (acrPropertiesTarget, bool) {
	path = strings.Trim(path, "/")
	if path == "" || path == "_catalog" {
		return acrPropertiesTarget{}, false
	}
	if repo, tag, ok := acrSplitOnLast(path, "/_tags/"); ok {
		return acrPropertiesTarget{repo: repo, kind: "tag", tag: tag}, repo != "" && tag != ""
	}
	if repo, digest, ok := acrSplitOnLast(path, "/_manifests/"); ok {
		return acrPropertiesTarget{repo: repo, kind: "manifest", digest: digest}, repo != "" && digest != ""
	}
	if repo, ok := strings.CutSuffix(path, "/_manifests"); ok {
		return acrPropertiesTarget{repo: repo, kind: "manifests"}, repo != ""
	}
	if repo, ok := strings.CutSuffix(path, "/_tags"); ok {
		return acrPropertiesTarget{repo: repo, kind: "tags"}, repo != ""
	}
	return acrPropertiesTarget{repo: path, kind: "repository"}, true
}

func acrSplitOnLast(path, sep string) (string, string, bool) {
	i := strings.LastIndex(path, sep)
	if i < 0 {
		return "", "", false
	}
	return path[:i], path[i+len(sep):], true
}

func acrDispatchProperties(w http.ResponseWriter, r *http.Request, reg *sim.OCIRegistry, method string) {
	target, ok := acrParsePropertiesPath(sim.PathParam(r, "path"))
	if !ok {
		acrPropertiesNotFound(w, "the path does not address a repository, manifest or tag")
		return
	}
	switch {
	case method == http.MethodPatch && target.kind == "repository":
		acrUpdateRepositoryProperties(w, r, reg, target.repo)
	case method == http.MethodDelete && target.kind == "repository":
		acrDeleteRepository(w, r, reg, target.repo)
	case method == http.MethodPatch && target.kind == "manifest":
		acrUpdateManifestProperties(w, r, reg, target.repo, target.digest)
	case method == http.MethodPatch && target.kind == "tag":
		acrUpdateTagAttributes(w, r, reg, target.repo, target.tag)
	case method == http.MethodDelete && target.kind == "tag":
		acrDeleteTag(w, r, reg, target.repo, target.tag)
	default:
		acrPropertiesNotFound(w, "the path does not address anything this method can be used on")
	}
}

// acrPropertiesNotFound answers in the registry's own error shape, which its
// clients parse.
func acrPropertiesNotFound(w http.ResponseWriter, detail string) {
	sim.WriteJSON(w, http.StatusNotFound, map[string]any{
		"errors": []any{map[string]any{
			"code":    "NOT_FOUND",
			"message": detail,
		}},
	})
}

func acrAttributeKey(scope, repo, suffix string) string {
	if suffix == "" {
		return scope + "|" + repo
	}
	return scope + "|" + repo + "|" + suffix
}

func acrAttributesFor(key string) acrWriteableAttributes {
	yes := true
	held, ok := acrChangeableAttributes.Get(key)
	if !ok {
		return acrWriteableAttributes{DeleteEnabled: &yes, WriteEnabled: &yes, ListEnabled: &yes, ReadEnabled: &yes}
	}
	set := acrWriteableAttributes{
		DeleteEnabled: held.DeleteEnabled, WriteEnabled: held.WriteEnabled,
		ListEnabled: held.ListEnabled, ReadEnabled: held.ReadEnabled,
	}
	for _, member := range []**bool{&set.DeleteEnabled, &set.WriteEnabled, &set.ListEnabled, &set.ReadEnabled} {
		if *member == nil {
			enabled := true
			*member = &enabled
		}
	}
	return set
}

// acrApplyAttributes keeps the value of every member the request leaves out, as
// a PATCH means.
func acrApplyAttributes(w http.ResponseWriter, r *http.Request, key string) (acrWriteableAttributes, bool) {
	var body acrWriteableAttributes
	if err := azureReadRequiredJSON(r, &body); err != nil {
		sim.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"errors": []any{map[string]any{
				"code": "INVALID_REQUEST", "message": "invalid request body: " + err.Error(),
			}},
		})
		return acrWriteableAttributes{}, false
	}
	merged := acrAttributesFor(key)
	if body.DeleteEnabled != nil {
		merged.DeleteEnabled = body.DeleteEnabled
	}
	if body.WriteEnabled != nil {
		merged.WriteEnabled = body.WriteEnabled
	}
	if body.ListEnabled != nil {
		merged.ListEnabled = body.ListEnabled
	}
	if body.ReadEnabled != nil {
		merged.ReadEnabled = body.ReadEnabled
	}
	acrChangeableAttributes.Put(key, acrAttributes{
		Key: key, DeleteEnabled: merged.DeleteEnabled, WriteEnabled: merged.WriteEnabled,
		ListEnabled: merged.ListEnabled, ReadEnabled: merged.ReadEnabled,
	})
	return merged, true
}

func acrAttributesDoc(set acrWriteableAttributes) map[string]any {
	return map[string]any{
		"deleteEnabled": *set.DeleteEnabled,
		"writeEnabled":  *set.WriteEnabled,
		"listEnabled":   *set.ListEnabled,
		"readEnabled":   *set.ReadEnabled,
	}
}

// acrRepositoryManifests returns the digest-keyed rows, without the tag aliases.
func acrRepositoryManifests(reg *sim.OCIRegistry, scope, repo string) []sim.OCIManifest {
	held := reg.Manifests.Filter(func(m sim.OCIManifest) bool {
		return m.Scope == scope && m.Repo == repo && m.Ref == m.Digest
	})
	sort.Slice(held, func(i, j int) bool { return held[i].Digest < held[j].Digest })
	return held
}

func acrRepositoryTags(reg *sim.OCIRegistry, scope, repo string) []sim.OCIManifest {
	held := reg.Manifests.Filter(func(m sim.OCIManifest) bool {
		return m.Scope == scope && m.Repo == repo && m.Ref != "" && m.Ref != m.Digest &&
			!strings.HasPrefix(m.Ref, "sha256:")
	})
	sort.Slice(held, func(i, j int) bool { return held[i].Ref < held[j].Ref })
	return held
}

// acrStamp reports the zero instant for a manifest stored before the registry
// recorded push times, rather than inventing one.
func acrStamp(at time.Time) string {
	return at.UTC().Format(time.RFC3339Nano)
}

func acrRepositoryExists(reg *sim.OCIRegistry, scope, repo string) bool {
	return len(reg.Manifests.Filter(func(m sim.OCIManifest) bool {
		return m.Scope == scope && m.Repo == repo
	})) > 0
}

func acrRepositoryPropertiesDoc(reg *sim.OCIRegistry, scope, registry, repo string) map[string]any {
	manifests := acrRepositoryManifests(reg, scope, repo)
	tags := acrRepositoryTags(reg, scope, repo)

	// Read both times from the manifests held, so they cannot drift.
	var earliest, latest time.Time
	for _, m := range append(append([]sim.OCIManifest{}, manifests...), tags...) {
		if m.Pushed.IsZero() {
			continue
		}
		if earliest.IsZero() || m.Pushed.Before(earliest) {
			earliest = m.Pushed
		}
		if m.Pushed.After(latest) {
			latest = m.Pushed
		}
	}
	return map[string]any{
		"registry":             registry,
		"imageName":            repo,
		"createdTime":          acrStamp(earliest),
		"lastUpdateTime":       acrStamp(latest),
		"manifestCount":        len(manifests),
		"tagCount":             len(tags),
		"changeableAttributes": acrAttributesDoc(acrAttributesFor(acrAttributeKey(scope, repo, ""))),
	}
}

func acrManifestPropertiesDoc(reg *sim.OCIRegistry, scope, registry, repo string, m sim.OCIManifest) map[string]any {
	var parsed struct {
		Config struct {
			MediaType string `json:"mediaType"`
		} `json:"config"`
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	_ = json.Unmarshal(m.Data, &parsed)

	tags := []any{}
	for _, tag := range acrRepositoryTags(reg, scope, repo) {
		if tag.Digest == m.Digest {
			tags = append(tags, tag.Ref)
		}
	}

	// An index names the platforms it covers; a single-platform manifest points at
	// a config blob that does.
	references := []any{}
	architecture, operatingSystem := "", ""
	for _, child := range parsed.Manifests {
		references = append(references, map[string]any{
			"digest":       child.Digest,
			"architecture": child.Platform.Architecture,
			"os":           child.Platform.OS,
		})
		if architecture == "" {
			architecture, operatingSystem = child.Platform.Architecture, child.Platform.OS
		}
	}
	if architecture == "" {
		if platform, ok := acrManifestPlatform(reg, scope, repo, m); ok {
			architecture, operatingSystem = platform[0], platform[1]
		}
	}

	manifest := map[string]any{
		"digest":               m.Digest,
		"imageSize":            len(m.Data),
		"createdTime":          acrStamp(m.Pushed),
		"lastUpdateTime":       acrStamp(m.Pushed),
		"configMediaType":      parsed.Config.MediaType,
		"references":           references,
		"tags":                 tags,
		"changeableAttributes": acrAttributesDoc(acrAttributesFor(acrAttributeKey(scope, repo, m.Digest))),
	}
	if architecture != "" {
		manifest["architecture"] = architecture
	}
	if operatingSystem != "" {
		manifest["os"] = operatingSystem
	}
	return map[string]any{"registry": registry, "imageName": repo, "manifest": manifest}
}

// acrManifestPlatform reads the architecture and OS from the image config blob
// the manifest points at.
func acrManifestPlatform(reg *sim.OCIRegistry, scope, repo string, m sim.OCIManifest) ([2]string, bool) {
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(m.Data, &manifest); err != nil || manifest.Config.Digest == "" {
		return [2]string{}, false
	}
	blob, ok := reg.GetBlob(scope, repo, manifest.Config.Digest)
	if !ok {
		return [2]string{}, false
	}
	var config struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	}
	if err := json.Unmarshal(blob, &config); err != nil || config.Architecture == "" {
		return [2]string{}, false
	}
	return [2]string{config.Architecture, config.OS}, true
}

func acrTagPropertiesDoc(scope, registry, repo string, m sim.OCIManifest) map[string]any {
	return map[string]any{
		"registry":  registry,
		"imageName": repo,
		"tag": map[string]any{
			"name":                 m.Ref,
			"digest":               m.Digest,
			"createdTime":          acrStamp(m.Pushed),
			"lastUpdateTime":       acrStamp(m.Pushed),
			"signed":               false,
			"changeableAttributes": acrAttributesDoc(acrAttributesFor(acrAttributeKey(scope, repo, ":"+m.Ref))),
		},
	}
}

// acrUpdateRepositoryProperties is ContainerRegistry_UpdateProperties.
func acrUpdateRepositoryProperties(w http.ResponseWriter, r *http.Request, reg *sim.OCIRegistry, repo string) {
	if !acrAuthorize(w, r, acrRepositoryResource(repo, acrActionMetadataWrite, acrActionMetadataWrite)) {
		return
	}
	scope := acrDataPlaneScope(r)
	if !acrRepositoryExists(reg, scope, repo) {
		acrPropertiesNotFound(w, "the repository "+repo+" is not in this registry")
		return
	}
	if _, ok := acrApplyAttributes(w, r, acrAttributeKey(scope, repo, "")); !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, acrRepositoryPropertiesDoc(reg, scope, acrRegistryName(r), repo))
}

// acrDeleteRepository is ContainerRegistry_DeleteRepository.
func acrDeleteRepository(w http.ResponseWriter, r *http.Request, reg *sim.OCIRegistry, repo string) {
	if !acrAuthorize(w, r, acrRepositoryResource(repo, acrActionDelete, acrActionDelete)) {
		return
	}
	scope := acrDataPlaneScope(r)
	if !acrRepositoryExists(reg, scope, repo) {
		acrPropertiesNotFound(w, "the repository "+repo+" is not in this registry")
		return
	}
	if enabled := acrAttributesFor(acrAttributeKey(scope, repo, "")).DeleteEnabled; enabled != nil && !*enabled {
		acrPropertiesDenied(w, "the repository "+repo+" has deletion disabled")
		return
	}
	for _, held := range reg.Manifests.Filter(func(m sim.OCIManifest) bool {
		return m.Scope == scope && m.Repo == repo
	}) {
		reg.DeleteManifest(scope, repo, held.Ref)
	}
	acrChangeableAttributes.Delete(acrAttributeKey(scope, repo, ""))
	w.WriteHeader(http.StatusAccepted)
}

// acrUpdateManifestProperties is ContainerRegistry_UpdateManifestProperties.
func acrUpdateManifestProperties(w http.ResponseWriter, r *http.Request, reg *sim.OCIRegistry, repo, digest string) {
	if !acrAuthorize(w, r, acrRepositoryResource(repo, acrActionMetadataWrite, acrActionMetadataWrite)) {
		return
	}
	scope := acrDataPlaneScope(r)
	held, ok := acrFindManifest(reg, scope, repo, digest)
	if !ok {
		acrPropertiesNotFound(w, "the manifest "+digest+" is not in repository "+repo)
		return
	}
	if _, ok := acrApplyAttributes(w, r, acrAttributeKey(scope, repo, digest)); !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, acrManifestPropertiesDoc(reg, scope, acrRegistryName(r), repo, held))
}

// acrUpdateTagAttributes is ContainerRegistry_UpdateTagAttributes.
func acrUpdateTagAttributes(w http.ResponseWriter, r *http.Request, reg *sim.OCIRegistry, repo, tag string) {
	if !acrAuthorize(w, r, acrRepositoryResource(repo, acrActionMetadataWrite, acrActionMetadataWrite)) {
		return
	}
	scope := acrDataPlaneScope(r)
	held, ok := acrFindTag(reg, scope, repo, tag)
	if !ok {
		acrPropertiesNotFound(w, "the tag "+tag+" is not in repository "+repo)
		return
	}
	if _, ok := acrApplyAttributes(w, r, acrAttributeKey(scope, repo, ":"+tag)); !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, acrTagPropertiesDoc(scope, acrRegistryName(r), repo, held))
}

// acrDeleteTag is ContainerRegistry_DeleteTag. It removes the name, not the
// manifest, which stays addressable by digest.
func acrDeleteTag(w http.ResponseWriter, r *http.Request, reg *sim.OCIRegistry, repo, tag string) {
	if !acrAuthorize(w, r, acrRepositoryResource(repo, acrActionDelete, acrActionDelete)) {
		return
	}
	scope := acrDataPlaneScope(r)
	if _, ok := acrFindTag(reg, scope, repo, tag); !ok {
		acrPropertiesNotFound(w, "the tag "+tag+" is not in repository "+repo)
		return
	}
	if enabled := acrAttributesFor(acrAttributeKey(scope, repo, ":"+tag)).DeleteEnabled; enabled != nil && !*enabled {
		acrPropertiesDenied(w, "the tag "+tag+" has deletion disabled")
		return
	}
	reg.DeleteTagOnly(scope, repo, tag)
	acrChangeableAttributes.Delete(acrAttributeKey(scope, repo, ":"+tag))
	w.WriteHeader(http.StatusAccepted)
}

func acrPropertiesDenied(w http.ResponseWriter, detail string) {
	sim.WriteJSON(w, http.StatusMethodNotAllowed, map[string]any{
		"errors": []any{map[string]any{"code": "DENIED", "message": detail}},
	})
}

func acrRegistryName(r *http.Request) string {
	if reg, ok := acrRegistryForHost(r.Host); ok {
		return reg.Properties.LoginServer
	}
	return ""
}

func acrFindManifest(reg *sim.OCIRegistry, scope, repo, digest string) (sim.OCIManifest, bool) {
	for _, held := range acrRepositoryManifests(reg, scope, repo) {
		if strings.EqualFold(held.Digest, digest) {
			return held, true
		}
	}
	return sim.OCIManifest{}, false
}

func acrFindTag(reg *sim.OCIRegistry, scope, repo, tag string) (sim.OCIManifest, bool) {
	for _, held := range acrRepositoryTags(reg, scope, repo) {
		if held.Ref == tag {
			return held, true
		}
	}
	return sim.OCIManifest{}, false
}

// acrReadProperties answers the three properties reads and the manifest list,
// which share the /acr/v1 GET path with the tag list.
func acrReadProperties(w http.ResponseWriter, r *http.Request, reg *sim.OCIRegistry, target acrPropertiesTarget) {
	if !acrAuthorize(w, r, acrRepositoryResource(target.repo, acrActionMetadataRead, acrActionMetadataRead)) {
		return
	}
	scope := acrDataPlaneScope(r)
	registry := acrRegistryName(r)
	switch target.kind {
	case "repository":
		if !acrRepositoryExists(reg, scope, target.repo) {
			acrPropertiesNotFound(w, "the repository "+target.repo+" is not in this registry")
			return
		}
		sim.WriteJSON(w, http.StatusOK, acrRepositoryPropertiesDoc(reg, scope, registry, target.repo))
	case "manifests":
		if !acrRepositoryExists(reg, scope, target.repo) {
			acrPropertiesNotFound(w, "the repository "+target.repo+" is not in this registry")
			return
		}
		manifests := []any{}
		for _, held := range acrRepositoryManifests(reg, scope, target.repo) {
			doc := acrManifestPropertiesDoc(reg, scope, registry, target.repo, held)
			manifests = append(manifests, doc["manifest"])
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"registry": registry, "imageName": target.repo, "manifests": manifests,
		})
	case "manifest":
		held, ok := acrFindManifest(reg, scope, target.repo, target.digest)
		if !ok {
			acrPropertiesNotFound(w, "the manifest "+target.digest+" is not in repository "+target.repo)
			return
		}
		sim.WriteJSON(w, http.StatusOK, acrManifestPropertiesDoc(reg, scope, registry, target.repo, held))
	case "tag":
		held, ok := acrFindTag(reg, scope, target.repo, target.tag)
		if !ok {
			acrPropertiesNotFound(w, "the tag "+target.tag+" is not in repository "+target.repo)
			return
		}
		sim.WriteJSON(w, http.StatusOK, acrTagPropertiesDoc(scope, registry, target.repo, held))
	default:
		acrPropertiesNotFound(w, "the path does not address anything that can be read")
	}
}
