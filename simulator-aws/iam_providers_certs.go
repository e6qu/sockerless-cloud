package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// IAM SAML providers, server certificates, account aliases, and the
// OpenID Connect provider tag-list op. Extends iam.go.
//
// Same AWS Query Protocol as the rest of IAM (POST / + Action=<Op>,
// form-encoded body, XML response, namespace
// https://iam.amazonaws.com/doc/2010-05-08/).
//
// All four families are real resources with real ARNs:
//   - SAML provider:   arn:aws:iam::<acct>:saml-provider/<Name>
//   - server cert:     arn:aws:iam::<acct>:server-certificate<Path><Name>
//   - account alias:   account-level singleton list (≤ 1 alias per account)
//   - OIDC tags:       tag list over the existing iamOIDCProviders store

// IAMSAMLProvider is a Security Assertion Markup Language (SAML) identity
// provider. The submitted metadata document is stored and echoed verbatim.
type IAMSAMLProvider struct {
	Arn                  string   `json:"arn"`
	Name                 string   `json:"name"`
	SAMLMetadataDocument string   `json:"samlMetadataDocument"`
	UUID                 string   `json:"uuid"`
	CreateDate           string   `json:"createDate"`
	ValidUntil           string   `json:"validUntil"`
	Tags                 []IAMTag `json:"tags,omitempty"`
}

// IAMServerCertificate is an uploaded X.509 server certificate. The
// certificate body, chain, and private key are stored and echoed verbatim
// (GetServerCertificate returns body + chain; the private key is never
// echoed, matching real IAM).
type IAMServerCertificate struct {
	Name             string   `json:"name"`
	Id               string   `json:"id"`
	Arn              string   `json:"arn"`
	Path             string   `json:"path"`
	CertificateBody  string   `json:"certificateBody"`
	CertificateChain string   `json:"certificateChain"`
	PrivateKey       string   `json:"privateKey"`
	UploadDate       string   `json:"uploadDate"`
	Expiration       string   `json:"expiration"`
	Tags             []IAMTag `json:"tags,omitempty"`
}

var (
	iamSAMLProviders  sim.Store[IAMSAMLProvider]
	iamServerCerts    sim.Store[IAMServerCertificate]
	iamAccountAliases sim.Store[string] // account-level alias singleton (key == alias)
)

func registerIAMProvidersCerts(r *AWSQueryRouter, srv *sim.Server) {
	iamSAMLProviders = sim.MakeStore[IAMSAMLProvider](srv.DB(), "iam_saml_providers")
	iamServerCerts = sim.MakeStore[IAMServerCertificate](srv.DB(), "iam_server_certificates")
	iamAccountAliases = sim.MakeStore[string](srv.DB(), "iam_account_aliases")

	for action, h := range map[string]http.HandlerFunc{
		// SAML providers
		"CreateSAMLProvider":   handleIAMCreateSAMLProvider,
		"GetSAMLProvider":      handleIAMGetSAMLProvider,
		"UpdateSAMLProvider":   handleIAMUpdateSAMLProvider,
		"DeleteSAMLProvider":   handleIAMDeleteSAMLProvider,
		"ListSAMLProviders":    handleIAMListSAMLProviders,
		"ListSAMLProviderTags": handleIAMListSAMLProviderTags,
		"TagSAMLProvider":      handleIAMTagSAMLProvider,
		"UntagSAMLProvider":    handleIAMUntagSAMLProvider,
		// OIDC provider tags (the providers themselves live in iam_slr_oidc.go)
		"ListOpenIDConnectProviderTags": handleIAMListOIDCProviderTags,
		// Server certificates
		"UploadServerCertificate":   handleIAMUploadServerCertificate,
		"GetServerCertificate":      handleIAMGetServerCertificate,
		"UpdateServerCertificate":   handleIAMUpdateServerCertificate,
		"DeleteServerCertificate":   handleIAMDeleteServerCertificate,
		"ListServerCertificates":    handleIAMListServerCertificates,
		"ListServerCertificateTags": handleIAMListServerCertificateTags,
		"TagServerCertificate":      handleIAMTagServerCertificate,
		"UntagServerCertificate":    handleIAMUntagServerCertificate,
		// Account aliases
		"CreateAccountAlias": handleIAMCreateAccountAlias,
		"DeleteAccountAlias": handleIAMDeleteAccountAlias,
		"ListAccountAliases": handleIAMListAccountAliases,
	} {
		r.Register(action, h)
	}
}

func iamSAMLArn(name string) string {
	return fmt.Sprintf("arn:aws:iam::%s:saml-provider/%s", awsAccountID(), name)
}

