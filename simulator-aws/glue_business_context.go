package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// The AWS Glue Data Catalog business-context API stores glossaries, form
// types, asset types, assets, attachments, and term associations as durable
// regional catalog state. The wire structures deliberately use the public
// AWS JSON 1.1 member names so official clients receive the same shapes they
// receive from AWS.
type GlueBusinessGlossary struct {
	Id          string `json:"Id"`
	Name        string `json:"Name"`
	Description string `json:"Description,omitempty"`
}

type GlueBusinessGlossaryTerm struct {
	Id               string `json:"Id"`
	GlossaryId       string `json:"GlossaryId"`
	Name             string `json:"Name"`
	ShortDescription string `json:"ShortDescription,omitempty"`
	LongDescription  string `json:"LongDescription,omitempty"`
}

type GlueBusinessFormType struct {
	Id     string `json:"Id"`
	Name   string `json:"Name"`
	Schema string `json:"Schema"`
}

type GlueBusinessAssetTypeFormReference struct {
	FormTypeIdentifier string `json:"FormTypeIdentifier"`
}

type GlueBusinessAssetType struct {
	Id    string                                        `json:"Id"`
	Name  string                                        `json:"Name"`
	Forms map[string]GlueBusinessAssetTypeFormReference `json:"Forms"`
}

type GlueBusinessAssetFormEntry struct {
	FormTypeId string `json:"FormTypeId,omitempty"`
	Content    string `json:"Content,omitempty"`
}

type GlueBusinessIterableItem struct {
	ItemId        string                                `json:"ItemId"`
	ItemName      string                                `json:"ItemName"`
	Description   string                                `json:"Description,omitempty"`
	GlossaryTerms []string                              `json:"GlossaryTerms,omitempty"`
	Forms         map[string]GlueBusinessAssetFormEntry `json:"Forms,omitempty"`
	Attachments   map[string]GlueBusinessAssetFormEntry `json:"Attachments,omitempty"`
}

type GlueBusinessIterableForm struct {
	FormTypeId string                              `json:"FormTypeId"`
	Items      map[string]GlueBusinessIterableItem `json:"Items,omitempty"`
}

type GlueBusinessAsset struct {
	Id            string                                `json:"Id"`
	AssetTypeId   string                                `json:"AssetTypeId"`
	Name          string                                `json:"Name"`
	Description   string                                `json:"Description,omitempty"`
	CreatedAt     float64                               `json:"CreatedAt"`
	UpdatedAt     float64                               `json:"UpdatedAt"`
	Forms         map[string]GlueBusinessAssetFormEntry `json:"Forms"`
	Attachments   map[string]GlueBusinessAssetFormEntry `json:"Attachments"`
	GlossaryTerms []string                              `json:"GlossaryTerms"`
	IterableForms map[string]GlueBusinessIterableForm   `json:"IterableForms"`
}

var (
	glueBusinessGlossaries sim.Store[GlueBusinessGlossary]
	glueBusinessTerms      sim.Store[GlueBusinessGlossaryTerm]
	glueBusinessFormTypes  sim.Store[GlueBusinessFormType]
	glueBusinessAssetTypes sim.Store[GlueBusinessAssetType]
	glueBusinessAssets     sim.Store[GlueBusinessAsset]
	glueBusinessTokens     sim.Store[string]
)

func registerGlueBusinessContext(r *sim.AWSRouter, srv *sim.Server) {
	glueBusinessGlossaries = sim.MakeStore[GlueBusinessGlossary](srv.DB(), "glue_business_glossaries")
	glueBusinessTerms = sim.MakeStore[GlueBusinessGlossaryTerm](srv.DB(), "glue_business_glossary_terms")
	glueBusinessFormTypes = sim.MakeStore[GlueBusinessFormType](srv.DB(), "glue_business_form_types")
	glueBusinessAssetTypes = sim.MakeStore[GlueBusinessAssetType](srv.DB(), "glue_business_asset_types")
	glueBusinessAssets = sim.MakeStore[GlueBusinessAsset](srv.DB(), "glue_business_assets")
	glueBusinessTokens = sim.MakeStore[string](srv.DB(), "glue_business_idempotency_tokens")

	registerGlueGlossaryOperations(r)
	registerGlueAssetOperations(r)
	registerGlueEntityOperations(r)
}

func registerGlueGlossaryOperations(r *sim.AWSRouter) {
	r.Register("AWSGlue.CreateGlossary", handleGlueCreateGlossary)
	r.Register("AWSGlue.GetGlossary", handleGlueGetGlossary)
	r.Register("AWSGlue.UpdateGlossary", handleGlueUpdateGlossary)
	r.Register("AWSGlue.DeleteGlossary", handleGlueDeleteGlossary)
	r.Register("AWSGlue.ListGlossaries", handleGlueListGlossaries)
	r.Register("AWSGlue.CreateGlossaryTerm", handleGlueCreateGlossaryTerm)
	r.Register("AWSGlue.GetGlossaryTerm", handleGlueGetGlossaryTerm)
	r.Register("AWSGlue.UpdateGlossaryTerm", handleGlueUpdateGlossaryTerm)
	r.Register("AWSGlue.DeleteGlossaryTerm", handleGlueDeleteGlossaryTerm)
	r.Register("AWSGlue.ListGlossaryTerms", handleGlueListGlossaryTerms)
	r.Register("AWSGlue.AssociateGlossaryTerms", handleGlueAssociateGlossaryTerms)
	r.Register("AWSGlue.DisassociateGlossaryTerms", handleGlueDisassociateGlossaryTerms)
}

