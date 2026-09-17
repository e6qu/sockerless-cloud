package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
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

func registerIAMAccountProperties(r *AWSQueryRouter, srv *sim.Server) {
	iamAccountProperties = sim.MakeStore[map[string]string](srv.DB(), "iam_account_properties")
	r.Register("GetAccountProperties", handleIAMGetAccountProperties)
	r.Register("PutAccountProperties", handleIAMPutAccountProperties)
	r.Register("GetRoleTemplateVersion", handleIAMGetRoleTemplateVersion)
	r.Register("AcquireRole", handleIAMAcquireRole)
}

// iamRequestAccountProperties reads the property map the query protocol
// flattens into Properties.entry.N.key/value. The entries are returned in
// request order, because the namespace rule is about the request as a whole.
func iamRequestAccountProperties(r *http.Request) [][2]string {
	var entries [][2]string
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("Properties.entry.%d.key", i))
		if key == "" {
			break
		}
		entries = append(entries, [2]string{key, r.FormValue(fmt.Sprintf("Properties.entry.%d.value", i))})
	}
	return entries
}

// iamAccountPropertyNamespace splits a property key into its namespace. The
// model is explicit: "Each key uses the format Namespace/PropertyName. The key
// must contain exactly one / separating the namespace from the property name,
// and cannot start or end with /."
func iamAccountPropertyNamespace(key string) (string, bool) {
	namespace, property, found := strings.Cut(key, "/")
	if !found || namespace == "" || property == "" || strings.Contains(property, "/") {
		return "", false
	}
	return namespace, true
}

func handleIAMPutAccountProperties(w http.ResponseWriter, r *http.Request) {
	entries := iamRequestAccountProperties(r)
	// "All properties in a single request must belong to the same namespace."
	// Accepting a malformed key would store a property GetAccountProperties
	// then returns in a shape the model says cannot exist, and a policy
	// conditioned on iam:AccountPropertyNamespaces would be evaluated against
	// a namespace the caller never named.
	namespace := ""
	for _, entry := range entries {
		got, ok := iamAccountPropertyNamespace(entry[0])
		if !ok {
			iamErrorXML(w, "InvalidInput", fmt.Sprintf("The account property key %q is not in Namespace/PropertyName "+
				"format: it must contain exactly one '/' and cannot start or end with one.", entry[0]),
				http.StatusBadRequest)
			return
		}
		if namespace == "" {
			namespace = got
		} else if got != namespace {
			iamErrorXML(w, "InvalidInput", fmt.Sprintf("All account properties in one request must belong to the same "+
				"namespace: the request names both %q and %q.", namespace, got), http.StatusBadRequest)
			return
		}
	}
	properties, _ := iamAccountProperties.Get(iamAccountPropertiesKey)
	if properties == nil {
		properties = map[string]string{}
	}
	for _, entry := range entries {
		properties[entry[0]] = entry[1]
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
