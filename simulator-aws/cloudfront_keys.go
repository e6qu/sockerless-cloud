package main

import (
	"encoding/xml"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CloudFront KeyGroup + PublicKey — used to sign signed-URL / signed-
// cookie verification. PublicKey wraps an Encoded PEM; KeyGroup
// references a list of PublicKey IDs.

type CFPublicKeyConfig struct {
	XMLName         xml.Name `xml:"PublicKeyConfig"`
	Xmlns           string   `xml:"xmlns,attr,omitempty"`
	CallerReference string   `xml:"CallerReference"`
	Name            string   `xml:"Name"`
	EncodedKey      string   `xml:"EncodedKey"`
	Comment         string   `xml:"Comment,omitempty"`
}

type CFPublicKey struct {
	XMLName         xml.Name          `xml:"PublicKey"`
	Xmlns           string            `xml:"xmlns,attr,omitempty"`
	Id              string            `xml:"Id"`
	CreatedTime     string            `xml:"CreatedTime"`
	PublicKeyConfig CFPublicKeyConfig `xml:"PublicKeyConfig"`
}

type CFPublicKeySummary struct {
	Id          string `xml:"Id"`
	Name        string `xml:"Name"`
	CreatedTime string `xml:"CreatedTime"`
	EncodedKey  string `xml:"EncodedKey"`
	Comment     string `xml:"Comment,omitempty"`
}

type CFPublicKeyList struct {
	XMLName    xml.Name             `xml:"PublicKeyList"`
	Xmlns      string               `xml:"xmlns,attr,omitempty"`
	MaxItems   int                  `xml:"MaxItems"`
	Quantity   int                  `xml:"Quantity"`
	NextMarker string               `xml:"NextMarker,omitempty"`
	Items      []CFPublicKeySummary `xml:"Items>PublicKeySummary,omitempty"`
}

type cfStoredPublicKey struct {
	PublicKey CFPublicKey
	ETag      string
}

type CFKeyGroupConfig struct {
	XMLName xml.Name `xml:"KeyGroupConfig"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Name    string   `xml:"Name"`
	Items   []string `xml:"Items>PublicKey"`
	Comment string   `xml:"Comment,omitempty"`
}

type CFKeyGroup struct {
	XMLName          xml.Name         `xml:"KeyGroup"`
	Xmlns            string           `xml:"xmlns,attr,omitempty"`
	Id               string           `xml:"Id"`
	LastModifiedTime string           `xml:"LastModifiedTime"`
	KeyGroupConfig   CFKeyGroupConfig `xml:"KeyGroupConfig"`
}

type CFKeyGroupSummary struct {
	KeyGroup CFKeyGroup `xml:"KeyGroup"`
}

type CFKeyGroupList struct {
	XMLName    xml.Name            `xml:"KeyGroupList"`
	Xmlns      string              `xml:"xmlns,attr,omitempty"`
	MaxItems   int                 `xml:"MaxItems"`
	Quantity   int                 `xml:"Quantity"`
	NextMarker string              `xml:"NextMarker,omitempty"`
	Items      []CFKeyGroupSummary `xml:"Items>KeyGroupSummary,omitempty"`
}

type cfStoredKeyGroup struct {
	KeyGroup CFKeyGroup
	ETag     string
}

var (
	cfPublicKeys sim.Store[cfStoredPublicKey]
	cfKeyGroups  sim.Store[cfStoredKeyGroup]
)

func registerCloudFrontKeys(srv *sim.Server) {
	cfPublicKeys = sim.MakeStore[cfStoredPublicKey](srv.DB(), "cloudfront_public_keys")
	cfKeyGroups = sim.MakeStore[cfStoredKeyGroup](srv.DB(), "cloudfront_key_groups")

	mux := srv

	publicKeyResource := cloudTrailRESTResource("AWS::CloudFront::PublicKey", "id")
	keyGroupResource := cloudTrailRESTResource("AWS::CloudFront::KeyGroup", "id")
	// PublicKey
	mux.HandleFunc("POST /"+cfAPIVersion+"/public-key", cloudTrailRecordedREST("CreatePublicKey", "cloudfront.amazonaws.com", nil, handleCFCreatePublicKey))
	mux.HandleFunc("GET /"+cfAPIVersion+"/public-key", cloudTrailRecordedREST("ListPublicKeys", "cloudfront.amazonaws.com", nil, handleCFListPublicKeys))
	mux.HandleFunc("GET /"+cfAPIVersion+"/public-key/{id}", cloudTrailRecordedREST("GetPublicKey", "cloudfront.amazonaws.com", publicKeyResource, handleCFGetPublicKey))
	mux.HandleFunc("GET /"+cfAPIVersion+"/public-key/{id}/config", cloudTrailRecordedREST("GetPublicKeyConfig", "cloudfront.amazonaws.com", publicKeyResource, handleCFGetPublicKeyConfig))
	mux.HandleFunc("PUT /"+cfAPIVersion+"/public-key/{id}/config", cloudTrailRecordedREST("UpdatePublicKey", "cloudfront.amazonaws.com", publicKeyResource, handleCFUpdatePublicKey))
	mux.HandleFunc("DELETE /"+cfAPIVersion+"/public-key/{id}", cloudTrailRecordedREST("DeletePublicKey", "cloudfront.amazonaws.com", publicKeyResource, handleCFDeletePublicKey))

	// KeyGroup
	mux.HandleFunc("POST /"+cfAPIVersion+"/key-group", cloudTrailRecordedREST("CreateKeyGroup", "cloudfront.amazonaws.com", nil, handleCFCreateKeyGroup))
	mux.HandleFunc("GET /"+cfAPIVersion+"/key-group", cloudTrailRecordedREST("ListKeyGroups", "cloudfront.amazonaws.com", nil, handleCFListKeyGroups))
	mux.HandleFunc("GET /"+cfAPIVersion+"/key-group/{id}", cloudTrailRecordedREST("GetKeyGroup", "cloudfront.amazonaws.com", keyGroupResource, handleCFGetKeyGroup))
	mux.HandleFunc("GET /"+cfAPIVersion+"/key-group/{id}/config", cloudTrailRecordedREST("GetKeyGroupConfig", "cloudfront.amazonaws.com", keyGroupResource, handleCFGetKeyGroupConfig))
	mux.HandleFunc("PUT /"+cfAPIVersion+"/key-group/{id}", cloudTrailRecordedREST("UpdateKeyGroup", "cloudfront.amazonaws.com", keyGroupResource, handleCFUpdateKeyGroup))
	mux.HandleFunc("DELETE /"+cfAPIVersion+"/key-group/{id}", cloudTrailRecordedREST("DeleteKeyGroup", "cloudfront.amazonaws.com", keyGroupResource, handleCFDeleteKeyGroup))
}

func handleCFCreatePublicKey(w http.ResponseWriter, r *http.Request) {
	var cfg CFPublicKeyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode PublicKeyConfig: "+err.Error())
		return
	}
	if cfg.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	if cfg.CallerReference == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "CallerReference is required")
		return
	}
	if cfg.EncodedKey == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "EncodedKey is required")
		return
	}
	cfg.Xmlns = ""
	id := cfRandomID("K")
	etag := cfETag()
	pk := CFPublicKey{
		Xmlns:           cfNamespace,
		Id:              id,
		CreatedTime:     cfNowISO(),
		PublicKeyConfig: cfg,
	}
	cfPublicKeys.Put(id, cfStoredPublicKey{PublicKey: pk, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/public-key/"+id)
	cfWriteXML(w, http.StatusCreated, pk)
}

func handleCFGetPublicKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfPublicKeys.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchPublicKey", "The specified public key does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.PublicKey)
}

func handleCFGetPublicKeyConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfPublicKeys.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchPublicKey", "The specified public key does not exist.")
		return
	}
	cfg := stored.PublicKey.PublicKeyConfig
	cfg.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, cfg)
}

func handleCFUpdatePublicKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfPublicKeys.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchPublicKey", "The specified public key does not exist.")
		return
	}
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match header is required.")
		return
	}
	if ifMatch != stored.ETag {
		cfWriteError(w, http.StatusPreconditionFailed, "PreconditionFailed", "The If-Match version is missing or does not match the resource's current ETag.")
		return
	}
	var cfg CFPublicKeyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode PublicKeyConfig: "+err.Error())
		return
	}
	cfg.Xmlns = ""
	newETag := cfETag()
	stored.PublicKey.PublicKeyConfig = cfg
	stored.ETag = newETag
	cfPublicKeys.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.PublicKey)
}

func handleCFDeletePublicKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfPublicKeys.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchPublicKey", "The specified public key does not exist.")
		return
	}
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match header is required.")
		return
	}
	if ifMatch != stored.ETag {
		cfWriteError(w, http.StatusPreconditionFailed, "PreconditionFailed", "The If-Match version is missing or does not match the resource's current ETag.")
		return
	}
	cfPublicKeys.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListPublicKeys(w http.ResponseWriter, r *http.Request) {
	items := []CFPublicKeySummary{}
	for _, stored := range cfPublicKeys.List() {
		items = append(items, CFPublicKeySummary{
			Id:          stored.PublicKey.Id,
			Name:        stored.PublicKey.PublicKeyConfig.Name,
			CreatedTime: stored.PublicKey.CreatedTime,
			EncodedKey:  stored.PublicKey.PublicKeyConfig.EncodedKey,
			Comment:     stored.PublicKey.PublicKeyConfig.Comment,
		})
	}
	list := CFPublicKeyList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	}
	cfWriteXML(w, http.StatusOK, list)
}

func handleCFCreateKeyGroup(w http.ResponseWriter, r *http.Request) {
	var cfg CFKeyGroupConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode KeyGroupConfig: "+err.Error())
		return
	}
	if cfg.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	if len(cfg.Items) == 0 {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "KeyGroupConfig.Items must contain at least one PublicKey ID")
		return
	}
	cfg.Xmlns = ""
	id := cfRandomID("KG")
	etag := cfETag()
	kg := CFKeyGroup{
		Xmlns:            cfNamespace,
		Id:               id,
		LastModifiedTime: cfNowISO(),
		KeyGroupConfig:   cfg,
	}
	cfKeyGroups.Put(id, cfStoredKeyGroup{KeyGroup: kg, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/key-group/"+id)
	cfWriteXML(w, http.StatusCreated, kg)
}

func handleCFGetKeyGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfKeyGroups.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified key group does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.KeyGroup)
}

func handleCFGetKeyGroupConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfKeyGroups.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified key group does not exist.")
		return
	}
	cfg := stored.KeyGroup.KeyGroupConfig
	cfg.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, cfg)
}

func handleCFUpdateKeyGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfKeyGroups.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified key group does not exist.")
		return
	}
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match header is required.")
		return
	}
	if ifMatch != stored.ETag {
		cfWriteError(w, http.StatusPreconditionFailed, "PreconditionFailed", "The If-Match version is missing or does not match the resource's current ETag.")
		return
	}
	var cfg CFKeyGroupConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode KeyGroupConfig: "+err.Error())
		return
	}
	cfg.Xmlns = ""
	newETag := cfETag()
	stored.KeyGroup.KeyGroupConfig = cfg
	stored.KeyGroup.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfKeyGroups.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.KeyGroup)
}

func handleCFDeleteKeyGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfKeyGroups.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified key group does not exist.")
		return
	}
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match header is required.")
		return
	}
	if ifMatch != stored.ETag {
		cfWriteError(w, http.StatusPreconditionFailed, "PreconditionFailed", "The If-Match version is missing or does not match the resource's current ETag.")
		return
	}
	cfKeyGroups.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListKeyGroups(w http.ResponseWriter, r *http.Request) {
	items := []CFKeyGroupSummary{}
	for _, stored := range cfKeyGroups.List() {
		items = append(items, CFKeyGroupSummary{KeyGroup: stored.KeyGroup})
	}
	list := CFKeyGroupList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	}
	cfWriteXML(w, http.StatusOK, list)
}