func registerGlueAssetOperations(r *sim.AWSRouter) {
	r.Register("AWSGlue.PutFormType", handleGluePutFormType)
	r.Register("AWSGlue.GetFormType", handleGlueGetFormType)
	r.Register("AWSGlue.DeleteFormType", handleGlueDeleteFormType)
	r.Register("AWSGlue.ListFormTypes", handleGlueListFormTypes)
	r.Register("AWSGlue.PutAssetType", handleGluePutAssetType)
	r.Register("AWSGlue.GetAssetType", handleGlueGetAssetType)
	r.Register("AWSGlue.DeleteAssetType", handleGlueDeleteAssetType)
	r.Register("AWSGlue.ListAssetTypes", handleGlueListAssetTypes)

	r.Register("AWSGlue.PutAsset", handleGluePutAsset)
	r.Register("AWSGlue.GetAsset", handleGlueGetAsset)
	r.Register("AWSGlue.UpdateAsset", handleGlueUpdateAsset)
	r.Register("AWSGlue.DeleteAsset", handleGlueDeleteAsset)
	r.Register("AWSGlue.SearchAssets", handleGlueSearchAssets)
	r.Register("AWSGlue.PutAttachment", handleGluePutAttachment)
	r.Register("AWSGlue.DeleteAttachment", handleGlueDeleteAttachment)
	r.Register("AWSGlue.ListIterableForms", handleGlueListIterableForms)
	r.Register("AWSGlue.BatchGetIterableForms", handleGlueBatchGetIterableForms)
}

func registerGlueEntityOperations(r *sim.AWSRouter) {
	r.Register("AWSGlue.BatchGetDataQualityRulesetEvaluationRun", handleGlueBatchGetDataQualityRulesetEvaluationRun)
	r.Register("AWSGlue.ListEntities", handleGlueListEntities)
	r.Register("AWSGlue.DescribeEntity", handleGlueDescribeEntity)
	r.Register("AWSGlue.GetEntityRecords", handleGlueGetEntityRecords)
}

func glueDecodeBusinessRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return false
	}
	return true
}

func glueBusinessID(prefix string) string {
	return prefix + "-" + strings.ReplaceAll(glueHashID(), "-", "")[:20]
}

func glueBusinessTokenKey(operation, token string) string {
	return operation + "\x00" + token
}

func glueBusinessStringField(item map[string]any, field string) string {
	value, _ := item[field].(string)
	return value
}

func glueResolveGlossary(identifier string) (GlueBusinessGlossary, bool) {
	if value, ok := glueBusinessGlossaries.Get(identifier); ok {
		return value, true
	}
	for _, value := range glueBusinessGlossaries.List() {
		if value.Name == identifier {
			return value, true
		}
	}
	return GlueBusinessGlossary{}, false
}

func glueResolveGlossaryTerm(identifier string) (GlueBusinessGlossaryTerm, bool) {
	if value, ok := glueBusinessTerms.Get(identifier); ok {
		return value, true
	}
	for _, value := range glueBusinessTerms.List() {
		if value.Name == identifier {
			return value, true
		}
	}
	return GlueBusinessGlossaryTerm{}, false
}

func handleGlueCreateGlossary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"Name"`
		Description string `json:"Description"`
		ClientToken string `json:"ClientToken"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	if req.ClientToken != "" {
		if id, ok := glueBusinessTokens.Get(glueBusinessTokenKey("CreateGlossary", req.ClientToken)); ok {
			if glossary, ok := glueBusinessGlossaries.Get(id); ok {
				glueWriteJSON(w, http.StatusOK, glossary)
				return
			}
		}
	}
	for _, glossary := range glueBusinessGlossaries.List() {
		if glossary.Name == req.Name {
			glueWriteError(w, "AlreadyExistsException", "Glossary already exists: "+req.Name)
			return
		}
	}
	glossary := GlueBusinessGlossary{
		Id: glueBusinessID("gl"), Name: req.Name, Description: req.Description,
	}
	glueBusinessGlossaries.Put(glossary.Id, glossary)
	if req.ClientToken != "" {
		glueBusinessTokens.Put(glueBusinessTokenKey("CreateGlossary", req.ClientToken), glossary.Id)
	}
	glueWriteJSON(w, http.StatusOK, glossary)
}

func handleGlueGetGlossary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"Identifier"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	glossary, ok := glueResolveGlossary(req.Identifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Glossary not found: "+req.Identifier)
		return
	}
	glueWriteJSON(w, http.StatusOK, glossary)
}

