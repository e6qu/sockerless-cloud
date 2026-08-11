package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// SSM Documents — a versioned named document (Command / Automation /
// Session / etc. content). terraform's `aws_ssm_document` resource and
// `aws ssm create-document` / `get-document` hit this slice; runners
// that pin a Command document for Run Command flows depend on it. The
// sim stores the full version history so GetDocument / ListDocumentVersions
// / UpdateDocumentDefaultVersion behave like real SSM.

// SSMDocumentVersion is one immutable revision of a document's content.
type SSMDocumentVersion struct {
	DocumentVersion string  `json:"DocumentVersion"`
	VersionName     string  `json:"VersionName,omitempty"`
	Content         string  `json:"Content"`
	DocumentFormat  string  `json:"DocumentFormat"`
	CreatedDate     float64 `json:"CreatedDate"`
	Status          string  `json:"Status"`
	StatusInfo      string  `json:"StatusInformation,omitempty"`
	ReviewStatus    string  `json:"ReviewStatus"`
}

// SSMDocument is the mutable document envelope plus its version history.
type SSMDocument struct {
	Name           string               `json:"Name"`
	DisplayName    string               `json:"DisplayName,omitempty"`
	DocumentType   string               `json:"DocumentType"`
	DocumentFormat string               `json:"DocumentFormat"`
	SchemaVersion  string               `json:"SchemaVersion"`
	TargetType     string               `json:"TargetType,omitempty"`
	Owner          string               `json:"Owner"`
	CreatedDate    float64              `json:"CreatedDate"`
	DefaultVersion string               `json:"DefaultVersion"`
	LatestVersion  string               `json:"LatestVersion"`
	Versions       []SSMDocumentVersion `json:"Versions"`
}

var ssmDocuments sim.Store[SSMDocument]

func registerSSMDocuments(r *sim.AWSRouter, srv *sim.Server) {
	ssmDocuments = sim.MakeStore[SSMDocument](srv.DB(), "ssm_documents")

	r.Register("AmazonSSM.CreateDocument", handleSSMCreateDocument)
	r.Register("AmazonSSM.DeleteDocument", handleSSMDeleteDocument)
	r.Register("AmazonSSM.DescribeDocument", handleSSMDescribeDocument)
	r.Register("AmazonSSM.GetDocument", handleSSMGetDocument)
	r.Register("AmazonSSM.ListDocuments", handleSSMListDocuments)
	r.Register("AmazonSSM.ListDocumentVersions", handleSSMListDocumentVersions)
	r.Register("AmazonSSM.UpdateDocument", handleSSMUpdateDocument)
	r.Register("AmazonSSM.UpdateDocumentDefaultVersion", handleSSMUpdateDocumentDefaultVersion)
}

func ssmDocHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func ssmDocVersion(d SSMDocument, version string) (SSMDocumentVersion, bool) {
	if version == "" || version == "$DEFAULT" {
		version = d.DefaultVersion
	} else if version == "$LATEST" {
		version = d.LatestVersion
	}
	for _, v := range d.Versions {
		if v.DocumentVersion == version {
			return v, true
		}
	}
	return SSMDocumentVersion{}, false
}

// ssmOmitEmptyStrings drops members whose value is the empty string. AWS Systems
// Manager omits an optional string it has no value for, and several of these are
// pattern-constrained — a document with no version name has no VersionName
// member, where an empty one is a value the model forbids and a client cannot
// send back.
func ssmOmitEmptyStrings(m map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && v == "" {
			delete(m, key)
		}
	}
	return m
}

// ssmDocumentDescriptionWire projects the DocumentDescription shape
// (CreateDocument / DescribeDocument / UpdateDocument responses).
func ssmDocumentDescriptionWire(d SSMDocument, v SSMDocumentVersion) map[string]any {
	return ssmOmitEmptyStrings(map[string]any{
		"Name":            d.Name,
		"DisplayName":     d.DisplayName,
		"DocumentType":    d.DocumentType,
		"DocumentFormat":  v.DocumentFormat,
		"DocumentVersion": v.DocumentVersion,
		"VersionName":     v.VersionName,
		"SchemaVersion":   d.SchemaVersion,
		"TargetType":      d.TargetType,
		"Owner":           d.Owner,
		"CreatedDate":     d.CreatedDate,
		"DefaultVersion":  d.DefaultVersion,
		"LatestVersion":   d.LatestVersion,
		"Status":          v.Status,
		"ReviewStatus":    v.ReviewStatus,
		"Hash":            ssmDocHash(v.Content),
		"HashType":        "Sha256",
		"PlatformTypes":   []string{"Linux", "Windows", "MacOS"},
	}, "VersionName", "TargetType", "DisplayName")
}

