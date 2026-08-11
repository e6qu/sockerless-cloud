package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode"
)

// Container administration: metadata, the stored access policies (`comp=acl`),
// and Find Blobs by Tags (`comp=blobs`) at both the container and the service
// scope. The tag filter is really evaluated against the stored tag sets — the
// expression is parsed, not pattern-matched — so a query that selects nothing
// returns nothing rather than everything.

func handleSetContainerMetadata(w http.ResponseWriter, r *http.Request, account, container string) {
	key := blobContainerKey(account, container)
	c, ok := blobContainersData.Get(key)
	if !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	if !blobContainerWriteAllowed(w, r, c) {
		return
	}
	c.Metadata = collectMetadata(r)
	c.Created = blobNowHTTP()
	c.ETag = `"` + generateUUID() + `"`
	blobContainersData.Put(key, c)
	w.Header().Set("ETag", c.ETag)
	w.Header().Set("Last-Modified", c.Created)
	w.WriteHeader(http.StatusOK)
}

// blobSignedIdentifiersDocument is the <SignedIdentifiers> document the
// container ACL is exchanged as.
type blobSignedIdentifiersDocument struct {
	XMLName          xml.Name                  `xml:"SignedIdentifiers"`
	SignedIdentifier []blobSignedIdentifierXML `xml:"SignedIdentifier"`
}

type blobSignedIdentifierXML struct {
	ID           string              `xml:"Id"`
	AccessPolicy blobAccessPolicyXML `xml:"AccessPolicy"`
}

type blobAccessPolicyXML struct {
	Start      string `xml:"Start,omitempty"`
	Expiry     string `xml:"Expiry,omitempty"`
	Permission string `xml:"Permission,omitempty"`
}

func handleGetContainerAccessPolicy(w http.ResponseWriter, r *http.Request, account, container string) {
	c, ok := blobContainersData.Get(blobContainerKey(account, container))
	if !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	if !blobLeaseAccessOK(w, r, c.Lease, "container") {
		return
	}
	doc := blobSignedIdentifiersDocument{}
	for _, si := range c.SignedIdentifiers {
		doc.SignedIdentifier = append(doc.SignedIdentifier, blobSignedIdentifierXML{
			ID: si.ID,
			AccessPolicy: blobAccessPolicyXML{
				Start:      si.Start,
				Expiry:     si.Expiry,
				Permission: si.Permission,
			},
		})
	}
	if c.PublicAccess != "" {
		w.Header().Set("x-ms-blob-public-access", c.PublicAccess)
	}
	w.Header().Set("ETag", c.ETag)
	w.Header().Set("Last-Modified", c.Created)
	writeStorageXML(w, http.StatusOK, doc)
}

func handleSetContainerAccessPolicy(w http.ResponseWriter, r *http.Request, account, container string) {
	key := blobContainerKey(account, container)
	c, ok := blobContainersData.Get(key)
	if !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	if !blobContainerWriteAllowed(w, r, c) {
		return
	}
	defer r.Body.Close()
	var doc blobSignedIdentifiersDocument
	// An empty body clears the ACL, which is how a client removes every policy.
	if err := xml.NewDecoder(r.Body).Decode(&doc); err != nil && err.Error() != "EOF" {
		writeStorageError(w, "InvalidXmlDocument",
			"XML specified is not syntactically valid.", http.StatusBadRequest)
		return
	}
	identifiers := make([]BlobSignedIdentifier, 0, len(doc.SignedIdentifier))
	for _, si := range doc.SignedIdentifier {
		if si.ID == "" {
			writeStorageError(w, "InvalidXmlNodeValue",
				"The value for one of the XML nodes is not in the correct format: Id.",
				http.StatusBadRequest)
			return
		}
		identifiers = append(identifiers, BlobSignedIdentifier{
			ID:         si.ID,
			Start:      si.AccessPolicy.Start,
			Expiry:     si.AccessPolicy.Expiry,
			Permission: si.AccessPolicy.Permission,
		})
	}
	c.SignedIdentifiers = identifiers
	if access := r.Header.Get("x-ms-blob-public-access"); access != "" {
		c.PublicAccess = access
	}
	c.Created = blobNowHTTP()
	c.ETag = `"` + generateUUID() + `"`
	blobContainersData.Put(key, c)
	w.Header().Set("ETag", c.ETag)
	w.Header().Set("Last-Modified", c.Created)
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Find Blobs by Tags
// ---------------------------------------------------------------------------

type blobFilterSegment struct {
	XMLName         xml.Name         `xml:"EnumerationResults"`
	ServiceEndpoint string           `xml:"ServiceEndpoint,attr"`
	Where           string           `xml:"Where"`
	Blobs           []blobFilterItem `xml:"Blobs>Blob"`
	NextMarker      string           `xml:"NextMarker"`
}

type blobFilterItem struct {
	Name          string            `xml:"Name"`
	ContainerName string            `xml:"ContainerName"`
	Tags          *blobTagsDocument `xml:"Tags,omitempty"`
}

// handleFilterBlobs implements Find Blobs by Tags. With `container` empty it is
// the service-scope operation and searches every container of the account;
// otherwise it is the container-scope sibling.
func handleFilterBlobs(w http.ResponseWriter, r *http.Request, account, container string) {
	where := r.URL.Query().Get("where")
	if strings.TrimSpace(where) == "" {
		writeStorageError(w, "MissingRequiredQueryParameter",
			"A query parameter that's mandatory for this request is not specified: where.",
			http.StatusBadRequest)
		return
	}
	expr, err := parseBlobTagFilter(where)
	if err != nil {
		writeStorageError(w, "InvalidQueryParameterValue",
			fmt.Sprintf("Value for one of the query parameters specified in the request URI is invalid: where (%v).", err),
			http.StatusBadRequest)
		return
	}

	var containers []string
	if container != "" {
		if _, ok := blobContainersData.Get(blobContainerKey(account, container)); !ok {
			writeStorageError(w, "ContainerNotFound",
				"The specified container does not exist.", http.StatusNotFound)
			return
		}
		containers = []string{container}
	} else {
		for _, c := range blobContainersData.List() {
			if c.Account == account && !c.Deleted {
				containers = append(containers, c.Name)
			}
		}
		sort.Strings(containers)
	}

	var matches []blobFilterItem
	for _, name := range containers {
		for _, b := range blobsInContainer(account, name) {
			if b.Deleted || b.Snapshot != "" || len(b.Tags) == 0 {
				continue
			}
			if !expr.matches(b.Tags) {
				continue
			}
			matches = append(matches, blobFilterItem{
				Name:          b.Name,
				ContainerName: b.Container,
				Tags:          blobTagsDocumentFor(b.Tags),
			})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].ContainerName != matches[j].ContainerName {
			return matches[i].ContainerName < matches[j].ContainerName
		}
		return matches[i].Name < matches[j].Name
	})

	page, marker := blobStoragePage(r, matches, func(e blobFilterItem) string { return e.Name })
	writeStorageXML(w, http.StatusOK, blobFilterSegment{
		ServiceEndpoint: azureStorageEndpointURL(r, account, "blob"),
		Where:           where,
		Blobs:           page,
		NextMarker:      marker,
	})
}