func handleGlueUpdateGlossary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier  string  `json:"Identifier"`
		Name        *string `json:"Name"`
		Description *string `json:"Description"`
		ClientToken string  `json:"ClientToken"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	glossary, ok := glueResolveGlossary(req.Identifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Glossary not found: "+req.Identifier)
		return
	}
	if req.Name != nil {
		for _, existing := range glueBusinessGlossaries.List() {
			if existing.Id != glossary.Id && existing.Name == *req.Name {
				glueWriteError(w, "AlreadyExistsException", "Glossary already exists: "+*req.Name)
				return
			}
		}
		glossary.Name = *req.Name
	}
	if req.Description != nil {
		glossary.Description = *req.Description
	}
	glueBusinessGlossaries.Put(glossary.Id, glossary)
	glueWriteJSON(w, http.StatusOK, glossary)
}

func handleGlueDeleteGlossary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"Identifier"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	glossary, ok := glueResolveGlossary(req.Identifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Glossary not found: "+req.Identifier)
		return
	}
	for _, term := range glueBusinessTerms.List() {
		if term.GlossaryId == glossary.Id {
			glueWriteError(w, "ConflictException", "Glossary contains terms: "+glossary.Id)
			return
		}
	}
	glueBusinessGlossaries.Delete(glossary.Id)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueListGlossaries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults *int   `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	all := glueBusinessGlossaries.List()
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	page, next := awsPage(all, req.NextToken, derefIntDefault(req.MaxResults, 0), 1000)
	response := map[string]any{"Items": page}
	if next != "" {
		response["NextToken"] = next
	}
	glueWriteJSON(w, http.StatusOK, response)
}

func handleGlueCreateGlossaryTerm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GlossaryIdentifier string `json:"GlossaryIdentifier"`
		Name               string `json:"Name"`
		ShortDescription   string `json:"ShortDescription"`
		LongDescription    string `json:"LongDescription"`
		ClientToken        string `json:"ClientToken"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	glossary, ok := glueResolveGlossary(req.GlossaryIdentifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Glossary not found: "+req.GlossaryIdentifier)
		return
	}
	if req.ClientToken != "" {
		if id, ok := glueBusinessTokens.Get(glueBusinessTokenKey("CreateGlossaryTerm", req.ClientToken)); ok {
			if term, ok := glueBusinessTerms.Get(id); ok {
				glueWriteJSON(w, http.StatusOK, term)
				return
			}
		}
	}
	for _, term := range glueBusinessTerms.List() {
		if term.GlossaryId == glossary.Id && term.Name == req.Name {
			glueWriteError(w, "AlreadyExistsException", "Glossary term already exists: "+req.Name)
			return
		}
	}
	term := GlueBusinessGlossaryTerm{
		Id: glueBusinessID("gt"), GlossaryId: glossary.Id, Name: req.Name,
		ShortDescription: req.ShortDescription, LongDescription: req.LongDescription,
	}
	glueBusinessTerms.Put(term.Id, term)
	if req.ClientToken != "" {
		glueBusinessTokens.Put(glueBusinessTokenKey("CreateGlossaryTerm", req.ClientToken), term.Id)
	}
	glueWriteJSON(w, http.StatusOK, term)
}

func handleGlueGetGlossaryTerm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"Identifier"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	term, ok := glueResolveGlossaryTerm(req.Identifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Glossary term not found: "+req.Identifier)
		return
	}
	glueWriteJSON(w, http.StatusOK, term)
}

func handleGlueUpdateGlossaryTerm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier       string  `json:"Identifier"`
		Name             *string `json:"Name"`
		ShortDescription *string `json:"ShortDescription"`
		LongDescription  *string `json:"LongDescription"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	term, ok := glueResolveGlossaryTerm(req.Identifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Glossary term not found: "+req.Identifier)
		return
	}
	if req.Name != nil {
		for _, existing := range glueBusinessTerms.List() {
			if existing.Id != term.Id && existing.GlossaryId == term.GlossaryId && existing.Name == *req.Name {
				glueWriteError(w, "AlreadyExistsException", "Glossary term already exists: "+*req.Name)
				return
			}
		}
		term.Name = *req.Name
	}
	if req.ShortDescription != nil {
		term.ShortDescription = *req.ShortDescription
	}
	if req.LongDescription != nil {
		term.LongDescription = *req.LongDescription
	}
	glueBusinessTerms.Put(term.Id, term)
	glueWriteJSON(w, http.StatusOK, term)
}

