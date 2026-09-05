package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon S3 Storage Lens: dashboards over an account's storage, and the groups
// that carve it into custom segments. What the control plane owns is the
// configuration — the dashboard document, its tags, and the groups — so that
// is what the simulator holds, verbatim as the client composed it.

// S3StorageLensConfiguration is one Storage Lens dashboard configuration.
type S3StorageLensConfiguration struct {
	AccountID     string            `json:"accountId"`
	ConfigID      string            `json:"configId"`
	HomeRegion    string            `json:"homeRegion"`
	IsEnabled     bool              `json:"isEnabled"`
	Configuration s3ControlXMLNode  `json:"configuration"`
	Tags          map[string]string `json:"tags,omitempty"`
}

// S3StorageLensGroup is one custom segment of an account's storage.
type S3StorageLensGroup struct {
	AccountID  string            `json:"accountId"`
	Name       string            `json:"name"`
	HomeRegion string            `json:"homeRegion"`
	Group      s3ControlXMLNode  `json:"group"`
	Tags       map[string]string `json:"tags,omitempty"`
}

var (
	s3StorageLensConfigurations sim.Store[S3StorageLensConfiguration]
	s3StorageLensGroups         sim.Store[S3StorageLensGroup]
)

func s3StorageLensARN(account, configID string) string {
	return fmt.Sprintf("arn:aws:s3:%s:%s:storage-lens/%s", awsRegion(), account, configID)
}

func s3StorageLensGroupARN(account, name string) string {
	return fmt.Sprintf("arn:aws:s3:%s:%s:storage-lens-group/%s", awsRegion(), account, name)
}

func registerS3ControlStorageLens(srv *sim.Server) {
	s3StorageLensConfigurations = sim.MakeStore[S3StorageLensConfiguration](srv.DB(), "s3_storage_lens_configurations")
	s3StorageLensGroups = sim.MakeStore[S3StorageLensGroup](srv.DB(), "s3_storage_lens_groups")

	srv.HandleFunc("PUT /v20180820/storagelens/{configId}", handleS3PutStorageLensConfiguration)
	srv.HandleFunc("GET /v20180820/storagelens/{configId}", handleS3GetStorageLensConfiguration)
	srv.HandleFunc("DELETE /v20180820/storagelens/{configId}", handleS3DeleteStorageLensConfiguration)
	srv.HandleFunc("GET /v20180820/storagelens", handleS3ListStorageLensConfigurations)
	srv.HandleFunc("PUT /v20180820/storagelens/{configId}/tagging", handleS3PutStorageLensConfigurationTagging)
	srv.HandleFunc("GET /v20180820/storagelens/{configId}/tagging", handleS3GetStorageLensConfigurationTagging)
	srv.HandleFunc("DELETE /v20180820/storagelens/{configId}/tagging", handleS3DeleteStorageLensConfigurationTagging)

	srv.HandleFunc("POST /v20180820/storagelensgroup", handleS3CreateStorageLensGroup)
	srv.HandleFunc("GET /v20180820/storagelensgroup/{name}", handleS3GetStorageLensGroup)
	srv.HandleFunc("PUT /v20180820/storagelensgroup/{name}", handleS3UpdateStorageLensGroup)
	srv.HandleFunc("DELETE /v20180820/storagelensgroup/{name}", handleS3DeleteStorageLensGroup)
	srv.HandleFunc("GET /v20180820/storagelensgroup", handleS3ListStorageLensGroups)
}

func handleS3PutStorageLensConfiguration(w http.ResponseWriter, r *http.Request) {
	account, configID := s3ControlAccountID(r), sim.PathParam(r, "configId")
	body, ok := s3ControlReadXMLBody(w, r, "PutStorageLensConfigurationRequest")
	if !ok {
		return
	}
	config, ok := body.Child("StorageLensConfiguration")
	if !ok {
		s3ControlError(w, "InvalidRequest", "StorageLensConfiguration is required", http.StatusBadRequest)
		return
	}
	if id := config.ChildText("Id"); id != "" && id != configID {
		s3ControlError(w, "InvalidRequest",
			"the configuration's Id must match the ConfigId in the request URI", http.StatusBadRequest)
		return
	}
	// AccountLevel is what a dashboard aggregates; the service will not store a
	// configuration that asks for nothing.
	if _, ok := config.Child("AccountLevel"); !ok {
		s3ControlError(w, "InvalidRequest", "AccountLevel is required", http.StatusBadRequest)
		return
	}
	config.SetChild("Id", configID)
	config.SetChild("StorageLensArn", s3StorageLensARN(account, configID))
	stored := S3StorageLensConfiguration{
		AccountID: account, ConfigID: configID, HomeRegion: awsRegion(),
		IsEnabled: config.ChildText("IsEnabled") == "true", Configuration: config,
	}
	if existing, found := s3StorageLensConfigurations.Get(s3AccessPointKey(account, configID)); found {
		stored.Tags = existing.Tags
	}
	if tags := s3ControlTagsFrom(body, "Tags", "Tag"); len(tags) > 0 {
		stored.Tags = tags
	}
	s3StorageLensConfigurations.Put(s3AccessPointKey(account, configID), stored)
	w.WriteHeader(http.StatusOK)
}