func handleIAMCreateSAMLProvider(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("Name")
	if name == "" {
		iamErrorXML(w, "ValidationError", "Name is required", http.StatusBadRequest)
		return
	}
	doc := r.FormValue("SAMLMetadataDocument")
	if doc == "" {
		iamErrorXML(w, "ValidationError", "SAMLMetadataDocument is required", http.StatusBadRequest)
		return
	}
	arn := iamSAMLArn(name)
	if _, ok := iamSAMLProviders.Get(arn); ok {
		iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("SAML provider with name %s already exists.", name), http.StatusConflict)
		return
	}
	now := time.Now().UTC()
	provider := IAMSAMLProvider{
		Arn:                  arn,
		Name:                 name,
		SAMLMetadataDocument: doc,
		UUID:                 strings.ToLower(generateUUID()),
		CreateDate:           now.Format(time.RFC3339),
		ValidUntil:           now.AddDate(5, 0, 0).Format(time.RFC3339),
		Tags:                 iamParseTags(r),
	}
	iamSAMLProviders.Put(arn, provider)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateSAMLProviderResponse %s>
  <CreateSAMLProviderResult>
    <SAMLProviderArn>%s</SAMLProviderArn>
    %s
  </CreateSAMLProviderResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</CreateSAMLProviderResponse>`, iamXmlns, xmlEscape(provider.Arn), iamTagsXML(provider.Tags), generateUUID())
}

func handleIAMGetSAMLProvider(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("SAMLProviderArn")
	provider, ok := iamSAMLProviders.Get(arn)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "SAML provider not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetSAMLProviderResponse %s>
  <GetSAMLProviderResult>
    <SAMLProviderUUID>%s</SAMLProviderUUID>
    <SAMLMetadataDocument>%s</SAMLMetadataDocument>
    <CreateDate>%s</CreateDate>
    <ValidUntil>%s</ValidUntil>
    %s
  </GetSAMLProviderResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetSAMLProviderResponse>`, iamXmlns, xmlEscape(provider.UUID),
		xmlEscape(provider.SAMLMetadataDocument), provider.CreateDate, provider.ValidUntil,
		iamTagsXML(provider.Tags), generateUUID())
}