func handleGlueDeleteGlossaryTerm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"Identifier"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	term, ok := glueResolveGlossaryTerm(req.Identifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Glossary term not found: "+req.Identifier)
		return
	}
	glueBusinessTerms.Delete(term.Id)
	for _, asset := range glueBusinessAssets.List() {
		asset.GlossaryTerms = removeGlueBusinessStrings(asset.GlossaryTerms, map[string]bool{term.Id: true})
		glueBusinessAssets.Put(asset.Id, asset)
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueListGlossaryTerms(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GlossaryIdentifier string `json:"GlossaryIdentifier"`
		MaxResults         *int   `json:"MaxResults"`
		NextToken          string `json:"NextToken"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	glossary, ok := glueResolveGlossary(req.GlossaryIdentifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Glossary not found: "+req.GlossaryIdentifier)
		return
	}
	items := make([]map[string]any, 0)
	for _, term := range glueBusinessTerms.List() {
		if term.GlossaryId == glossary.Id {
			items = append(items, map[string]any{
				"Id": term.Id, "Name": term.Name, "ShortDescription": term.ShortDescription,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return glueBusinessStringField(items[i], "Name") < glueBusinessStringField(items[j], "Name")
	})
	page, next := awsPage(items, req.NextToken, derefIntDefault(req.MaxResults, 0), 1000)
	response := map[string]any{"Items": page}
	if next != "" {
		response["NextToken"] = next
	}
	glueWriteJSON(w, http.StatusOK, response)
}

func glueResolveFormType(identifier string) (GlueBusinessFormType, bool) {
	if value, ok := glueBusinessFormTypes.Get(identifier); ok {
		return value, true
	}
	for _, value := range glueBusinessFormTypes.List() {
		if value.Name == identifier {
			return value, true
		}
	}
	return GlueBusinessFormType{}, false
}

func handleGluePutFormType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"Name"`
		Schema      string `json:"Schema"`
		ClientToken string `json:"ClientToken"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	if req.Name == "" || req.Schema == "" {
		glueWriteError(w, "InvalidInputException", "Name and Schema are required")
		return
	}
	if first := req.Name[0]; first < 'A' || first > 'Z' {
		glueWriteError(w, "InvalidInputException", "Name must start with an uppercase letter")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	formType := GlueBusinessFormType{Id: req.Name, Name: req.Name, Schema: req.Schema}
	glueBusinessFormTypes.Put(formType.Id, formType)
	glueWriteJSON(w, http.StatusOK, formType)
}

func handleGlueGetFormType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"Identifier"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	formType, ok := glueResolveFormType(req.Identifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Form type not found: "+req.Identifier)
		return
	}
	glueWriteJSON(w, http.StatusOK, formType)
}

func handleGlueDeleteFormType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"Identifier"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	formType, ok := glueResolveFormType(req.Identifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Form type not found: "+req.Identifier)
		return
	}
	for _, assetType := range glueBusinessAssetTypes.List() {
		for _, reference := range assetType.Forms {
			if reference.FormTypeIdentifier == formType.Id {
				glueWriteError(w, "ConflictException", "Form type is referenced by asset type: "+assetType.Id)
				return
			}
		}
	}
	glueBusinessFormTypes.Delete(formType.Id)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueListFormTypes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults *int   `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	all := glueBusinessFormTypes.List()
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	items := make([]map[string]any, 0, len(all))
	for _, value := range all {
		items = append(items, map[string]any{"Id": value.Id, "Name": value.Name})
	}
	page, next := awsPage(items, req.NextToken, derefIntDefault(req.MaxResults, 0), 1000)
	response := map[string]any{"Items": page}
	if next != "" {
		response["NextToken"] = next
	}
	glueWriteJSON(w, http.StatusOK, response)
}

func glueResolveAssetType(identifier string) (GlueBusinessAssetType, bool) {
	if value, ok := glueBusinessAssetTypes.Get(identifier); ok {
		return value, true
	}
	for _, value := range glueBusinessAssetTypes.List() {
		if value.Name == identifier {
			return value, true
		}
	}
	if identifier == "DataSet" {
		return GlueBusinessAssetType{Id: "DataSet", Name: "DataSet", Forms: map[string]GlueBusinessAssetTypeFormReference{}}, true
	}
	return GlueBusinessAssetType{}, false
}

func handleGluePutAssetType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string                                        `json:"Name"`
		Forms       map[string]GlueBusinessAssetTypeFormReference `json:"Forms"`
		ClientToken string                                        `json:"ClientToken"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	if req.Name == "" || req.Forms == nil {
		glueWriteError(w, "InvalidInputException", "Name and Forms are required")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	for _, reference := range req.Forms {
		if _, ok := glueResolveFormType(reference.FormTypeIdentifier); !ok {
			glueWriteError(w, "EntityNotFoundException", "Form type not found: "+reference.FormTypeIdentifier)
			return
		}
	}
	assetType := GlueBusinessAssetType{Id: req.Name, Name: req.Name, Forms: req.Forms}
	glueBusinessAssetTypes.Put(assetType.Id, assetType)
	glueWriteJSON(w, http.StatusOK, assetType)
}

func handleGlueGetAssetType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"Identifier"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	assetType, ok := glueResolveAssetType(req.Identifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Asset type not found: "+req.Identifier)
		return
	}
	glueWriteJSON(w, http.StatusOK, assetType)
}

func handleGlueDeleteAssetType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"Identifier"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	assetType, ok := glueResolveAssetType(req.Identifier)
	if !ok || assetType.Id == "DataSet" {
		glueWriteError(w, "EntityNotFoundException", "Asset type not found: "+req.Identifier)
		return
	}
	for _, asset := range glueBusinessAssets.List() {
		if asset.AssetTypeId == assetType.Id {
			glueWriteError(w, "ConflictException", "Asset type is referenced by asset: "+asset.Id)
			return
		}
	}
	glueBusinessAssetTypes.Delete(assetType.Id)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueListAssetTypes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults *int   `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	items := []map[string]any{{"Id": "DataSet", "Name": "DataSet"}}
	for _, value := range glueBusinessAssetTypes.List() {
		if value.Id != "DataSet" {
			items = append(items, map[string]any{"Id": value.Id, "Name": value.Name})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return glueBusinessStringField(items[i], "Name") < glueBusinessStringField(items[j], "Name")
	})
	page, next := awsPage(items, req.NextToken, derefIntDefault(req.MaxResults, 0), 1000)
	response := map[string]any{"Items": page}
	if next != "" {
		response["NextToken"] = next
	}
	glueWriteJSON(w, http.StatusOK, response)
}