func handleS3GetStorageLensConfiguration(w http.ResponseWriter, r *http.Request) {
	account, configID := s3ControlAccountID(r), sim.PathParam(r, "configId")
	stored, ok := s3StorageLensConfigurations.Get(s3AccessPointKey(account, configID))
	if !ok {
		s3ControlError(w, "NoSuchConfiguration",
			"The specified configuration does not exist", http.StatusNotFound)
		return
	}
	s3ControlWriteXMLElement(w, http.StatusOK, "StorageLensConfiguration", stored.Configuration)
}

func handleS3DeleteStorageLensConfiguration(w http.ResponseWriter, r *http.Request) {
	account, configID := s3ControlAccountID(r), sim.PathParam(r, "configId")
	if !s3StorageLensConfigurations.Delete(s3AccessPointKey(account, configID)) {
		s3ControlError(w, "NoSuchConfiguration",
			"The specified configuration does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3ListStorageLensConfigurations(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	type entry struct {
		ID             string `xml:"Id"`
		StorageLensArn string `xml:"StorageLensArn"`
		HomeRegion     string `xml:"HomeRegion"`
		IsEnabled      bool   `xml:"IsEnabled"`
	}
	var items []entry
	for _, cfg := range s3StorageLensConfigurations.List() {
		if cfg.AccountID != account {
			continue
		}
		items = append(items, entry{
			ID: cfg.ConfigID, StorageLensArn: s3StorageLensARN(account, cfg.ConfigID),
			HomeRegion: cfg.HomeRegion, IsEnabled: cfg.IsEnabled,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"ListStorageLensConfigurationResult"`
		Entries []entry  `xml:"StorageLensConfiguration"`
	}{Entries: items})
}

func handleS3PutStorageLensConfigurationTagging(w http.ResponseWriter, r *http.Request) {
	account, configID := s3ControlAccountID(r), sim.PathParam(r, "configId")
	body, ok := s3ControlReadXMLBody(w, r, "PutStorageLensConfigurationTaggingRequest")
	if !ok {
		return
	}
	tags := s3ControlTagsFrom(body, "Tags", "Tag")
	if !s3StorageLensConfigurations.Update(s3AccessPointKey(account, configID),
		func(cfg *S3StorageLensConfiguration) { cfg.Tags = tags }) {
		s3ControlError(w, "NoSuchConfiguration",
			"The specified configuration does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3GetStorageLensConfigurationTagging(w http.ResponseWriter, r *http.Request) {
	account, configID := s3ControlAccountID(r), sim.PathParam(r, "configId")
	stored, ok := s3StorageLensConfigurations.Get(s3AccessPointKey(account, configID))
	if !ok {
		s3ControlError(w, "NoSuchConfiguration",
			"The specified configuration does not exist", http.StatusNotFound)
		return
	}
	s3ControlWriteTags(w, "GetStorageLensConfigurationTaggingResult", "Tag", stored.Tags)
}

func handleS3DeleteStorageLensConfigurationTagging(w http.ResponseWriter, r *http.Request) {
	account, configID := s3ControlAccountID(r), sim.PathParam(r, "configId")
	if !s3StorageLensConfigurations.Update(s3AccessPointKey(account, configID),
		func(cfg *S3StorageLensConfiguration) { cfg.Tags = nil }) {
		s3ControlError(w, "NoSuchConfiguration",
			"The specified configuration does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3CreateStorageLensGroup(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	body, ok := s3ControlReadXMLBody(w, r, "CreateStorageLensGroupRequest")
	if !ok {
		return
	}
	group, ok := body.Child("StorageLensGroup")
	if !ok {
		s3ControlError(w, "InvalidRequest", "StorageLensGroup is required", http.StatusBadRequest)
		return
	}
	name := group.ChildText("Name")
	if name == "" {
		s3ControlError(w, "InvalidRequest", "the group must have a Name", http.StatusBadRequest)
		return
	}
	// A group without a filter selects nothing, which the service rejects
	// rather than storing a segment that can never match.
	if _, ok := group.Child("Filter"); !ok {
		s3ControlError(w, "InvalidRequest", "the group must have a Filter", http.StatusBadRequest)
		return
	}
	if _, exists := s3StorageLensGroups.Get(s3AccessPointKey(account, name)); exists {
		s3ControlError(w, "InvalidRequest",
			"a Storage Lens group named "+name+" already exists in this account", http.StatusBadRequest)
		return
	}
	group.SetChild("StorageLensGroupArn", s3StorageLensGroupARN(account, name))
	s3StorageLensGroups.Put(s3AccessPointKey(account, name), S3StorageLensGroup{
		AccountID: account, Name: name, HomeRegion: awsRegion(), Group: group,
		Tags: s3ControlTagsFrom(body, "Tags", "Tag"),
	})
	w.WriteHeader(http.StatusNoContent)
}

func handleS3GetStorageLensGroup(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	stored, ok := s3StorageLensGroups.Get(s3AccessPointKey(account, name))
	if !ok {
		s3ControlError(w, "NoSuchStorageLensGroup",
			"The specified Storage Lens group does not exist", http.StatusNotFound)
		return
	}
	s3ControlWriteXMLElement(w, http.StatusOK, "StorageLensGroup", stored.Group)
}

func handleS3UpdateStorageLensGroup(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	body, ok := s3ControlReadXMLBody(w, r, "UpdateStorageLensGroupRequest")
	if !ok {
		return
	}
	group, ok := body.Child("StorageLensGroup")
	if !ok {
		s3ControlError(w, "InvalidRequest", "StorageLensGroup is required", http.StatusBadRequest)
		return
	}
	group.SetChild("Name", name)
	group.SetChild("StorageLensGroupArn", s3StorageLensGroupARN(account, name))
	if !s3StorageLensGroups.Update(s3AccessPointKey(account, name),
		func(g *S3StorageLensGroup) { g.Group = group }) {
		s3ControlError(w, "NoSuchStorageLensGroup",
			"The specified Storage Lens group does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleS3DeleteStorageLensGroup(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	if !s3StorageLensGroups.Delete(s3AccessPointKey(account, name)) {
		s3ControlError(w, "NoSuchStorageLensGroup",
			"The specified Storage Lens group does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleS3ListStorageLensGroups(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	type entry struct {
		Name                string `xml:"Name"`
		StorageLensGroupArn string `xml:"StorageLensGroupArn"`
		HomeRegion          string `xml:"HomeRegion"`
	}
	var items []entry
	for _, g := range s3StorageLensGroups.List() {
		if g.AccountID != account {
			continue
		}
		items = append(items, entry{
			Name: g.Name, StorageLensGroupArn: s3StorageLensGroupARN(account, g.Name),
			HomeRegion: g.HomeRegion,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	// StorageLensGroupList is a flattened list, so each group sits directly
	// under the result rather than inside a wrapper element.
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"ListStorageLensGroupsResult"`
		Entries []entry  `xml:"StorageLensGroup"`
	}{Entries: items})
}

// s3ControlTagsFrom reads a tag list out of a stored document. The control
// plane spells its tag lists differently per resource — a Storage Lens tag is
// a Tag, a job tag is an unnamed list member — so the caller names the element
// the model gives that list.
func s3ControlTagsFrom(body s3ControlXMLNode, listElement, tagElement string) map[string]string {
	list, ok := body.Child(listElement)
	if !ok {
		return nil
	}
	tags := map[string]string{}
	for _, child := range list.Children {
		if child.Name != tagElement {
			continue
		}
		if key := child.ChildText("Key"); key != "" {
			tags[key] = child.ChildText("Value")
		}
	}
	if len(tags) == 0 {
		return nil
	}
	return tags
}

// s3ControlWriteTags writes a tag list back in the shape the reading operation
// declares.
func s3ControlWriteTags(w http.ResponseWriter, resultElement, tagElement string, tags map[string]string) {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "<"+resultElement+"><Tags>")
	for _, k := range keys {
		_, _ = io.WriteString(w, "<"+tagElement+">")
		_ = xml.EscapeText(w, nil)
		_, _ = io.WriteString(w, "<Key>")
		_ = xml.EscapeText(w, []byte(k))
		_, _ = io.WriteString(w, "</Key><Value>")
		_ = xml.EscapeText(w, []byte(tags[k]))
		_, _ = io.WriteString(w, "</Value></"+tagElement+">")
	}
	_, _ = io.WriteString(w, "</Tags></"+resultElement+">")
}
