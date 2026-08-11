package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// IAM service-linked roles (SLRs) + OIDC providers. Extends iam.go.
// Same AWS Query Protocol (POST / + Action=<Op>, form-encoded body,
// XML response, namespace https://iam.amazonaws.com/doc/2010-05-08/).
//
// Sim policy:
//   - SLR creation accepts any service principal (e.g.,
//     "cloudfront.amazonaws.com", "amplify.amazonaws.com") — no
//     allowlist. Real AWS gates this to specific services.
//   - SLR deletion is eager: GetServiceLinkedRoleDeletionStatus
//     returns SUCCEEDED immediately. Real AWS goes through
//     IN_PROGRESS → SUCCEEDED over seconds.

// ---------- Types ----------

type IAMServiceLinkedRole struct {
	RoleName                 string
	RoleId                   string
	Arn                      string
	Path                     string
	AssumeRolePolicyDocument string
	CreateDate               string
	ServicePrincipal         string
	Description              string
}

type IAMOIDCProvider struct {
	Arn            string
	URL            string
	ClientIDList   []string
	ThumbprintList []string
	CreateDate     string
	Tags           map[string]string
}

var (
	iamSLRs          sim.Store[IAMServiceLinkedRole]
	iamOIDCProviders sim.Store[IAMOIDCProvider]
	iamSLRDeletions  sim.Store[string]
)

func registerIAMSLRandOIDC(r *sim.AWSQueryRouter, srv *sim.Server) {
	iamSLRs = sim.MakeStore[IAMServiceLinkedRole](srv.DB(), "iam_slrs")
	iamOIDCProviders = sim.MakeStore[IAMOIDCProvider](srv.DB(), "iam_oidc_providers")
	iamSLRDeletions = sim.MakeStore[string](srv.DB(), "iam_service_linked_role_deletions")

	// Service-linked roles
	r.Register("CreateServiceLinkedRole", handleIAMCreateServiceLinkedRole)
	r.Register("DeleteServiceLinkedRole", handleIAMDeleteServiceLinkedRole)
	r.Register("GetServiceLinkedRoleDeletionStatus", handleIAMGetSLRDeletionStatus)
	// OIDC providers
	r.Register("CreateOpenIDConnectProvider", handleIAMCreateOIDCProvider)
	r.Register("GetOpenIDConnectProvider", handleIAMGetOIDCProvider)
	r.Register("TagOpenIDConnectProvider", handleIAMTagOIDCProvider)
	r.Register("UntagOpenIDConnectProvider", handleIAMUntagOIDCProvider)
	r.Register("UpdateOpenIDConnectProviderThumbprint", handleIAMUpdateOIDCThumbprint)
	r.Register("AddClientIDToOpenIDConnectProvider", handleIAMAddOIDCClientID)
	r.Register("RemoveClientIDFromOpenIDConnectProvider", handleIAMRemoveOIDCClientID)
	r.Register("DeleteOpenIDConnectProvider", handleIAMDeleteOIDCProvider)
	r.Register("ListOpenIDConnectProviders", handleIAMListOIDCProviders)
}

// ---------- Service-linked role helpers ----------

func iamSLRName(servicePrincipal, customSuffix string) string {
	// AWSServiceRoleFor<Service> where <Service> is derived from the
	// principal. cloudfront.amazonaws.com → CloudFrontLogger (real AWS
	// uses service-specific suffixes; sim does a simple title-case map
	// over a known list, falling back to PascalCase of the leading
	// segment).
	known := map[string]string{
		"cloudfront.amazonaws.com":        "CloudFrontLogger",
		"amplify.amazonaws.com":           "Amplify",
		"elasticbeanstalk.amazonaws.com":  "ElasticBeanstalk",
		"ecs.amazonaws.com":               "ECS",
		"eks.amazonaws.com":               "EKS",
		"rds.amazonaws.com":               "RDS",
		"replication.ecr.amazonaws.com":   "ECRReplication",
		"globalaccelerator.amazonaws.com": "GlobalAccelerator",
		"appsync.amazonaws.com":           "AppSync",
	}
	suffix := known[servicePrincipal]
	if suffix == "" {
		// Fallback: take the leading service token + PascalCase.
		// SplitN always returns ≥1 element but the first can be empty
		// (e.g. principal ".foo"), so guard the leading character and
		// title-case rune-aware (a multibyte first rune must not be
		// sliced mid-byte).
		parts := strings.SplitN(servicePrincipal, ".", 2)
		if parts[0] != "" {
			rs := []rune(parts[0])
			suffix = strings.ToUpper(string(rs[0])) + string(rs[1:])
		}
	}
	name := "AWSServiceRoleFor" + suffix
	if customSuffix != "" {
		name += "_" + customSuffix
	}
	return name
}