func glueValidateAssetForms(assetType GlueBusinessAssetType, forms map[string]GlueBusinessAssetFormEntry) error {
	for name, entry := range forms {
		reference, ok := assetType.Forms[name]
		if len(assetType.Forms) > 0 && !ok {
			return fmt.Errorf("form %q is not declared by asset type %q", name, assetType.Id)
		}
		if ok && entry.FormTypeId != reference.FormTypeIdentifier {
			return fmt.Errorf("form %q must use form type %q", name, reference.FormTypeIdentifier)
		}
		if entry.FormTypeId != "" {
			if _, ok := glueResolveFormType(entry.FormTypeId); !ok {
				return fmt.Errorf("form type not found: %s", entry.FormTypeId)
			}
		}
		if entry.Content != "" && !json.Valid([]byte(entry.Content)) {
			return fmt.Errorf("form %q content is not valid JSON", name)
		}
	}
	return nil
}

func handleGluePutAsset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssetTypeId string                                `json:"AssetTypeId"`
		Identifier  string                                `json:"Identifier"`
		Name        string                                `json:"Name"`
		Description string                                `json:"Description"`
		Forms       map[string]GlueBusinessAssetFormEntry `json:"Forms"`
		ClientToken string                                `json:"ClientToken"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	if req.AssetTypeId == "" || req.Identifier == "" || req.Name == "" || req.Forms == nil {
		glueWriteError(w, "InvalidInputException", "AssetTypeId, Identifier, Name, and Forms are required")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	assetType, ok := glueResolveAssetType(req.AssetTypeId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Asset type not found: "+req.AssetTypeId)
		return
	}
	if err := glueValidateAssetForms(assetType, req.Forms); err != nil {
		glueWriteError(w, "InvalidInputException", err.Error())
		return
	}
	now := glueEpochNow()
	asset, exists := glueBusinessAssets.Get(req.Identifier)
	if !exists {
		asset = GlueBusinessAsset{
			Id: req.Identifier, CreatedAt: now, Attachments: map[string]GlueBusinessAssetFormEntry{},
			GlossaryTerms: []string{}, IterableForms: map[string]GlueBusinessIterableForm{},
		}
	}
	asset.AssetTypeId = assetType.Id
	asset.Name = req.Name
	asset.Description = req.Description
	asset.Forms = req.Forms
	asset.UpdatedAt = now
	glueBusinessAssets.Put(asset.Id, asset)
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"Id": asset.Id, "Name": asset.Name, "Description": asset.Description,
		"CreatedAt": asset.CreatedAt, "Forms": asset.Forms,
	})
}

func handleGlueGetAsset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"Identifier"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	asset, ok := glueBusinessAssets.Get(req.Identifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Asset not found: "+req.Identifier)
		return
	}
	forms := glueAssetIterableForms(asset)
	iterable := make(map[string]map[string]string, len(forms))
	for name, form := range forms {
		iterable[name] = map[string]string{"FormTypeId": form.FormTypeId}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"Id": asset.Id, "AssetTypeId": asset.AssetTypeId, "Name": asset.Name,
		"Description": asset.Description, "CreatedAt": asset.CreatedAt, "UpdatedAt": asset.UpdatedAt,
		"Forms": asset.Forms, "Attachments": asset.Attachments, "GlossaryTerms": asset.GlossaryTerms,
		"IterableForms": iterable,
	})
}

func handleGlueUpdateAsset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier  string  `json:"Identifier"`
		Name        *string `json:"Name"`
		Description *string `json:"Description"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	asset, ok := glueBusinessAssets.Get(req.Identifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Asset not found: "+req.Identifier)
		return
	}
	if req.Name != nil {
		asset.Name = *req.Name
	}
	if req.Description != nil {
		asset.Description = *req.Description
	}
	asset.UpdatedAt = glueEpochNow()
	glueBusinessAssets.Put(asset.Id, asset)
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"Id": asset.Id, "Name": asset.Name, "Description": asset.Description, "UpdatedAt": asset.UpdatedAt,
	})
}

func handleGlueDeleteAsset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"Identifier"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	if _, ok := glueBusinessAssets.Get(req.Identifier); !ok {
		glueWriteError(w, "EntityNotFoundException", "Asset not found: "+req.Identifier)
		return
	}
	glueBusinessAssets.Delete(req.Identifier)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueAssociateGlossaryTerms(w http.ResponseWriter, r *http.Request) {
	glueUpdateAssetGlossaryTerms(w, r, true)
}

func handleGlueDisassociateGlossaryTerms(w http.ResponseWriter, r *http.Request) {
	glueUpdateAssetGlossaryTerms(w, r, false)
}