// ---------------------------------------------------------------------------
// Tag filter expression
// ---------------------------------------------------------------------------

// blobTagFilter is a parsed `where=` expression: a conjunction of comparisons
// between a tag name (bare or quoted with "…") and a string literal ('…').
// Azure's tag query grammar supports =, >, >=, < and <= over string values
// joined by AND, and that is exactly what is evaluated here.
type blobTagFilter struct {
	terms []blobTagTerm
}

type blobTagTerm struct {
	key   string
	op    string
	value string
}

func (f blobTagFilter) matches(tags map[string]string) bool {
	for _, t := range f.terms {
		got, ok := tags[t.key]
		if !ok {
			return false
		}
		switch t.op {
		case "=":
			if got != t.value {
				return false
			}
		case ">":
			if got <= t.value {
				return false
			}
		case ">=":
			if got < t.value {
				return false
			}
		case "<":
			if got >= t.value {
				return false
			}
		case "<=":
			if got > t.value {
				return false
			}
		default:
			return false
		}
	}
	return len(f.terms) > 0
}

// parseBlobTagFilter parses the `where=` grammar. Anything outside it — OR, a
// parenthesised group, a comparison between two tags — is rejected loudly rather
// than silently matching the wrong blobs.
func parseBlobTagFilter(where string) (blobTagFilter, error) {
	var filter blobTagFilter
	rest := where
	for {
		term, remainder, err := parseBlobTagTerm(rest)
		if err != nil {
			return blobTagFilter{}, err
		}
		filter.terms = append(filter.terms, term)
		remainder = strings.TrimSpace(remainder)
		if remainder == "" {
			return filter, nil
		}
		upper := strings.ToUpper(remainder)
		if !strings.HasPrefix(upper, "AND ") && upper != "AND" {
			return blobTagFilter{}, fmt.Errorf("expected AND at %q", remainder)
		}
		rest = remainder[len("AND"):]
	}
}

func parseBlobTagTerm(in string) (blobTagTerm, string, error) {
	s := strings.TrimSpace(in)
	key, s, err := parseBlobTagOperand(s)
	if err != nil {
		return blobTagTerm{}, "", err
	}
	s = strings.TrimSpace(s)
	var op string
	switch {
	case strings.HasPrefix(s, ">="), strings.HasPrefix(s, "<="):
		op, s = s[:2], s[2:]
	case strings.HasPrefix(s, "="), strings.HasPrefix(s, ">"), strings.HasPrefix(s, "<"):
		op, s = s[:1], s[1:]
	default:
		return blobTagTerm{}, "", fmt.Errorf("expected a comparison operator at %q", s)
	}
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "'") {
		return blobTagTerm{}, "", fmt.Errorf("expected a quoted value at %q", s)
	}
	end := strings.Index(s[1:], "'")
	if end < 0 {
		return blobTagTerm{}, "", fmt.Errorf("unterminated value literal at %q", s)
	}
	value := s[1 : 1+end]
	return blobTagTerm{key: key, op: op, value: value}, s[end+2:], nil
}

// parseBlobTagOperand reads a tag name, either bare or in double quotes.
func parseBlobTagOperand(s string) (string, string, error) {
	if strings.HasPrefix(s, `"`) {
		end := strings.Index(s[1:], `"`)
		if end < 0 {
			return "", "", fmt.Errorf("unterminated tag name at %q", s)
		}
		return s[1 : 1+end], s[end+2:], nil
	}
	i := 0
	for i < len(s) {
		r := rune(s[i])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '-' || r == '+' || r == '/' || r == ':' {
			i++
			continue
		}
		break
	}
	if i == 0 {
		return "", "", fmt.Errorf("expected a tag name at %q", s)
	}
	// @container is Azure's pseudo-tag for the container name; the container
	// scope is already the request coordinate, so a query naming it is outside
	// the grammar this evaluates rather than silently ignored.
	if strings.EqualFold(s[:i], "@container") {
		return "", "", fmt.Errorf("@container is not a supported filter operand")
	}
	return s[:i], s[i:], nil
}