func iamRandomID(prefix string, n int) string {
	buf := make([]byte, n/2)
	_, _ = rand.Read(buf)
	return prefix + strings.ToUpper(hex.EncodeToString(buf))
}

func iamSLRPath(servicePrincipal string) string {
	return "/aws-service-role/" + servicePrincipal + "/"
}

func iamSLRARN(name, servicePrincipal string) string {
	return fmt.Sprintf("arn:aws:iam::%s:role%s%s", awsAccountID(), iamSLRPath(servicePrincipal), name)
}

func iamSLRAssumeDoc(servicePrincipal string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"` + servicePrincipal + `"},"Action":"sts:AssumeRole"}]}`
}

// ---------- SLR handlers ----------

func handleIAMCreateServiceLinkedRole(w http.ResponseWriter, r *http.Request) {
	sp := r.FormValue("AWSServiceName")
	if sp == "" {
		iamErrorXML(w, "InvalidInput", "AWSServiceName is required", http.StatusBadRequest)
		return
	}
	customSuffix := r.FormValue("CustomSuffix")
	description := r.FormValue("Description")
	name := iamSLRName(sp, customSuffix)
	if _, exists := iamSLRs.Get(name); exists {
		iamErrorXML(w, "InvalidInput", "Service role name "+name+" has been taken in this account, please try a different suffix.", http.StatusBadRequest)
		return
	}
	role := IAMServiceLinkedRole{
		RoleName:                 name,
		RoleId:                   iamRandomID("AROA", 16),
		Arn:                      iamSLRARN(name, sp),
		Path:                     iamSLRPath(sp),
		AssumeRolePolicyDocument: iamSLRAssumeDoc(sp),
		CreateDate:               time.Now().UTC().Format(time.RFC3339),
		ServicePrincipal:         sp,
		Description:              description,
	}
	iamSLRs.Put(name, role)
	// Real AWS exposes the SLR via GetRole as well. Terraform's
	// aws_iam_service_linked_role.Read calls GetRole with the full
	// role name — store an IAMRole shadow in the regular store so
	// that path finds the same record.
	iamRoles.Put(name, IAMRole{
		RoleName:                 role.RoleName,
		RoleId:                   role.RoleId,
		Arn:                      role.Arn,
		Path:                     role.Path,
		AssumeRolePolicyDocument: role.AssumeRolePolicyDocument,
		CreateDate:               role.CreateDate,
		Description:              role.Description,
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateServiceLinkedRoleResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <CreateServiceLinkedRoleResult>
    <Role>
      <Path>%s</Path>
      <RoleName>%s</RoleName>
      <RoleId>%s</RoleId>
      <Arn>%s</Arn>
      <CreateDate>%s</CreateDate>
      <AssumeRolePolicyDocument>%s</AssumeRolePolicyDocument>
      <Description>%s</Description>
    </Role>
  </CreateServiceLinkedRoleResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</CreateServiceLinkedRoleResponse>`,
		role.Path, role.RoleName, role.RoleId, role.Arn, role.CreateDate,
		url.QueryEscape(role.AssumeRolePolicyDocument), html.EscapeString(role.Description), generateUUID())
}

func handleIAMDeleteServiceLinkedRole(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("RoleName")
	if _, ok := iamSLRs.Get(name); !ok {
		iamErrorXML(w, "NoSuchEntity", "Service-linked role "+name+" not found.", http.StatusNotFound)
		return
	}
	iamSLRs.Delete(name)
	iamRoles.Delete(name) // remove the shadow record
	taskID := iamRandomID("task_", 16)
	iamSLRDeletions.Put(taskID, "SUCCEEDED")
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteServiceLinkedRoleResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <DeleteServiceLinkedRoleResult><DeletionTaskId>%s</DeletionTaskId></DeleteServiceLinkedRoleResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</DeleteServiceLinkedRoleResponse>`, taskID, generateUUID())
}

func handleIAMGetSLRDeletionStatus(w http.ResponseWriter, r *http.Request) {
	taskID := r.FormValue("DeletionTaskId")
	status, ok := iamSLRDeletions.Get(taskID)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "Deletion task "+taskID+" was not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetServiceLinkedRoleDeletionStatusResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <GetServiceLinkedRoleDeletionStatusResult><Status>%s</Status></GetServiceLinkedRoleDeletionStatusResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetServiceLinkedRoleDeletionStatusResponse>`, status, generateUUID())
}