func glueUpdateAssetGlossaryTerms(w http.ResponseWriter, r *http.Request, associate bool) {
	var req struct {
		AssetIdentifier         string   `json:"AssetIdentifier"`
		IterableFormName        string   `json:"IterableFormName"`
		ItemIdentifier          string   `json:"ItemIdentifier"`
		GlossaryTermIdentifiers []string `json:"GlossaryTermIdentifiers"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	if req.AssetIdentifier == "" || len(req.GlossaryTermIdentifiers) == 0 {
		glueWriteError(w, "InvalidInputException", "AssetIdentifier and GlossaryTermIdentifiers are required")
		return
	}
	// "The identifier of the item within the iterable form. Required when
	// iterableFormName is specified."
	if (req.IterableFormName == "") != (req.ItemIdentifier == "") {
		glueWriteError(w, "InvalidInputException", "IterableFormName and ItemIdentifier must be specified together")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	asset, ok := glueBusinessAssets.Get(req.AssetIdentifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Asset not found: "+req.AssetIdentifier)
		return
	}
	selected := make(map[string]bool, len(req.GlossaryTermIdentifiers))
	for _, identifier := range req.GlossaryTermIdentifiers {
		term, ok := glueResolveGlossaryTerm(identifier)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Glossary term not found: "+identifier)
			return
		}
		selected[term.Id] = true
	}
	response := map[string]any{"AssetIdentifier": asset.Id}
	if req.IterableFormName == "" {
		asset.GlossaryTerms = glueApplyGlossaryTerms(asset.GlossaryTerms, selected, associate)
		response["GlossaryTerms"] = asset.GlossaryTerms
	} else {
		if _, ok := glueAssetIterableForms(asset)[req.IterableFormName]; !ok {
			glueWriteError(w, "EntityNotFoundException", "Iterable form not found: "+req.IterableFormName)
			return
		}
		item, ok := glueStoredIterableItem(&asset, req.IterableFormName, req.ItemIdentifier)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Iterable form item not found: "+req.ItemIdentifier)
			return
		}
		item.GlossaryTerms = glueApplyGlossaryTerms(item.GlossaryTerms, selected, associate)
		glueStoreIterableItemAnnotations(&asset, req.IterableFormName, item)
		response["IterableFormName"] = req.IterableFormName
		response["ItemIdentifier"] = req.ItemIdentifier
		response["GlossaryTerms"] = item.GlossaryTerms
	}
	asset.UpdatedAt = glueEpochNow()
	glueBusinessAssets.Put(asset.Id, asset)
	glueWriteJSON(w, http.StatusOK, response)
}

func glueApplyGlossaryTerms(current []string, selected map[string]bool, associate bool) []string {
	if !associate {
		return removeGlueBusinessStrings(current, selected)
	}
	for termID := range selected {
		if !containsGlueBusinessString(current, termID) {
			current = append(current, termID)
		}
	}
	sort.Strings(current)
	return current
}

func containsGlueBusinessString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func removeGlueBusinessStrings(values []string, remove map[string]bool) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !remove[value] {
			out = append(out, value)
		}
	}
	return out
}

func handleGluePutAttachment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssetIdentifier  string `json:"AssetIdentifier"`
		AttachmentName   string `json:"AttachmentName"`
		FormTypeId       string `json:"FormTypeId"`
		Content          string `json:"Content"`
		IterableFormName string `json:"IterableFormName"`
		ItemIdentifier   string `json:"ItemIdentifier"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	if req.AssetIdentifier == "" || req.AttachmentName == "" || req.FormTypeId == "" || req.Content == "" {
		glueWriteError(w, "InvalidInputException", "AssetIdentifier, AttachmentName, FormTypeId, and Content are required")
		return
	}
	if !json.Valid([]byte(req.Content)) {
		glueWriteError(w, "InvalidInputException", "Content is not valid JSON")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	asset, ok := glueBusinessAssets.Get(req.AssetIdentifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Asset not found: "+req.AssetIdentifier)
		return
	}
	if _, ok := glueResolveFormType(req.FormTypeId); !ok {
		glueWriteError(w, "EntityNotFoundException", "Form type not found: "+req.FormTypeId)
		return
	}
	entry := GlueBusinessAssetFormEntry{FormTypeId: req.FormTypeId, Content: req.Content}
	if req.IterableFormName == "" && req.ItemIdentifier == "" {
		if asset.Attachments == nil {
			asset.Attachments = map[string]GlueBusinessAssetFormEntry{}
		}
		asset.Attachments[req.AttachmentName] = entry
	} else {
		if req.IterableFormName == "" || req.ItemIdentifier == "" {
			glueWriteError(w, "InvalidInputException", "IterableFormName and ItemIdentifier must be specified together")
			return
		}
		if _, ok := glueAssetIterableForms(asset)[req.IterableFormName]; !ok {
			glueWriteError(w, "EntityNotFoundException", "Iterable form not found: "+req.IterableFormName)
			return
		}
		item, ok := glueStoredIterableItem(&asset, req.IterableFormName, req.ItemIdentifier)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Iterable form item not found: "+req.ItemIdentifier)
			return
		}
		if item.Attachments == nil {
			item.Attachments = map[string]GlueBusinessAssetFormEntry{}
		}
		item.Attachments[req.AttachmentName] = entry
		glueStoreIterableItemAnnotations(&asset, req.IterableFormName, item)
	}
	asset.UpdatedAt = glueEpochNow()
	glueBusinessAssets.Put(asset.Id, asset)
	// An attachment made against the asset rather than an iterable form names
	// no form, and the model constrains IterableFormName to a real identifier —
	// so the member is absent rather than empty.
	response := map[string]any{
		"AssetIdentifier": asset.Id, "AttachmentName": req.AttachmentName, "FormTypeId": req.FormTypeId,
	}
	if req.IterableFormName != "" {
		response["IterableFormName"] = req.IterableFormName
	}
	if req.ItemIdentifier != "" {
		response["ItemIdentifier"] = req.ItemIdentifier
	}
	glueWriteJSON(w, http.StatusOK, response)
}

func handleGlueDeleteAttachment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssetIdentifier  string `json:"AssetIdentifier"`
		AttachmentName   string `json:"AttachmentName"`
		IterableFormName string `json:"IterableFormName"`
		ItemIdentifier   string `json:"ItemIdentifier"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	asset, ok := glueBusinessAssets.Get(req.AssetIdentifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Asset not found: "+req.AssetIdentifier)
		return
	}
	if req.IterableFormName == "" && req.ItemIdentifier == "" {
		if _, ok := asset.Attachments[req.AttachmentName]; !ok {
			glueWriteError(w, "EntityNotFoundException", "Attachment not found: "+req.AttachmentName)
			return
		}
		delete(asset.Attachments, req.AttachmentName)
	} else {
		if _, ok := glueAssetIterableForms(asset)[req.IterableFormName]; !ok {
			glueWriteError(w, "EntityNotFoundException", "Iterable form not found: "+req.IterableFormName)
			return
		}
		item, ok := glueStoredIterableItem(&asset, req.IterableFormName, req.ItemIdentifier)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Iterable form item not found: "+req.ItemIdentifier)
			return
		}
		if _, ok := item.Attachments[req.AttachmentName]; !ok {
			glueWriteError(w, "EntityNotFoundException", "Attachment not found: "+req.AttachmentName)
			return
		}
		delete(item.Attachments, req.AttachmentName)
		glueStoreIterableItemAnnotations(&asset, req.IterableFormName, item)
	}
	asset.UpdatedAt = glueEpochNow()
	glueBusinessAssets.Put(asset.Id, asset)
	// The two item members are present only when the deletion targeted an
	// iterable form item, which is what "if applicable" means on them.
	response := map[string]any{"AssetIdentifier": asset.Id}
	if req.IterableFormName != "" {
		response["IterableFormName"] = req.IterableFormName
	}
	if req.ItemIdentifier != "" {
		response["ItemIdentifier"] = req.ItemIdentifier
	}
	glueWriteJSON(w, http.StatusOK, response)
}

func handleGlueListIterableForms(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssetIdentifier  string `json:"AssetIdentifier"`
		IterableFormName string `json:"IterableFormName"`
		MaxResults       *int   `json:"MaxResults"`
		NextToken        string `json:"NextToken"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	asset, ok := glueBusinessAssets.Get(req.AssetIdentifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Asset not found: "+req.AssetIdentifier)
		return
	}
	form, ok := glueAssetIterableForms(asset)[req.IterableFormName]
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Iterable form not found: "+req.IterableFormName)
		return
	}
	items := make([]map[string]any, 0, len(form.Items))
	for _, item := range form.Items {
		items = append(items, map[string]any{
			"ItemId": item.ItemId, "ItemName": item.ItemName,
			"Description": item.Description, "GlossaryTerms": item.GlossaryTerms,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return glueBusinessStringField(items[i], "ItemName") < glueBusinessStringField(items[j], "ItemName")
	})
	page, next := awsPage(items, req.NextToken, derefIntDefault(req.MaxResults, 0), 1000)
	response := map[string]any{"Items": page}
	if next != "" {
		response["NextToken"] = next
	}
	glueWriteJSON(w, http.StatusOK, response)
}