func handleSSMCreateDocument(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name           string `json:"Name"`
		Content        string `json:"Content"`
		DisplayName    string `json:"DisplayName"`
		DocumentType   string `json:"DocumentType"`
		DocumentFormat string `json:"DocumentFormat"`
		TargetType     string `json:"TargetType"`
		VersionName    string `json:"VersionName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Content == "" {
		sim.AWSError(w, "ValidationException", "Name and Content are required", http.StatusBadRequest)
		return
	}
	if _, exists := ssmDocuments.Get(req.Name); exists {
		sim.AWSErrorf(w, "DocumentAlreadyExists", http.StatusBadRequest,
			"The specified document already exists.")
		return
	}
	if req.DocumentType == "" {
		req.DocumentType = "Command"
	}
	if req.DocumentFormat == "" {
		req.DocumentFormat = "JSON"
	}
	now := float64(time.Now().Unix())
	ver := SSMDocumentVersion{
		DocumentVersion: "1",
		VersionName:     req.VersionName,
		Content:         req.Content,
		DocumentFormat:  req.DocumentFormat,
		CreatedDate:     now,
		Status:          "Active",
		ReviewStatus:    "NOT_REVIEWED",
	}
	doc := SSMDocument{
		Name:           req.Name,
		DisplayName:    req.DisplayName,
		DocumentType:   req.DocumentType,
		DocumentFormat: req.DocumentFormat,
		SchemaVersion:  "2.2",
		TargetType:     req.TargetType,
		Owner:          awsAccountID(),
		CreatedDate:    now,
		DefaultVersion: "1",
		LatestVersion:  "1",
		Versions:       []SSMDocumentVersion{ver},
	}
	ssmDocuments.Put(req.Name, doc)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"DocumentDescription": ssmDocumentDescriptionWire(doc, ver),
	})
}

func handleSSMDeleteDocument(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmDocuments.Get(req.Name); !ok {
		sim.AWSErrorf(w, "InvalidDocument", http.StatusBadRequest,
			"The specified SSM document doesn't exist.")
		return
	}
	ssmDocuments.Delete(req.Name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMDescribeDocument(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"Name"`
		DocumentVersion string `json:"DocumentVersion"`
		VersionName     string `json:"VersionName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	doc, ok := ssmDocuments.Get(req.Name)
	if !ok {
		sim.AWSErrorf(w, "InvalidDocument", http.StatusBadRequest,
			"The specified SSM document doesn't exist.")
		return
	}
	ver, ok := ssmDocVersion(doc, req.DocumentVersion)
	if !ok {
		sim.AWSErrorf(w, "InvalidDocumentVersion", http.StatusBadRequest,
			"The document version isn't valid or doesn't exist.")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Document": ssmDocumentDescriptionWire(doc, ver),
	})
}

func handleSSMGetDocument(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"Name"`
		DocumentVersion string `json:"DocumentVersion"`
		VersionName     string `json:"VersionName"`
		DocumentFormat  string `json:"DocumentFormat"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	doc, ok := ssmDocuments.Get(req.Name)
	if !ok {
		sim.AWSErrorf(w, "InvalidDocument", http.StatusBadRequest,
			"The specified SSM document doesn't exist.")
		return
	}
	ver, ok := ssmDocVersion(doc, req.DocumentVersion)
	if !ok {
		sim.AWSErrorf(w, "InvalidDocumentVersion", http.StatusBadRequest,
			"The document version isn't valid or doesn't exist.")
		return
	}
	format := ver.DocumentFormat
	if req.DocumentFormat != "" {
		format = req.DocumentFormat
	}
	sim.WriteJSON(w, http.StatusOK, ssmOmitEmptyStrings(map[string]any{
		"Name":            doc.Name,
		"DisplayName":     doc.DisplayName,
		"DocumentType":    doc.DocumentType,
		"DocumentFormat":  format,
		"DocumentVersion": ver.DocumentVersion,
		"VersionName":     ver.VersionName,
		"Content":         ver.Content,
		"CreatedDate":     ver.CreatedDate,
		"Status":          ver.Status,
		"ReviewStatus":    ver.ReviewStatus,
	}, "VersionName", "DisplayName"))
}

func handleSSMListDocuments(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmDocuments.List()
	if all == nil {
		all = []SSMDocument{}
	}
	sortBy(all, func(d SSMDocument) string { return d.Name })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, d := range page {
		def, _ := ssmDocVersion(d, "$DEFAULT")
		out = append(out, ssmOmitEmptyStrings(map[string]any{
			"Name":            d.Name,
			"DisplayName":     d.DisplayName,
			"DocumentType":    d.DocumentType,
			"DocumentFormat":  d.DocumentFormat,
			"DocumentVersion": def.DocumentVersion,
			"VersionName":     def.VersionName,
			"SchemaVersion":   d.SchemaVersion,
			"TargetType":      d.TargetType,
			"Owner":           d.Owner,
			"CreatedDate":     d.CreatedDate,
			"ReviewStatus":    def.ReviewStatus,
			"PlatformTypes":   []string{"Linux", "Windows", "MacOS"},
		}, "VersionName", "TargetType", "DisplayName"))
	}
	resp := map[string]any{"DocumentIdentifiers": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMListDocumentVersions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"Name"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	doc, ok := ssmDocuments.Get(req.Name)
	if !ok {
		sim.AWSErrorf(w, "InvalidDocument", http.StatusBadRequest,
			"The specified SSM document doesn't exist.")
		return
	}
	all := make([]SSMDocumentVersion, len(doc.Versions))
	copy(all, doc.Versions)
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, v := range page {
		out = append(out, ssmOmitEmptyStrings(map[string]any{
			"Name":             doc.Name,
			"DisplayName":      doc.DisplayName,
			"DocumentVersion":  v.DocumentVersion,
			"VersionName":      v.VersionName,
			"DocumentFormat":   v.DocumentFormat,
			"CreatedDate":      v.CreatedDate,
			"IsDefaultVersion": v.DocumentVersion == doc.DefaultVersion,
			"Status":           v.Status,
			"ReviewStatus":     v.ReviewStatus,
		}, "VersionName", "DisplayName"))
	}
	resp := map[string]any{"DocumentVersions": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMUpdateDocument(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"Name"`
		Content         string `json:"Content"`
		DisplayName     string `json:"DisplayName"`
		DocumentFormat  string `json:"DocumentFormat"`
		DocumentVersion string `json:"DocumentVersion"`
		TargetType      string `json:"TargetType"`
		VersionName     string `json:"VersionName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	doc, ok := ssmDocuments.Get(req.Name)
	if !ok {
		sim.AWSErrorf(w, "InvalidDocument", http.StatusBadRequest,
			"The specified SSM document doesn't exist.")
		return
	}
	if req.Content == "" {
		sim.AWSError(w, "ValidationException", "Content is required", http.StatusBadRequest)
		return
	}
	// Real SSM rejects an update whose content is identical to the
	// latest version (DuplicateDocumentContent).
	latest, _ := ssmDocVersion(doc, "$LATEST")
	if latest.Content == req.Content {
		sim.AWSErrorf(w, "DuplicateDocumentContent", http.StatusBadRequest,
			"The content of the association document matches another document. Change the content of the document and try again.")
		return
	}
	format := doc.DocumentFormat
	if req.DocumentFormat != "" {
		format = req.DocumentFormat
	}
	newVersion := nextSSMDocVersion(doc)
	now := float64(time.Now().Unix())
	ver := SSMDocumentVersion{
		DocumentVersion: newVersion,
		VersionName:     req.VersionName,
		Content:         req.Content,
		DocumentFormat:  format,
		CreatedDate:     now,
		Status:          "Active",
		ReviewStatus:    "NOT_REVIEWED",
	}
	doc.Versions = append(doc.Versions, ver)
	doc.LatestVersion = newVersion
	doc.DocumentFormat = format
	if req.DisplayName != "" {
		doc.DisplayName = req.DisplayName
	}
	if req.TargetType != "" {
		doc.TargetType = req.TargetType
	}
	ssmDocuments.Put(doc.Name, doc)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"DocumentDescription": ssmDocumentDescriptionWire(doc, ver),
	})
}

func nextSSMDocVersion(d SSMDocument) string {
	max := 0
	for _, v := range d.Versions {
		n := 0
		for _, c := range v.DocumentVersion {
			if c < '0' || c > '9' {
				n = 0
				break
			}
			n = n*10 + int(c-'0')
		}
		if n > max {
			max = n
		}
	}
	return itoaSSM(max + 1)
}

func itoaSSM(n int) string {
	if n == 0 {
		return "0"
	}
	var b strings.Builder
	digits := []byte{}
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}
	for i := len(digits) - 1; i >= 0; i-- {
		b.WriteByte(digits[i])
	}
	return b.String()
}

func handleSSMUpdateDocumentDefaultVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"Name"`
		DocumentVersion string `json:"DocumentVersion"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	doc, ok := ssmDocuments.Get(req.Name)
	if !ok {
		sim.AWSErrorf(w, "InvalidDocument", http.StatusBadRequest,
			"The specified SSM document doesn't exist.")
		return
	}
	ver, ok := ssmDocVersion(doc, req.DocumentVersion)
	if !ok {
		sim.AWSErrorf(w, "InvalidDocumentVersion", http.StatusBadRequest,
			"The document version isn't valid or doesn't exist.")
		return
	}
	doc.DefaultVersion = ver.DocumentVersion
	ssmDocuments.Put(doc.Name, doc)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Description": ssmOmitEmptyStrings(map[string]any{
			"Name":               doc.Name,
			"DefaultVersion":     ver.DocumentVersion,
			"DefaultVersionName": ver.VersionName,
		}, "DefaultVersionName"),
	})
}