// ---------- OIDC provider helpers ----------

func iamOIDCArn(providerURL string) string {
	// Real format: arn:aws:iam::<account>:oidc-provider/<url-without-scheme>
	u := strings.TrimPrefix(strings.TrimPrefix(providerURL, "https://"), "http://")
	return fmt.Sprintf("arn:aws:iam::%s:oidc-provider/%s", awsAccountID(), u)
}

func iamReadList(r *http.Request, key string) []string {
	// AWS Query Protocol encodes lists as Key.member.1=foo&Key.member.2=bar.
	values := []string{}
	for i := 1; ; i++ {
		v := r.FormValue(fmt.Sprintf("%s.member.%d", key, i))
		if v == "" {
			break
		}
		values = append(values, v)
	}
	return values
}

// ---------- OIDC handlers ----------

func handleIAMCreateOIDCProvider(w http.ResponseWriter, r *http.Request) {
	providerURL := r.FormValue("Url")
	if providerURL == "" {
		iamErrorXML(w, "InvalidInput", "Url is required", http.StatusBadRequest)
		return
	}
	clientIDs := iamReadList(r, "ClientIDList")
	thumbprints := iamReadList(r, "ThumbprintList")
	if len(thumbprints) == 0 {
		iamErrorXML(w, "InvalidInput", "ThumbprintList is required", http.StatusBadRequest)
		return
	}
	arn := iamOIDCArn(providerURL)
	if _, exists := iamOIDCProviders.Get(arn); exists {
		iamErrorXML(w, "EntityAlreadyExists", "Provider for "+providerURL+" already exists", http.StatusConflict)
		return
	}
	tags := map[string]string{}
	for i := 1; ; i++ {
		k := r.FormValue(fmt.Sprintf("Tags.member.%d.Key", i))
		if k == "" {
			break
		}
		tags[k] = r.FormValue(fmt.Sprintf("Tags.member.%d.Value", i))
	}
	provider := IAMOIDCProvider{
		Arn:            arn,
		URL:            providerURL,
		ClientIDList:   clientIDs,
		ThumbprintList: thumbprints,
		CreateDate:     time.Now().UTC().Format(time.RFC3339),
		Tags:           tags,
	}
	iamOIDCProviders.Put(arn, provider)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateOpenIDConnectProviderResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <CreateOpenIDConnectProviderResult>
    <OpenIDConnectProviderArn>%s</OpenIDConnectProviderArn>
  </CreateOpenIDConnectProviderResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</CreateOpenIDConnectProviderResponse>`, arn, generateUUID())
}

func iamOIDCMembers(values []string, elemName string) string {
	var b strings.Builder
	for _, v := range values {
		b.WriteString("<member>")
		b.WriteString(html.EscapeString(v))
		b.WriteString("</member>")
	}
	_ = elemName
	return b.String()
}

func handleIAMGetOIDCProvider(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("OpenIDConnectProviderArn")
	provider, ok := iamOIDCProviders.Get(arn)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "OpenID Connect provider not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetOpenIDConnectProviderResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <GetOpenIDConnectProviderResult>
    <Url>%s</Url>
    <CreateDate>%s</CreateDate>
    <ClientIDList>%s</ClientIDList>
    <ThumbprintList>%s</ThumbprintList>
    %s
  </GetOpenIDConnectProviderResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetOpenIDConnectProviderResponse>`,
		provider.URL, provider.CreateDate,
		iamOIDCMembers(provider.ClientIDList, "member"),
		iamOIDCMembers(provider.ThumbprintList, "member"),
		iamMapTagsXML(provider.Tags),
		generateUUID())
}