func handleGlueBatchGetIterableForms(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssetIdentifier  string   `json:"AssetIdentifier"`
		IterableFormName string   `json:"IterableFormName"`
		ItemIdentifiers  []string `json:"ItemIdentifiers"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	asset, ok := glueBusinessAssets.Get(req.AssetIdentifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Asset not found: "+req.AssetIdentifier)
		return
	}
	form, ok := glueAssetIterableForms(asset)[req.IterableFormName]
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Iterable form not found: "+req.IterableFormName)
		return
	}
	// A batch-get answers with IterableFormItem members — the item's forms,
	// attachments and glossary terms. The description belongs to the list
	// item, which is a different shape, so it is not reported here.
	items := make([]map[string]any, 0, len(req.ItemIdentifiers))
	errors := make([]map[string]any, 0)
	for _, identifier := range req.ItemIdentifiers {
		item, ok := form.Items[identifier]
		if !ok {
			for _, candidate := range form.Items {
				if candidate.ItemName == identifier {
					item, ok = candidate, true
					break
				}
			}
		}
		if ok {
			entry := map[string]any{"ItemId": item.ItemId, "ItemName": item.ItemName}
			if len(item.GlossaryTerms) > 0 {
				entry["GlossaryTerms"] = item.GlossaryTerms
			}
			if len(item.Forms) > 0 {
				entry["Forms"] = item.Forms
			}
			if len(item.Attachments) > 0 {
				entry["Attachments"] = item.Attachments
			}
			items = append(items, entry)
		} else {
			errors = append(errors, map[string]any{
				"ItemIdentifier": identifier, "Code": "EntityNotFoundException",
				"Message": "Iterable form item not found: " + identifier,
			})
		}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Items": items, "Errors": errors})
}

func handleGlueSearchAssets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SearchText   string         `json:"SearchText"`
		FilterClause map[string]any `json:"FilterClause"`
		Sort         struct {
			Attribute string `json:"Attribute"`
			Order     string `json:"Order"`
		} `json:"Sort"`
		MaxResults *int   `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	if req.SearchText == "" && len(req.FilterClause) == 0 {
		glueWriteError(w, "InvalidInputException", "SearchText or FilterClause is required")
		return
	}
	needle := strings.ToLower(req.SearchText)
	matches := make([]GlueBusinessAsset, 0)
	for _, asset := range glueBusinessAssets.List() {
		if needle != "" && !strings.Contains(strings.ToLower(asset.Id+"\n"+asset.Name+"\n"+asset.Description), needle) {
			continue
		}
		if len(req.FilterClause) > 0 && !glueBusinessAssetMatchesFilter(asset, req.FilterClause) {
			continue
		}
		matches = append(matches, asset)
	}
	sort.Slice(matches, func(i, j int) bool {
		var left, right string
		switch strings.ToLower(req.Sort.Attribute) {
		case "id":
			left, right = matches[i].Id, matches[j].Id
		case "assettypeid":
			left, right = matches[i].AssetTypeId, matches[j].AssetTypeId
		case "updatedat":
			left, right = fmt.Sprintf("%020.6f", matches[i].UpdatedAt), fmt.Sprintf("%020.6f", matches[j].UpdatedAt)
		default:
			left, right = matches[i].Name, matches[j].Name
		}
		if req.Sort.Order == "DESCENDING" {
			return left > right
		}
		return left < right
	})
	items := make([]map[string]any, 0, len(matches))
	for _, asset := range matches {
		items = append(items, map[string]any{
			"Id": asset.Id, "AssetName": asset.Name, "AssetDescription": asset.Description,
			"AssetTypeId": asset.AssetTypeId, "UpdatedAt": asset.UpdatedAt,
		})
	}
	page, next := awsPage(items, req.NextToken, derefIntDefault(req.MaxResults, 0), 1000)
	response := map[string]any{"Items": page}
	if next != "" {
		response["NextToken"] = next
	}
	glueWriteJSON(w, http.StatusOK, response)
}