func handleIAMUpdateSAMLProvider(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("SAMLProviderArn")
	doc := r.FormValue("SAMLMetadataDocument")
	if !iamSAMLProviders.Update(arn, func(p *IAMSAMLProvider) {
		if doc != "" {
			p.SAMLMetadataDocument = doc
		}
	}) {
		iamErrorXML(w, "NoSuchEntity", "SAML provider not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<UpdateSAMLProviderResponse %s>
  <UpdateSAMLProviderResult>
    <SAMLProviderArn>%s</SAMLProviderArn>
  </UpdateSAMLProviderResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</UpdateSAMLProviderResponse>`, iamXmlns, xmlEscape(arn), generateUUID())
}

func handleIAMDeleteSAMLProvider(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("SAMLProviderArn")
	if !iamSAMLProviders.Delete(arn) {
		iamErrorXML(w, "NoSuchEntity", "SAML provider not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteSAMLProviderResponse %s>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</DeleteSAMLProviderResponse>`, iamXmlns, generateUUID())
}

func handleIAMListSAMLProviders(w http.ResponseWriter, r *http.Request) {
	providers := iamSAMLProviders.List()
	sort.Slice(providers, func(i, j int) bool { return providers[i].Arn < providers[j].Arn })
	var members strings.Builder
	for _, p := range providers {
		fmt.Fprintf(&members, "<member><Arn>%s</Arn><ValidUntil>%s</ValidUntil><CreateDate>%s</CreateDate></member>",
			xmlEscape(p.Arn), p.ValidUntil, p.CreateDate)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListSAMLProvidersResponse %s>
  <ListSAMLProvidersResult>
    <SAMLProviderList>%s</SAMLProviderList>
  </ListSAMLProvidersResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListSAMLProvidersResponse>`, iamXmlns, members.String(), generateUUID())
}

func handleIAMListSAMLProviderTags(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("SAMLProviderArn")
	provider, ok := iamSAMLProviders.Get(arn)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "SAML provider not found", http.StatusNotFound)
		return
	}
	iamTagListResultXML(w, "ListSAMLProviderTags", provider.Tags)
}

func handleIAMTagSAMLProvider(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("SAMLProviderArn")
	newTags := iamParseTags(r)
	if !iamSAMLProviders.Update(arn, func(p *IAMSAMLProvider) {
		p.Tags = iamMergeTags(p.Tags, newTags)
	}) {
		iamErrorXML(w, "NoSuchEntity", "SAML provider not found", http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "TagSAMLProvider")
}

func handleIAMUntagSAMLProvider(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("SAMLProviderArn")
	remove := iamParseTagKeys(r)
	if !iamSAMLProviders.Update(arn, func(p *IAMSAMLProvider) {
		p.Tags = iamRemoveTags(p.Tags, remove)
	}) {
		iamErrorXML(w, "NoSuchEntity", "SAML provider not found", http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "UntagSAMLProvider")
}

func handleIAMListOIDCProviderTags(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("OpenIDConnectProviderArn")
	provider, ok := iamOIDCProviders.Get(arn)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "OpenID Connect provider not found", http.StatusNotFound)
		return
	}
	iamTagListResultXML(w, "ListOpenIDConnectProviderTags", iamMapToTags(provider.Tags))
}

func iamServerCertArn(path, name string) string {
	return fmt.Sprintf("arn:aws:iam::%s:server-certificate%s%s", awsAccountID(), path, name)
}

func iamServerCertMetadataXML(c IAMServerCertificate) string {
	return fmt.Sprintf("<Path>%s</Path><ServerCertificateName>%s</ServerCertificateName><ServerCertificateId>%s</ServerCertificateId><Arn>%s</Arn><UploadDate>%s</UploadDate><Expiration>%s</Expiration>",
		xmlEscape(c.Path), xmlEscape(c.Name), xmlEscape(c.Id), xmlEscape(c.Arn), c.UploadDate, c.Expiration)
}

func handleIAMUploadServerCertificate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("ServerCertificateName")
	if name == "" {
		iamErrorXML(w, "ValidationError", "ServerCertificateName is required", http.StatusBadRequest)
		return
	}
	body := r.FormValue("CertificateBody")
	if body == "" {
		iamErrorXML(w, "ValidationError", "CertificateBody is required", http.StatusBadRequest)
		return
	}
	pk := r.FormValue("PrivateKey")
	if pk == "" {
		iamErrorXML(w, "ValidationError", "PrivateKey is required", http.StatusBadRequest)
		return
	}
	if _, ok := iamServerCerts.Get(name); ok {
		iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("The Server Certificate with name %s already exists.", name), http.StatusConflict)
		return
	}
	path := r.FormValue("Path")
	if path == "" {
		path = "/"
	}
	now := time.Now().UTC()
	cert := IAMServerCertificate{
		Name:             name,
		Id:               iamRandomID("ASCA", 16),
		Arn:              iamServerCertArn(path, name),
		Path:             path,
		CertificateBody:  body,
		CertificateChain: r.FormValue("CertificateChain"),
		PrivateKey:       pk,
		UploadDate:       now.Format(time.RFC3339),
		Expiration:       now.AddDate(1, 0, 0).Format(time.RFC3339),
		Tags:             iamParseTags(r),
	}
	iamServerCerts.Put(name, cert)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<UploadServerCertificateResponse %s>
  <UploadServerCertificateResult>
    <ServerCertificateMetadata>%s</ServerCertificateMetadata>
    %s
  </UploadServerCertificateResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</UploadServerCertificateResponse>`, iamXmlns, iamServerCertMetadataXML(cert), iamTagsXML(cert.Tags), generateUUID())
}

func handleIAMGetServerCertificate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("ServerCertificateName")
	cert, ok := iamServerCerts.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The Server Certificate with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	chain := ""
	if cert.CertificateChain != "" {
		chain = "<CertificateChain>" + xmlEscape(cert.CertificateChain) + "</CertificateChain>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetServerCertificateResponse %s>
  <GetServerCertificateResult>
    <ServerCertificate>
      <ServerCertificateMetadata>%s</ServerCertificateMetadata>
      <CertificateBody>%s</CertificateBody>
      %s
      %s
    </ServerCertificate>
  </GetServerCertificateResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetServerCertificateResponse>`, iamXmlns, iamServerCertMetadataXML(cert),
		xmlEscape(cert.CertificateBody), chain, iamTagsXML(cert.Tags), generateUUID())
}

func handleIAMUpdateServerCertificate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("ServerCertificateName")
	newName := r.FormValue("NewServerCertificateName")
	newPath := r.FormValue("NewPath")
	cert, ok := iamServerCerts.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The Server Certificate with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	if newName != "" && newName != name {
		if _, exists := iamServerCerts.Get(newName); exists {
			iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("The Server Certificate with name %s already exists.", newName), http.StatusConflict)
			return
		}
	}
	iamServerCerts.Delete(name)
	if newPath != "" {
		cert.Path = newPath
	}
	if newName != "" {
		cert.Name = newName
	}
	cert.Arn = iamServerCertArn(cert.Path, cert.Name)
	iamServerCerts.Put(cert.Name, cert)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<UpdateServerCertificateResponse %s>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</UpdateServerCertificateResponse>`, iamXmlns, generateUUID())
}