// iamMapTagsXML renders a map-backed tag set (OIDC providers store tags as a
// map) in deterministic key order as the IAM <Tags> member list.
func iamMapTagsXML(tags map[string]string) string {
	if len(tags) == 0 {
		return "<Tags/>"
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("<Tags>")
	for _, k := range keys {
		fmt.Fprintf(&b, "<member><Key>%s</Key><Value>%s</Value></member>", xmlEscape(k), xmlEscape(tags[k]))
	}
	b.WriteString("</Tags>")
	return b.String()
}

func handleIAMTagOIDCProvider(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("OpenIDConnectProviderArn")
	if !iamOIDCProviders.Update(arn, func(p *IAMOIDCProvider) {
		if p.Tags == nil {
			p.Tags = map[string]string{}
		}
		for i := 1; ; i++ {
			k := r.FormValue(fmt.Sprintf("Tags.member.%d.Key", i))
			if k == "" {
				break
			}
			p.Tags[k] = r.FormValue(fmt.Sprintf("Tags.member.%d.Value", i))
		}
	}) {
		iamErrorXML(w, "NoSuchEntity", "OpenID Connect provider not found", http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "TagOpenIDConnectProvider")
}

func handleIAMUntagOIDCProvider(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("OpenIDConnectProviderArn")
	if !iamOIDCProviders.Update(arn, func(p *IAMOIDCProvider) {
		for i := 1; ; i++ {
			k := r.FormValue(fmt.Sprintf("TagKeys.member.%d", i))
			if k == "" {
				break
			}
			delete(p.Tags, k)
		}
	}) {
		iamErrorXML(w, "NoSuchEntity", "OpenID Connect provider not found", http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "UntagOpenIDConnectProvider")
}

func handleIAMUpdateOIDCThumbprint(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("OpenIDConnectProviderArn")
	thumbprints := iamReadList(r, "ThumbprintList")
	if !iamOIDCProviders.Update(arn, func(p *IAMOIDCProvider) {
		p.ThumbprintList = thumbprints
	}) {
		iamErrorXML(w, "NoSuchEntity", "OpenID Connect provider not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<UpdateOpenIDConnectProviderThumbprintResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</UpdateOpenIDConnectProviderThumbprintResponse>`, generateUUID())
}

func handleIAMAddOIDCClientID(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("OpenIDConnectProviderArn")
	clientID := r.FormValue("ClientID")
	if !iamOIDCProviders.Update(arn, func(p *IAMOIDCProvider) {
		for _, c := range p.ClientIDList {
			if c == clientID {
				return
			}
		}
		p.ClientIDList = append(p.ClientIDList, clientID)
	}) {
		iamErrorXML(w, "NoSuchEntity", "OpenID Connect provider not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AddClientIDToOpenIDConnectProviderResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</AddClientIDToOpenIDConnectProviderResponse>`, generateUUID())
}

func handleIAMRemoveOIDCClientID(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("OpenIDConnectProviderArn")
	clientID := r.FormValue("ClientID")
	if !iamOIDCProviders.Update(arn, func(p *IAMOIDCProvider) {
		out := p.ClientIDList[:0]
		for _, c := range p.ClientIDList {
			if c != clientID {
				out = append(out, c)
			}
		}
		p.ClientIDList = out
	}) {
		iamErrorXML(w, "NoSuchEntity", "OpenID Connect provider not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RemoveClientIDFromOpenIDConnectProviderResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</RemoveClientIDFromOpenIDConnectProviderResponse>`, generateUUID())
}

func handleIAMDeleteOIDCProvider(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("OpenIDConnectProviderArn")
	if _, ok := iamOIDCProviders.Get(arn); !ok {
		iamErrorXML(w, "NoSuchEntity", "OpenID Connect provider not found", http.StatusNotFound)
		return
	}
	iamOIDCProviders.Delete(arn)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteOpenIDConnectProviderResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</DeleteOpenIDConnectProviderResponse>`, generateUUID())
}

func handleIAMListOIDCProviders(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	for _, p := range iamOIDCProviders.List() {
		b.WriteString("<member><Arn>")
		b.WriteString(html.EscapeString(p.Arn))
		b.WriteString("</Arn></member>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListOpenIDConnectProvidersResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ListOpenIDConnectProvidersResult>
    <OpenIDConnectProviderList>%s</OpenIDConnectProviderList>
  </ListOpenIDConnectProvidersResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListOpenIDConnectProvidersResponse>`, b.String(), generateUUID())
}
