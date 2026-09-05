package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon S3 Access Grants: an account-level instance, the locations it manages
// (an S3 prefix plus the IAM role that reaches it), and the grants that hand
// principals permission inside those locations. A grantee redeems a grant
// through GetDataAccess, which vends credentials by assuming the location's
// role — so the credentials it returns are the ones STS mints here, and they
// work on the S3 surface like any other session credentials.

// S3AccessGrantsInstance is one account's Access Grants instance.
type S3AccessGrantsInstance struct {
	AccountID                    string            `json:"accountId"`
	InstanceID                   string            `json:"instanceId"`
	CreatedAt                    string            `json:"createdAt"`
	IdentityCenterArn            string            `json:"identityCenterArn,omitempty"`
	IdentityCenterApplicationArn string            `json:"identityCenterApplicationArn,omitempty"`
	Tags                         map[string]string `json:"tags,omitempty"`
	Policy                       string            `json:"policy,omitempty"`
	PolicyOrganization           string            `json:"policyOrganization,omitempty"`
	PolicyCreatedAt              string            `json:"policyCreatedAt,omitempty"`
}

// S3AccessGrantsLocation is a prefix the instance manages, and the role it
// reaches that prefix with.
type S3AccessGrantsLocation struct {
	AccountID     string            `json:"accountId"`
	LocationID    string            `json:"locationId"`
	LocationScope string            `json:"locationScope"`
	IAMRoleArn    string            `json:"iamRoleArn"`
	CreatedAt     string            `json:"createdAt"`
	Tags          map[string]string `json:"tags,omitempty"`
}

// S3AccessGrant hands one grantee a permission inside one location.
type S3AccessGrant struct {
	AccountID         string            `json:"accountId"`
	GrantID           string            `json:"grantId"`
	CreatedAt         string            `json:"createdAt"`
	LocationID        string            `json:"locationId"`
	S3SubPrefix       string            `json:"s3SubPrefix,omitempty"`
	GranteeType       string            `json:"granteeType"`
	GranteeIdentifier string            `json:"granteeIdentifier"`
	Permission        string            `json:"permission"`
	ApplicationArn    string            `json:"applicationArn,omitempty"`
	S3PrefixType      string            `json:"s3PrefixType,omitempty"`
	GrantScope        string            `json:"grantScope"`
	Tags              map[string]string `json:"tags,omitempty"`
}

var (
	s3AccessGrantsInstances sim.Store[S3AccessGrantsInstance]
	s3AccessGrantsLocations sim.Store[S3AccessGrantsLocation]
	s3AccessGrants          sim.Store[S3AccessGrant]
)

// S3AccessGrantsCredential records that one credential was issued by an S3
// Access Grants instance, so a request signed with it can say which.
type S3AccessGrantsCredential struct {
	AccessKeyID string `json:"accessKeyId"`
	InstanceARN string `json:"instanceArn"`
	GrantScope  string `json:"grantScope"`
}

var s3AccessGrantsCredentials sim.Store[S3AccessGrantsCredential]

func s3AccessGrantsInstanceARN(account string) string {
	return fmt.Sprintf("arn:aws:s3:%s:%s:access-grants/default", awsRegion(), account)
}

func s3AccessGrantsLocationARN(account, id string) string {
	return fmt.Sprintf("arn:aws:s3:%s:%s:access-grants/default/location/%s", awsRegion(), account, id)
}

func s3AccessGrantARN(account, id string) string {
	return fmt.Sprintf("arn:aws:s3:%s:%s:access-grants/default/grant/%s", awsRegion(), account, id)
}

// s3AccessGrantsDefaultLocationID is the identifier of the location covering
// every bucket in the account, which is the one an instance starts with.
const s3AccessGrantsDefaultLocationID = "default"