func handleIAMDeleteServerCertificate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("ServerCertificateName")
	if !iamServerCerts.Delete(name) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The Server Certificate with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteServerCertificateResponse %s>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</DeleteServerCertificateResponse>`, iamXmlns, generateUUID())
}

func handleIAMListServerCertificates(w http.ResponseWriter, r *http.Request) {
	certs := iamServerCerts.List()
	sort.Slice(certs, func(i, j int) bool { return certs[i].Name < certs[j].Name })
	page, next := awsPageExplicit(certs, r.FormValue("Marker"), atoiDefault(r.FormValue("MaxItems"), 0))
	var members strings.Builder
	for _, c := range page {
		fmt.Fprintf(&members, "<member>%s</member>", iamServerCertMetadataXML(c))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListServerCertificatesResponse %s>
  <ListServerCertificatesResult>
    <ServerCertificateMetadataList>%s</ServerCertificateMetadataList>
    <IsTruncated>%t</IsTruncated>%s
  </ListServerCertificatesResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListServerCertificatesResponse>`, iamXmlns, members.String(), next != "", iamMarkerXML(next), generateUUID())
}

func handleIAMListServerCertificateTags(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("ServerCertificateName")
	cert, ok := iamServerCerts.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The Server Certificate with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	iamTagListResultXML(w, "ListServerCertificateTags", cert.Tags)
}

func handleIAMTagServerCertificate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("ServerCertificateName")
	newTags := iamParseTags(r)
	if !iamServerCerts.Update(name, func(c *IAMServerCertificate) {
		c.Tags = iamMergeTags(c.Tags, newTags)
	}) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The Server Certificate with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "TagServerCertificate")
}

func handleIAMUntagServerCertificate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("ServerCertificateName")
	remove := iamParseTagKeys(r)
	if !iamServerCerts.Update(name, func(c *IAMServerCertificate) {
		c.Tags = iamRemoveTags(c.Tags, remove)
	}) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The Server Certificate with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "UntagServerCertificate")
}

func handleIAMCreateAccountAlias(w http.ResponseWriter, r *http.Request) {
	alias := r.FormValue("AccountAlias")
	if alias == "" {
		iamErrorXML(w, "ValidationError", "AccountAlias is required", http.StatusBadRequest)
		return
	}
	// Real IAM allows at most one alias per account: a second CreateAccountAlias
	// with a different alias fails with EntityAlreadyExists.
	for _, existing := range iamAccountAliases.List() {
		if existing != alias {
			iamErrorXML(w, "EntityAlreadyExists", "The account alias "+existing+" already exists.", http.StatusConflict)
			return
		}
	}
	iamAccountAliases.Put(alias, alias)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateAccountAliasResponse %s>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</CreateAccountAliasResponse>`, iamXmlns, generateUUID())
}

func handleIAMDeleteAccountAlias(w http.ResponseWriter, r *http.Request) {
	alias := r.FormValue("AccountAlias")
	if !iamAccountAliases.Delete(alias) {
		iamErrorXML(w, "NoSuchEntity", "The account alias "+alias+" cannot be found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteAccountAliasResponse %s>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</DeleteAccountAliasResponse>`, iamXmlns, generateUUID())
}

func handleIAMListAccountAliases(w http.ResponseWriter, r *http.Request) {
	aliases := iamAccountAliases.List()
	sort.Strings(aliases)
	var members strings.Builder
	for _, a := range aliases {
		fmt.Fprintf(&members, "<member>%s</member>", xmlEscape(a))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListAccountAliasesResponse %s>
  <ListAccountAliasesResult>
    <AccountAliases>%s</AccountAliases>
    <IsTruncated>false</IsTruncated>
  </ListAccountAliasesResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListAccountAliasesResponse>`, iamXmlns, members.String(), generateUUID())
}

// iamTagListResultXML emits the ListXxxTags response shape (Tags member list +
// IsTruncated; the sim never truncates a tag list).
func iamTagListResultXML(w http.ResponseWriter, op string, tags []IAMTag) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s>
  <%sResult>
    %s
    <IsTruncated>false</IsTruncated>
  </%sResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</%sResponse>`, op, iamXmlns, op, iamTagsXML(tags), op, generateUUID(), op)
}

func iamParseTagKeys(r *http.Request) map[string]bool {
	remove := map[string]bool{}
	for i := 1; ; i++ {
		k := r.FormValue(fmt.Sprintf("TagKeys.member.%d", i))
		if k == "" {
			break
		}
		remove[k] = true
	}
	return remove
}

func iamRemoveTags(tags []IAMTag, remove map[string]bool) []IAMTag {
	kept := tags[:0]
	for _, t := range tags {
		if !remove[t.Key] {
			kept = append(kept, t)
		}
	}
	return kept
}

// iamMapToTags converts a map-backed tag set (OIDC providers store tags as a
// map) to the slice form, in deterministic key order.
func iamMapToTags(m map[string]string) []IAMTag {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]IAMTag, 0, len(keys))
	for _, k := range keys {
		out = append(out, IAMTag{Key: k, Value: m[k]})
	}
	return out
}