func glueBusinessAssetMatchesFilter(asset GlueBusinessAsset, clause map[string]any) bool {
	if raw, ok := clause["AndAllFilters"].([]any); ok {
		for _, child := range raw {
			childMap, ok := child.(map[string]any)
			if !ok || !glueBusinessAssetMatchesFilter(asset, childMap) {
				return false
			}
		}
		return true
	}
	if raw, ok := clause["OrAnyFilters"].([]any); ok {
		for _, child := range raw {
			if childMap, ok := child.(map[string]any); ok && glueBusinessAssetMatchesFilter(asset, childMap) {
				return true
			}
		}
		return false
	}
	filter, ok := clause["AttributeFilter"].(map[string]any)
	if !ok {
		return false
	}
	attribute, _ := filter["Attribute"].(string)
	operator, _ := filter["Operator"].(string)
	valueMap, _ := filter["Value"].(map[string]any)
	expected := fmt.Sprint(valueMap["StringValue"])
	if longValue, ok := valueMap["LongValue"].(float64); ok {
		expected = strconv.FormatInt(int64(longValue), 10)
	}
	actual := ""
	switch strings.ToLower(attribute) {
	case "id":
		actual = asset.Id
	case "name", "assetname":
		actual = asset.Name
	case "description", "assetdescription":
		actual = asset.Description
	case "assettypeid":
		actual = asset.AssetTypeId
	case "updatedat":
		actual = strconv.FormatInt(int64(asset.UpdatedAt), 10)
	}
	switch operator {
	case "equals":
		return actual == expected
	case "greaterThan":
		return actual > expected
	case "greaterThanOrEquals":
		return actual >= expected
	case "lessThan":
		return actual < expected
	case "lessThanOrEquals":
		return actual <= expected
	case "notExists":
		return actual == ""
	default:
		return false
	}
}

func handleGlueBatchGetDataQualityRulesetEvaluationRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunIds []string `json:"RunIds"`
	}
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	runs := make([]GlueDQRulesetEvaluationRun, 0, len(req.RunIds))
	notFound := make([]string, 0)
	for _, id := range req.RunIds {
		run, ok := glueDQEvalRuns.Get(id)
		if ok {
			runs = append(runs, run)
		} else {
			notFound = append(notFound, id)
		}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Runs": runs, "RunsNotFound": notFound})
}
