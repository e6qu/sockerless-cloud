package main

import (
	"fmt"
	"net/http"
	"net/url"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// AWS Identity and Access Management account properties and role templates.
//
// Account properties are a real account-scoped key/value map —
// PutAccountProperties writes them and GetAccountProperties returns exactly
// what was written, which is the whole contract.
//
// Role templates are a different matter. Every role template lives under the
// literal account `aws` — arn:<partition>:iam::aws:role-template/... — so the
// templates and their content (trust policies, inline policies, name
// patterns) are AWS's own published catalog, like the managed runtime-stack
// and SKU catalogs this project has declined to invent before. Serving
// GetRoleTemplateVersion or AcquireRole with fabricated template content
// would hand a caller a trust policy AWS never published, so both fail
// loudly, and the failure names the missing catalog rather than a vague
// unavailability.

// iamAccountProperties is the account's property map. One row per account,
// and this simulator serves one account.
var iamAccountProperties sim.Store[map[string]string]

const iamAccountPropertiesKey = "account"

func registerIAMAccountProperties(r *sim.AWSQueryRouter, srv *sim.Server) {
	iamAccountProperties = sim.MakeStore[map[string]string](srv.DB(), "iam_account_properties")
	r.Register("GetAccountProperties", handleIAMGetAccountProperties)
	r.Register("PutAccountProperties", handleIAMPutAccountProperties)
	r.Register("GetRoleTemplateVersion", handleIAMGetRoleTemplateVersion)
	r.Register("AcquireRole", handleIAMAcquireRole)
}

func handleIAMPutAccountProperties(w http.ResponseWriter, r *http.Request) {
	// The query protocol flattens a map into Properties.entry.N.key/value.
	properties, _ := iamAccountProperties.Get(iamAccountPropertiesKey)
	if properties == nil {
		properties = map[string]string{}
	}
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("Properties.entry.%d.key", i))
		if key == "" {
			break
		}
		value := r.FormValue(fmt.Sprintf("Properties.entry.%d.value", i))
		properties[key] = value
	}
	iamAccountProperties.Put(iamAccountPropertiesKey, properties)
	iamEmptyResultXML(w, "PutAccountProperties")
}

func handleIAMGetAccountProperties(w http.ResponseWriter, r *http.Request) {
	properties, _ := iamAccountProperties.Get(iamAccountPropertiesKey)
	entries := ""
	for key, value := range properties {
		entries += fmt.Sprintf("<entry><key>%s</key><value>%s</value></entry>",
			xmlEscape(key), xmlEscape(value))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetAccountPropertiesResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <GetAccountPropertiesResult><Properties>%s</Properties></GetAccountPropertiesResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetAccountPropertiesResponse>`, entries, generateUUID())
}

// iamRoleTemplateCatalogUnavailable is the reason both template operations
// fail: the catalog is AWS's, not this simulator's to invent.
const iamRoleTemplateCatalogUnavailable = "IAM role templates are AWS's own published catalog: " +
	"every template lives under the literal account 'aws' and carries a trust policy, inline " +
	"policies and naming patterns that AWS authors. This simulator does not vendor that catalog " +
	"and will not fabricate a template's content, because a role acquired from an invented trust " +
	"policy would be a role AWS never defined. Roles themselves are fully implemented: CreateRole " +
	"with an explicit trust policy is the same operation without the catalog dependency."

func handleIAMGetRoleTemplateVersion(w http.ResponseWriter, r *http.Request) {
	iamErrorXML(w, "NoSuchEntity", iamRoleTemplateCatalogUnavailable, http.StatusNotFound)
}

func handleIAMAcquireRole(w http.ResponseWriter, r *http.Request) {
	templateArn, _ := url.QueryUnescape(r.FormValue("TemplateArn"))
	_ = templateArn
	iamErrorXML(w, "NoSuchEntity", iamRoleTemplateCatalogUnavailable, http.StatusNotFound)
}