func registerS3ControlAccessGrants(srv *sim.Server) {
	s3AccessGrantsInstances = sim.MakeStore[S3AccessGrantsInstance](srv.DB(), "s3_access_grants_instances")
	s3AccessGrantsLocations = sim.MakeStore[S3AccessGrantsLocation](srv.DB(), "s3_access_grants_locations")
	s3AccessGrants = sim.MakeStore[S3AccessGrant](srv.DB(), "s3_access_grants")
	s3AccessGrantsCredentials = sim.MakeStore[S3AccessGrantsCredential](srv.DB(), "s3_access_grants_credentials")

	srv.HandleFunc("POST /v20180820/accessgrantsinstance", handleS3CreateAccessGrantsInstance)
	srv.HandleFunc("GET /v20180820/accessgrantsinstance", handleS3GetAccessGrantsInstance)
	srv.HandleFunc("DELETE /v20180820/accessgrantsinstance", handleS3DeleteAccessGrantsInstance)
	srv.HandleFunc("GET /v20180820/accessgrantsinstances", handleS3ListAccessGrantsInstances)
	srv.HandleFunc("GET /v20180820/accessgrantsinstance/prefix", handleS3GetAccessGrantsInstanceForPrefix)

	srv.HandleFunc("POST /v20180820/accessgrantsinstance/identitycenter", handleS3AssociateAccessGrantsIdentityCenter)
	srv.HandleFunc("DELETE /v20180820/accessgrantsinstance/identitycenter", handleS3DissociateAccessGrantsIdentityCenter)

	srv.HandleFunc("PUT /v20180820/accessgrantsinstance/resourcepolicy", handleS3PutAccessGrantsInstanceResourcePolicy)
	srv.HandleFunc("GET /v20180820/accessgrantsinstance/resourcepolicy", handleS3GetAccessGrantsInstanceResourcePolicy)
	srv.HandleFunc("DELETE /v20180820/accessgrantsinstance/resourcepolicy", handleS3DeleteAccessGrantsInstanceResourcePolicy)

	srv.HandleFunc("POST /v20180820/accessgrantsinstance/location", handleS3CreateAccessGrantsLocation)
	srv.HandleFunc("GET /v20180820/accessgrantsinstance/location/{locationId}", handleS3GetAccessGrantsLocation)
	srv.HandleFunc("PUT /v20180820/accessgrantsinstance/location/{locationId}", handleS3UpdateAccessGrantsLocation)
	srv.HandleFunc("DELETE /v20180820/accessgrantsinstance/location/{locationId}", handleS3DeleteAccessGrantsLocation)
	srv.HandleFunc("GET /v20180820/accessgrantsinstance/locations", handleS3ListAccessGrantsLocations)

	srv.HandleFunc("POST /v20180820/accessgrantsinstance/grant", handleS3CreateAccessGrant)
	srv.HandleFunc("GET /v20180820/accessgrantsinstance/grant/{grantId}", handleS3GetAccessGrant)
	srv.HandleFunc("DELETE /v20180820/accessgrantsinstance/grant/{grantId}", handleS3DeleteAccessGrant)
	srv.HandleFunc("GET /v20180820/accessgrantsinstance/grants", handleS3ListAccessGrants)
	srv.HandleFunc("GET /v20180820/accessgrantsinstance/caller/grants", handleS3ListCallerAccessGrants)
	srv.HandleFunc("GET /v20180820/accessgrantsinstance/dataaccess", handleS3GetDataAccess)
}

// s3ControlOptionalXMLBody reads a request document that the operation does
// not require. An absent or unreadable body is an empty document rather than
// an error, because there is nothing in it the operation needs.
func s3ControlOptionalXMLBody(r *http.Request) s3ControlXMLNode {
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		return s3ControlXMLNode{}
	}
	var node s3ControlXMLNode
	if xml.Unmarshal(body, &node) != nil {
		return s3ControlXMLNode{}
	}
	return node
}

// s3AccessGrantsNoInstance answers the error every Access Grants operation
// returns before the account has an instance.
func s3AccessGrantsNoInstance(w http.ResponseWriter) {
	s3ControlError(w, "AccessGrantsInstanceNotExistsError",
		"Access Grants Instance does not exist", http.StatusNotFound)
}

func handleS3CreateAccessGrantsInstance(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	if _, exists := s3AccessGrantsInstances.Get(account); exists {
		s3ControlError(w, "AccessGrantsInstanceAlreadyExistsError",
			"Access Grants Instance already exists", http.StatusConflict)
		return
	}
	// Every member of this request is optional, so a create with no body is a
	// valid create of an instance with no Identity Center association and no
	// tags — which is what the CLI sends when neither is given.
	body := s3ControlOptionalXMLBody(r)
	instance := S3AccessGrantsInstance{
		AccountID: account, InstanceID: "default",
		CreatedAt:         time.Now().UTC().Format(time.RFC3339),
		IdentityCenterArn: body.ChildText("IdentityCenterArn"),
		Tags:              s3ControlTagsFrom(body, "Tags", "Tag"),
	}
	if instance.IdentityCenterArn != "" {
		instance.IdentityCenterApplicationArn = s3AccessGrantsApplicationARN(account)
	}
	s3AccessGrantsInstances.Put(account, instance)
	s3WriteAccessGrantsInstance(w, "CreateAccessGrantsInstanceResult", instance)
}

// s3AccessGrantsApplicationARN is the IAM Identity Center application S3
// creates for an instance when one is associated. It is derived from the
// instance it belongs to, which is the only account-scoped identifier it has.
func s3AccessGrantsApplicationARN(account string) string {
	return fmt.Sprintf("arn:aws:sso::%s:application/ssoins-s3ag/apl-%s", account, account)
}

func s3WriteAccessGrantsInstance(w http.ResponseWriter, element string, instance S3AccessGrantsInstance) {
	type result struct {
		XMLName                      xml.Name
		CreatedAt                    string `xml:"CreatedAt"`
		AccessGrantsInstanceID       string `xml:"AccessGrantsInstanceId"`
		AccessGrantsInstanceArn      string `xml:"AccessGrantsInstanceArn"`
		IdentityCenterArn            string `xml:"IdentityCenterArn,omitempty"`
		IdentityCenterInstanceArn    string `xml:"IdentityCenterInstanceArn,omitempty"`
		IdentityCenterApplicationArn string `xml:"IdentityCenterApplicationArn,omitempty"`
	}
	WriteXML(w, http.StatusOK, result{
		XMLName:                      xml.Name{Local: element},
		CreatedAt:                    instance.CreatedAt,
		AccessGrantsInstanceID:       instance.InstanceID,
		AccessGrantsInstanceArn:      s3AccessGrantsInstanceARN(instance.AccountID),
		IdentityCenterArn:            instance.IdentityCenterArn,
		IdentityCenterInstanceArn:    instance.IdentityCenterArn,
		IdentityCenterApplicationArn: instance.IdentityCenterApplicationArn,
	})
}

func handleS3GetAccessGrantsInstance(w http.ResponseWriter, r *http.Request) {
	instance, ok := s3AccessGrantsInstances.Get(s3ControlAccountID(r))
	if !ok {
		s3AccessGrantsNoInstance(w)
		return
	}
	s3WriteAccessGrantsInstance(w, "GetAccessGrantsInstanceResult", instance)
}

func handleS3DeleteAccessGrantsInstance(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	// The instance owns its locations and grants; the service refuses to delete
	// one that still has them rather than orphaning access it granted.
	for _, grant := range s3AccessGrants.List() {
		if grant.AccountID == account {
			s3ControlError(w, "AccessGrantsInstanceNotEmptyError",
				"The Access Grants instance still has grants", http.StatusConflict)
			return
		}
	}
	for _, location := range s3AccessGrantsLocations.List() {
		if location.AccountID == account {
			s3ControlError(w, "AccessGrantsInstanceNotEmptyError",
				"The Access Grants instance still has locations", http.StatusConflict)
			return
		}
	}
	if !s3AccessGrantsInstances.Delete(account) {
		s3AccessGrantsNoInstance(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3ListAccessGrantsInstances(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	type entry struct {
		AccessGrantsInstanceID       string `xml:"AccessGrantsInstanceId"`
		AccessGrantsInstanceArn      string `xml:"AccessGrantsInstanceArn"`
		CreatedAt                    string `xml:"CreatedAt"`
		IdentityCenterArn            string `xml:"IdentityCenterArn,omitempty"`
		IdentityCenterInstanceArn    string `xml:"IdentityCenterInstanceArn,omitempty"`
		IdentityCenterApplicationArn string `xml:"IdentityCenterApplicationArn,omitempty"`
	}
	var items []entry
	if instance, ok := s3AccessGrantsInstances.Get(account); ok {
		items = append(items, entry{
			AccessGrantsInstanceID:       instance.InstanceID,
			AccessGrantsInstanceArn:      s3AccessGrantsInstanceARN(account),
			CreatedAt:                    instance.CreatedAt,
			IdentityCenterArn:            instance.IdentityCenterArn,
			IdentityCenterInstanceArn:    instance.IdentityCenterArn,
			IdentityCenterApplicationArn: instance.IdentityCenterApplicationArn,
		})
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"ListAccessGrantsInstancesResult"`
		Entries []entry  `xml:"AccessGrantsInstancesList>AccessGrantsInstance"`
	}{Entries: items})
}

func handleS3GetAccessGrantsInstanceForPrefix(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	prefix := r.URL.Query().Get("s3prefix")
	if prefix == "" {
		s3ControlError(w, "InvalidRequest", "s3prefix is required", http.StatusBadRequest)
		return
	}
	if _, ok := s3AccessGrantsInstances.Get(account); !ok {
		s3AccessGrantsNoInstance(w)
		return
	}
	// The instance answers only for a prefix one of its locations covers: a
	// prefix nothing manages belongs to no instance.
	if _, ok := s3AccessGrantsLocationFor(account, prefix); !ok {
		s3ControlError(w, "AccessGrantsLocationNotExistsError",
			"No Access Grants location covers "+prefix, http.StatusNotFound)
		return
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName                 xml.Name `xml:"GetAccessGrantsInstanceForPrefixResult"`
		AccessGrantsInstanceArn string   `xml:"AccessGrantsInstanceArn"`
		AccessGrantsInstanceID  string   `xml:"AccessGrantsInstanceId"`
	}{AccessGrantsInstanceArn: s3AccessGrantsInstanceARN(account), AccessGrantsInstanceID: "default"})
}

// s3AccessGrantsLocationFor finds the location whose scope covers a prefix,
// preferring the most specific one — the same way a grant is matched.
func s3AccessGrantsLocationFor(account, prefix string) (S3AccessGrantsLocation, bool) {
	var best S3AccessGrantsLocation
	found := false
	for _, location := range s3AccessGrantsLocations.List() {
		if location.AccountID != account {
			continue
		}
		if !s3AccessGrantsScopeCovers(location.LocationScope, prefix) {
			continue
		}
		if !found || len(location.LocationScope) > len(best.LocationScope) {
			best, found = location, true
		}
	}
	return best, found
}

// s3AccessGrantsScopeCovers reports whether a scope covers a target. The
// scope `s3://` covers everything; any other scope covers what it prefixes,
// with a trailing `*` matching the rest of the path.
func s3AccessGrantsScopeCovers(scope, target string) bool {
	if scope == "" {
		return false
	}
	if scope == "s3://" || scope == "s3://*" {
		return true
	}
	return strings.HasPrefix(target, strings.TrimSuffix(scope, "*"))
}

func handleS3AssociateAccessGrantsIdentityCenter(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	body, ok := s3ControlReadXMLBody(w, r, "AssociateAccessGrantsIdentityCenterRequest")
	if !ok {
		return
	}
	arn := body.ChildText("IdentityCenterArn")
	if arn == "" {
		s3ControlError(w, "InvalidRequest", "IdentityCenterArn is required", http.StatusBadRequest)
		return
	}
	if !s3AccessGrantsInstances.Update(account, func(instance *S3AccessGrantsInstance) {
		instance.IdentityCenterArn = arn
		instance.IdentityCenterApplicationArn = s3AccessGrantsApplicationARN(account)
	}) {
		s3AccessGrantsNoInstance(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3DissociateAccessGrantsIdentityCenter(w http.ResponseWriter, r *http.Request) {
	if !s3AccessGrantsInstances.Update(s3ControlAccountID(r), func(instance *S3AccessGrantsInstance) {
		instance.IdentityCenterArn = ""
		instance.IdentityCenterApplicationArn = ""
	}) {
		s3AccessGrantsNoInstance(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3PutAccessGrantsInstanceResourcePolicy(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	body, ok := s3ControlReadXMLBody(w, r, "PutAccessGrantsInstanceResourcePolicyRequest")
	if !ok {
		return
	}
	policy := body.ChildText("Policy")
	if policy == "" {
		s3ControlError(w, "InvalidRequest", "Policy is required", http.StatusBadRequest)
		return
	}
	created := time.Now().UTC().Format(time.RFC3339)
	organization := body.ChildText("Organization")
	if !s3AccessGrantsInstances.Update(account, func(instance *S3AccessGrantsInstance) {
		instance.Policy, instance.PolicyOrganization, instance.PolicyCreatedAt = policy, organization, created
	}) {
		s3AccessGrantsNoInstance(w)
		return
	}
	s3WriteAccessGrantsPolicy(w, "PutAccessGrantsInstanceResourcePolicyResult", policy, organization, created)
}

func s3WriteAccessGrantsPolicy(w http.ResponseWriter, element, policy, organization, created string) {
	type result struct {
		XMLName      xml.Name
		Policy       string `xml:"Policy,omitempty"`
		Organization string `xml:"Organization,omitempty"`
		CreatedAt    string `xml:"CreatedAt,omitempty"`
	}
	WriteXML(w, http.StatusOK, result{
		XMLName: xml.Name{Local: element}, Policy: policy,
		Organization: organization, CreatedAt: created,
	})
}

func handleS3GetAccessGrantsInstanceResourcePolicy(w http.ResponseWriter, r *http.Request) {
	instance, ok := s3AccessGrantsInstances.Get(s3ControlAccountID(r))
	if !ok {
		s3AccessGrantsNoInstance(w)
		return
	}
	s3WriteAccessGrantsPolicy(w, "GetAccessGrantsInstanceResourcePolicyResult",
		instance.Policy, instance.PolicyOrganization, instance.PolicyCreatedAt)
}

func handleS3DeleteAccessGrantsInstanceResourcePolicy(w http.ResponseWriter, r *http.Request) {
	if !s3AccessGrantsInstances.Update(s3ControlAccountID(r), func(instance *S3AccessGrantsInstance) {
		instance.Policy, instance.PolicyOrganization, instance.PolicyCreatedAt = "", "", ""
	}) {
		s3AccessGrantsNoInstance(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3CreateAccessGrantsLocation(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	if _, ok := s3AccessGrantsInstances.Get(account); !ok {
		s3AccessGrantsNoInstance(w)
		return
	}
	body, ok := s3ControlReadXMLBody(w, r, "CreateAccessGrantsLocationRequest")
	if !ok {
		return
	}
	scope, roleArn := body.ChildText("LocationScope"), body.ChildText("IAMRoleArn")
	if scope == "" || roleArn == "" {
		s3ControlError(w, "InvalidRequest",
			"LocationScope and IAMRoleArn are required", http.StatusBadRequest)
		return
	}
	// The role is what reaches the location's data; the service will not
	// register a location behind a role that does not exist.
	if _, ok := iamRoles.Get(iamRoleNameFromArn(roleArn)); !ok {
		s3ControlError(w, "InvalidRequest",
			"The role "+roleArn+" does not exist", http.StatusBadRequest)
		return
	}
	locationID := s3AccessGrantsDefaultLocationID
	if scope != "s3://" {
		locationID = s3ObjectLambdaID()[:16]
	}
	if _, exists := s3AccessGrantsLocations.Get(s3AccessPointKey(account, locationID)); exists {
		s3ControlError(w, "AccessGrantsLocationAlreadyExistsError",
			"The Access Grants location already exists", http.StatusConflict)
		return
	}
	location := S3AccessGrantsLocation{
		AccountID: account, LocationID: locationID, LocationScope: scope, IAMRoleArn: roleArn,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Tags:      s3ControlTagsFrom(body, "Tags", "Tag"),
	}
	s3AccessGrantsLocations.Put(s3AccessPointKey(account, locationID), location)
	s3WriteAccessGrantsLocation(w, "CreateAccessGrantsLocationResult", location)
}

func s3WriteAccessGrantsLocation(w http.ResponseWriter, element string, location S3AccessGrantsLocation) {
	type result struct {
		XMLName                 xml.Name
		CreatedAt               string `xml:"CreatedAt"`
		AccessGrantsLocationID  string `xml:"AccessGrantsLocationId"`
		AccessGrantsLocationArn string `xml:"AccessGrantsLocationArn"`
		LocationScope           string `xml:"LocationScope"`
		IAMRoleArn              string `xml:"IAMRoleArn"`
	}
	WriteXML(w, http.StatusOK, result{
		XMLName:                 xml.Name{Local: element},
		CreatedAt:               location.CreatedAt,
		AccessGrantsLocationID:  location.LocationID,
		AccessGrantsLocationArn: s3AccessGrantsLocationARN(location.AccountID, location.LocationID),
		LocationScope:           location.LocationScope,
		IAMRoleArn:              location.IAMRoleArn,
	})
}

func handleS3GetAccessGrantsLocation(w http.ResponseWriter, r *http.Request) {
	account, id := s3ControlAccountID(r), sim.PathParam(r, "locationId")
	location, ok := s3AccessGrantsLocations.Get(s3AccessPointKey(account, id))
	if !ok {
		s3ControlError(w, "AccessGrantsLocationNotExistsError",
			"The Access Grants location does not exist", http.StatusNotFound)
		return
	}
	s3WriteAccessGrantsLocation(w, "GetAccessGrantsLocationResult", location)
}

func handleS3UpdateAccessGrantsLocation(w http.ResponseWriter, r *http.Request) {
	account, id := s3ControlAccountID(r), sim.PathParam(r, "locationId")
	body, ok := s3ControlReadXMLBody(w, r, "UpdateAccessGrantsLocationRequest")
	if !ok {
		return
	}
	roleArn := body.ChildText("IAMRoleArn")
	if roleArn == "" {
		s3ControlError(w, "InvalidRequest", "IAMRoleArn is required", http.StatusBadRequest)
		return
	}
	if _, ok := iamRoles.Get(iamRoleNameFromArn(roleArn)); !ok {
		s3ControlError(w, "InvalidRequest",
			"The role "+roleArn+" does not exist", http.StatusBadRequest)
		return
	}
	if !s3AccessGrantsLocations.Update(s3AccessPointKey(account, id),
		func(l *S3AccessGrantsLocation) { l.IAMRoleArn = roleArn }) {
		s3ControlError(w, "AccessGrantsLocationNotExistsError",
			"The Access Grants location does not exist", http.StatusNotFound)
		return
	}
	location, _ := s3AccessGrantsLocations.Get(s3AccessPointKey(account, id))
	s3WriteAccessGrantsLocation(w, "UpdateAccessGrantsLocationResult", location)
}

func handleS3DeleteAccessGrantsLocation(w http.ResponseWriter, r *http.Request) {
	account, id := s3ControlAccountID(r), sim.PathParam(r, "locationId")
	// A location with grants in it is still handing out access; deleting it
	// would leave grants pointing at nothing.
	for _, grant := range s3AccessGrants.List() {
		if grant.AccountID == account && grant.LocationID == id {
			s3ControlError(w, "AccessGrantsLocationNotEmptyError",
				"The Access Grants location still has grants", http.StatusConflict)
			return
		}
	}
	if !s3AccessGrantsLocations.Delete(s3AccessPointKey(account, id)) {
		s3ControlError(w, "AccessGrantsLocationNotExistsError",
			"The Access Grants location does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3ListAccessGrantsLocations(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	scope := r.URL.Query().Get("locationscope")
	type entry struct {
		CreatedAt               string `xml:"CreatedAt"`
		AccessGrantsLocationID  string `xml:"AccessGrantsLocationId"`
		AccessGrantsLocationArn string `xml:"AccessGrantsLocationArn"`
		LocationScope           string `xml:"LocationScope"`
		IAMRoleArn              string `xml:"IAMRoleArn"`
	}
	var items []entry
	for _, location := range s3AccessGrantsLocations.List() {
		if location.AccountID != account {
			continue
		}
		if scope != "" && !s3AccessGrantsScopeCovers(location.LocationScope, scope) {
			continue
		}
		items = append(items, entry{
			CreatedAt: location.CreatedAt, AccessGrantsLocationID: location.LocationID,
			AccessGrantsLocationArn: s3AccessGrantsLocationARN(account, location.LocationID),
			LocationScope:           location.LocationScope, IAMRoleArn: location.IAMRoleArn,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AccessGrantsLocationID < items[j].AccessGrantsLocationID })
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"ListAccessGrantsLocationsResult"`
		Entries []entry  `xml:"AccessGrantsLocationsList>AccessGrantsLocation"`
	}{Entries: items})
}

func handleS3CreateAccessGrant(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	body, ok := s3ControlReadXMLBody(w, r, "CreateAccessGrantRequest")
	if !ok {
		return
	}
	locationID := body.ChildText("AccessGrantsLocationId")
	permission := body.ChildText("Permission")
	grantee, hasGrantee := body.Child("Grantee")
	if locationID == "" || permission == "" || !hasGrantee {
		s3ControlError(w, "InvalidRequest",
			"AccessGrantsLocationId, Grantee and Permission are required", http.StatusBadRequest)
		return
	}
	if !s3AccessGrantsPermissionValid(permission) {
		s3ControlError(w, "InvalidRequest",
			"Permission must be READ, WRITE or READWRITE", http.StatusBadRequest)
		return
	}
	location, ok := s3AccessGrantsLocations.Get(s3AccessPointKey(account, locationID))
	if !ok {
		s3ControlError(w, "AccessGrantsLocationNotExistsError",
			"The Access Grants location does not exist", http.StatusNotFound)
		return
	}
	subPrefix := ""
	if cfg, ok := body.Child("AccessGrantsLocationConfiguration"); ok {
		subPrefix = cfg.ChildText("S3SubPrefix")
	}
	grant := S3AccessGrant{
		AccountID: account, GrantID: s3ObjectLambdaID()[:16],
		CreatedAt: time.Now().UTC().Format(time.RFC3339), LocationID: locationID,
		S3SubPrefix:       subPrefix,
		GranteeType:       grantee.ChildText("GranteeType"),
		GranteeIdentifier: grantee.ChildText("GranteeIdentifier"),
		Permission:        permission,
		ApplicationArn:    body.ChildText("ApplicationArn"),
		S3PrefixType:      body.ChildText("S3PrefixType"),
		GrantScope:        s3AccessGrantScope(location.LocationScope, subPrefix),
		Tags:              s3ControlTagsFrom(body, "Tags", "Tag"),
	}
	s3AccessGrants.Put(s3AccessPointKey(account, grant.GrantID), grant)
	s3WriteAccessGrant(w, "CreateAccessGrantResult", grant)
}

func s3AccessGrantsPermissionValid(permission string) bool {
	switch permission {
	case "READ", "WRITE", "READWRITE":
		return true
	}
	return false
}

// s3AccessGrantScope is the prefix a grant actually covers: the location's
// scope, narrowed by the sub-prefix the grant names.
func s3AccessGrantScope(locationScope, subPrefix string) string {
	if subPrefix == "" {
		return locationScope
	}
	return strings.TrimSuffix(locationScope, "*") + strings.TrimPrefix(subPrefix, "/")
}

func s3WriteAccessGrant(w http.ResponseWriter, element string, grant S3AccessGrant) {
	type granteeXML struct {
		GranteeType       string `xml:"GranteeType,omitempty"`
		GranteeIdentifier string `xml:"GranteeIdentifier,omitempty"`
	}
	type locationConfig struct {
		S3SubPrefix string `xml:"S3SubPrefix,omitempty"`
	}
	type result struct {
		XMLName                           xml.Name
		CreatedAt                         string          `xml:"CreatedAt"`
		AccessGrantID                     string          `xml:"AccessGrantId"`
		AccessGrantArn                    string          `xml:"AccessGrantArn"`
		Grantee                           granteeXML      `xml:"Grantee"`
		AccessGrantsLocationID            string          `xml:"AccessGrantsLocationId"`
		AccessGrantsLocationConfiguration *locationConfig `xml:"AccessGrantsLocationConfiguration,omitempty"`
		Permission                        string          `xml:"Permission"`
		ApplicationArn                    string          `xml:"ApplicationArn,omitempty"`
		GrantScope                        string          `xml:"GrantScope"`
	}
	out := result{
		XMLName: xml.Name{Local: element}, CreatedAt: grant.CreatedAt,
		AccessGrantID:  grant.GrantID,
		AccessGrantArn: s3AccessGrantARN(grant.AccountID, grant.GrantID),
		Grantee: granteeXML{
			GranteeType: grant.GranteeType, GranteeIdentifier: grant.GranteeIdentifier,
		},
		AccessGrantsLocationID: grant.LocationID, Permission: grant.Permission,
		ApplicationArn: grant.ApplicationArn, GrantScope: grant.GrantScope,
	}
	if grant.S3SubPrefix != "" {
		out.AccessGrantsLocationConfiguration = &locationConfig{S3SubPrefix: grant.S3SubPrefix}
	}
	WriteXML(w, http.StatusOK, out)
}

func handleS3GetAccessGrant(w http.ResponseWriter, r *http.Request) {
	account, id := s3ControlAccountID(r), sim.PathParam(r, "grantId")
	grant, ok := s3AccessGrants.Get(s3AccessPointKey(account, id))
	if !ok {
		s3ControlError(w, "AccessGrantNotExistsError",
			"The Access Grant does not exist", http.StatusNotFound)
		return
	}
	s3WriteAccessGrant(w, "GetAccessGrantResult", grant)
}

func handleS3DeleteAccessGrant(w http.ResponseWriter, r *http.Request) {
	account, id := s3ControlAccountID(r), sim.PathParam(r, "grantId")
	if !s3AccessGrants.Delete(s3AccessPointKey(account, id)) {
		s3ControlError(w, "AccessGrantNotExistsError",
			"The Access Grant does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3ListAccessGrants(w http.ResponseWriter, r *http.Request) {
	account, q := s3ControlAccountID(r), r.URL.Query()
	type granteeXML struct {
		GranteeType       string `xml:"GranteeType,omitempty"`
		GranteeIdentifier string `xml:"GranteeIdentifier,omitempty"`
	}
	type entry struct {
		CreatedAt              string     `xml:"CreatedAt"`
		AccessGrantID          string     `xml:"AccessGrantId"`
		AccessGrantArn         string     `xml:"AccessGrantArn"`
		Grantee                granteeXML `xml:"Grantee"`
		Permission             string     `xml:"Permission"`
		AccessGrantsLocationID string     `xml:"AccessGrantsLocationId"`
		GrantScope             string     `xml:"GrantScope"`
		ApplicationArn         string     `xml:"ApplicationArn,omitempty"`
	}
	var items []entry
	for _, grant := range s3AccessGrants.List() {
		if grant.AccountID != account {
			continue
		}
		if v := q.Get("granteetype"); v != "" && v != grant.GranteeType {
			continue
		}
		if v := q.Get("granteeidentifier"); v != "" && v != grant.GranteeIdentifier {
			continue
		}
		if v := q.Get("permission"); v != "" && v != grant.Permission {
			continue
		}
		if v := q.Get("grantscope"); v != "" && !s3AccessGrantsScopeCovers(grant.GrantScope, v) {
			continue
		}
		if v := q.Get("application_arn"); v != "" && v != grant.ApplicationArn {
			continue
		}
		items = append(items, entry{
			CreatedAt: grant.CreatedAt, AccessGrantID: grant.GrantID,
			AccessGrantArn: s3AccessGrantARN(account, grant.GrantID),
			Grantee: granteeXML{
				GranteeType: grant.GranteeType, GranteeIdentifier: grant.GranteeIdentifier,
			},
			Permission: grant.Permission, AccessGrantsLocationID: grant.LocationID,
			GrantScope: grant.GrantScope, ApplicationArn: grant.ApplicationArn,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AccessGrantID < items[j].AccessGrantID })
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"ListAccessGrantsResult"`
		Entries []entry  `xml:"AccessGrantsList>AccessGrant"`
	}{Entries: items})
}

func handleS3ListCallerAccessGrants(w http.ResponseWriter, r *http.Request) {
	account, q := s3ControlAccountID(r), r.URL.Query()
	// The caller sees the grants made to the identity signing the request,
	// which is what makes this different from listing the instance's grants.
	caller := s3ControlCallerIdentifier(r)
	type entry struct {
		Permission     string `xml:"Permission"`
		GrantScope     string `xml:"GrantScope"`
		ApplicationArn string `xml:"ApplicationArn,omitempty"`
	}
	var items []entry
	for _, grant := range s3AccessGrants.List() {
		if grant.AccountID != account || grant.GranteeIdentifier != caller {
			continue
		}
		if v := q.Get("grantscope"); v != "" && !s3AccessGrantsScopeCovers(grant.GrantScope, v) {
			continue
		}
		if q.Get("allowedByApplication") == "true" && grant.ApplicationArn == "" {
			continue
		}
		items = append(items, entry{
			Permission: grant.Permission, GrantScope: grant.GrantScope,
			ApplicationArn: grant.ApplicationArn,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].GrantScope < items[j].GrantScope })
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"ListCallerAccessGrantsResult"`
		Entries []entry  `xml:"CallerAccessGrantsList>AccessGrant"`
	}{Entries: items})
}

// s3ControlCallerIdentifier is the identity the request is signed with, in the
// form a grantee is named by. An unsigned request has no identity, and every
// grant filter that uses this treats that as "no filter" rather than as a
// caller matching nothing.
func s3ControlCallerIdentifier(r *http.Request) string {
	principalArn, _, _, ok := iamPrincipalForAccessKey(iamAccessKeyIDFromRequest(r))
	if !ok {
		return ""
	}
	return principalArn
}

func handleS3GetDataAccess(w http.ResponseWriter, r *http.Request) {
	account, q := s3ControlAccountID(r), r.URL.Query()
	target, permission := q.Get("target"), q.Get("permission")
	if target == "" || permission == "" {
		s3ControlError(w, "InvalidRequest", "target and permission are required", http.StatusBadRequest)
		return
	}
	if _, ok := s3AccessGrantsInstances.Get(account); !ok {
		s3AccessGrantsNoInstance(w)
		return
	}
	caller := s3ControlCallerIdentifier(r)
	grant, ok := s3AccessGrantMatching(account, caller, target, permission)
	if !ok {
		s3ControlError(w, "AccessDenied",
			"No Access Grant permits "+permission+" on "+target, http.StatusForbidden)
		return
	}
	location, ok := s3AccessGrantsLocations.Get(s3AccessPointKey(account, grant.LocationID))
	if !ok {
		s3ControlError(w, "AccessGrantsLocationNotExistsError",
			"The Access Grants location does not exist", http.StatusNotFound)
		return
	}
	// The credentials are the location's role, assumed — the same session
	// credentials STS mints for any AssumeRole, so they authenticate on the S3
	// surface exactly as a real grantee's do.
	role, ok := iamRoles.Get(iamRoleNameFromArn(location.IAMRoleArn))
	if !ok {
		s3ControlError(w, "InvalidRequest",
			"The location's role "+location.IAMRoleArn+" does not exist", http.StatusBadRequest)
		return
	}
	duration := 3600
	if v := q.Get("durationSeconds"); v != "" {
		if n, err := time.ParseDuration(v + "s"); err == nil && n > 0 {
			duration = int(n.Seconds())
		}
	}
	akid, secret, token := stsMintTempCred()
	expiration := time.Now().UTC().Add(time.Duration(duration) * time.Second)
	assumedArn := fmt.Sprintf("arn:aws:sts::%s:assumed-role/%s/s3-access-grants", account, role.RoleName)
	iamTempCreds.Put(akid, IAMTempCred{
		AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token,
		RoleName: role.RoleName, PrincipalArn: assumedArn,
		Expiration: expiration.Format(time.RFC3339),
	})
	// A request made with these credentials was authorized through S3 Access
	// Grants, and s3:AccessGrantsInstanceArn names the instance that issued
	// them. It is a fact about the credential, so it is recorded with it.
	s3AccessGrantsCredentials.Put(akid, S3AccessGrantsCredential{
		AccessKeyID: akid,
		InstanceARN: s3AccessGrantsInstanceARN(account),
		GrantScope:  grant.GrantScope,
	})
	type credentials struct {
		AccessKeyID     string `xml:"AccessKeyId"`
		SecretAccessKey string `xml:"SecretAccessKey"`
		SessionToken    string `xml:"SessionToken"`
		Expiration      string `xml:"Expiration"`
	}
	type granteeXML struct {
		GranteeType       string `xml:"GranteeType,omitempty"`
		GranteeIdentifier string `xml:"GranteeIdentifier,omitempty"`
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName            xml.Name    `xml:"GetDataAccessResult"`
		Credentials        credentials `xml:"Credentials"`
		MatchedGrantTarget string      `xml:"MatchedGrantTarget"`
		Grantee            granteeXML  `xml:"Grantee"`
	}{
		Credentials: credentials{
			AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token,
			Expiration: expiration.Format(time.RFC3339),
		},
		MatchedGrantTarget: grant.GrantScope,
		Grantee: granteeXML{
			GranteeType: grant.GranteeType, GranteeIdentifier: grant.GranteeIdentifier,
		},
	})
}

// s3AccessGrantMatching finds the grant that admits a caller to a target,
// preferring the most specific scope. A grant for READWRITE satisfies a READ
// or a WRITE request; the narrower permissions satisfy only themselves.
func s3AccessGrantMatching(account, caller, target, permission string) (S3AccessGrant, bool) {
	var best S3AccessGrant
	found := false
	for _, grant := range s3AccessGrants.List() {
		if grant.AccountID != account {
			continue
		}
		if caller != "" && grant.GranteeIdentifier != caller {
			continue
		}
		if grant.Permission != permission && grant.Permission != "READWRITE" {
			continue
		}
		if !s3AccessGrantsScopeCovers(grant.GrantScope, target) {
			continue
		}
		if !found || len(grant.GrantScope) > len(best.GrantScope) {
			best, found = grant, true
		}
	}
	return best, found
}
