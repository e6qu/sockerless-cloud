package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// The Cloud Quotas API (cloudquotas-v1 and cloudquotas-v1beta discovery
// documents): the QuotaInfo of every quota the simulator enforces, and the
// QuotaPreference resources that set a project's value for one. A quota's
// value in effect is the granted value of the preference for its dimensions,
// else of the preference that names none of them, else the quota's default
// (cloudquotas-api-overview.md.gz, "Quota preference is required").

// cloudQuotaDefinition is one quota a service defines and the simulator
// enforces.
type cloudQuotaDefinition struct {
	service           string
	quotaID           string
	metric            string
	metricDisplayName string
	quotaDisplayName  string
	metricUnit        string
	dimensions        []string
	defaultValue      int64
	// usage is the consumption the quota limits in a project and region,
	// which QUOTA_DECREASE_BELOW_USAGE compares a decrease against.
	usage func(project, region string) int64
}

// cloudQuotaCatalog holds the quotas the simulator enforces. Cloud Run's
// regional CPU allocation (cloudrun-quotas.md.gz) is the
// run.googleapis.com/cpu_allocation metric, in milli vCPU, per project and
// region. The page gives its default only as "Depends on selected region", so
// the default is the most the page's other limits let a region allocate: 100
// instances per project and region, of at most 8 vCPU each.
var cloudQuotaCatalog = []*cloudQuotaDefinition{{
	service:           cloudRunQuotaService,
	quotaID:           cloudRunCPUQuotaID,
	metric:            "run.googleapis.com/cpu_allocation",
	metricDisplayName: "CPU allocation, in milli vCPU",
	quotaDisplayName:  "Total CPU, in milli vCPU, allocated across all instances per project per region",
	metricUnit:        "1/{project}/{region}",
	dimensions:        []string{"region"},
	defaultValue:      100 * 8 * 1000,
	usage:             cloudRunAllocatedCPU,
}}

const (
	cloudRunQuotaService = "run.googleapis.com"
	cloudRunCPUQuotaID   = "CpuAllocPerProjectRegion"
)

func cloudQuotaLookup(service, quotaID string) *cloudQuotaDefinition {
	for _, d := range cloudQuotaCatalog {
		if d.service == service && d.quotaID == quotaID {
			return d
		}
	}
	return nil
}

// cloudQuotaInt64 is an int64 in protobuf JSON: a string on output, a string
// or a number on input, as protojson accepts both.
type cloudQuotaInt64 int64

func (v cloudQuotaInt64) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatInt(int64(v), 10))
}

func (v *cloudQuotaInt64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid int64 value %s", b)
	}
	*v = cloudQuotaInt64(n)
	return nil
}

// QuotaConfig is a quota preference's preferred and granted values.
type QuotaConfig struct {
	PreferredValue cloudQuotaInt64   `json:"preferredValue"`
	StateDetail    string            `json:"stateDetail,omitempty"`
	GrantedValue   *cloudQuotaInt64  `json:"grantedValue,omitempty"`
	TraceID        string            `json:"traceId,omitempty"`
	Annotations    map[string]string `json:"annotations,omitempty"`
	RequestOrigin  string            `json:"requestOrigin,omitempty"`
}

// QuotaPreference is the stored form of a Cloud Quotas QuotaPreference.
// contactEmail is input only, so the store never holds it.
type QuotaPreference struct {
	Name          string            `json:"name"`
	Dimensions    map[string]string `json:"dimensions,omitempty"`
	QuotaConfig   QuotaConfig       `json:"quotaConfig"`
	Etag          string            `json:"etag,omitempty"`
	CreateTime    string            `json:"createTime,omitempty"`
	UpdateTime    string            `json:"updateTime,omitempty"`
	Service       string            `json:"service"`
	QuotaID       string            `json:"quotaId"`
	Reconciling   bool              `json:"reconciling,omitempty"`
	Justification string            `json:"justification,omitempty"`
}

// cloudQuotaPreferenceInput is what a create or patch request carries: the
// output-only members are read and dropped, and requestOrigin may come as the
// enum's number from a client that encodes enums as integers.
type cloudQuotaPreferenceInput struct {
	Dimensions  map[string]string `json:"dimensions"`
	QuotaConfig *struct {
		PreferredValue *cloudQuotaInt64  `json:"preferredValue"`
		Annotations    map[string]string `json:"annotations"`
	} `json:"quotaConfig"`
	Etag          string `json:"etag"`
	Service       string `json:"service"`
	QuotaID       string `json:"quotaId"`
	Justification string `json:"justification"`
	ContactEmail  string `json:"contactEmail"`
}

var cloudQuotaPreferences sim.Store[QuotaPreference]

func registerCloudQuotas(srv *sim.Server) {
	cloudQuotaPreferences = sim.MakeStore[QuotaPreference](srv.DB(), "cloudquotas_preferences")

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/quotaPreferences", handleCreateQuotaPreference)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/quotaPreferences", handleListQuotaPreferences)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/quotaPreferences/{preference}", handleGetQuotaPreference)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/quotaPreferences/{preference}", handlePatchQuotaPreference)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/services/{service}/quotaInfos", handleListQuotaInfos)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/services/{service}/quotaInfos/{quotaInfo}", handleGetQuotaInfo)

	srv.HandleFunc("POST /v1beta/projects/{project}/locations/{location}/quotaPreferences", handleCreateQuotaPreference)
	srv.HandleFunc("GET /v1beta/projects/{project}/locations/{location}/quotaPreferences", handleListQuotaPreferences)
	srv.HandleFunc("GET /v1beta/projects/{project}/locations/{location}/quotaPreferences/{preference}", handleGetQuotaPreference)
	srv.HandleFunc("PATCH /v1beta/projects/{project}/locations/{location}/quotaPreferences/{preference}", handlePatchQuotaPreference)
	srv.HandleFunc("GET /v1beta/projects/{project}/locations/{location}/services/{service}/quotaInfos", handleListQuotaInfos)
	srv.HandleFunc("GET /v1beta/projects/{project}/locations/{location}/services/{service}/quotaInfos/{quotaInfo}", handleGetQuotaInfo)

	srv.HandleFunc("GET /v1/folders/{folder}/locations/{location}/{rest...}", cloudQuotaFolderOrOrganizationUnserved)
	srv.HandleFunc("POST /v1/folders/{folder}/locations/{location}/{rest...}", cloudQuotaFolderOrOrganizationUnserved)
	srv.HandleFunc("PATCH /v1/folders/{folder}/locations/{location}/{rest...}", cloudQuotaFolderOrOrganizationUnserved)
	srv.HandleFunc("GET /v1/organizations/{organization}/locations/{location}/{rest...}", cloudQuotaFolderOrOrganizationUnserved)
	srv.HandleFunc("POST /v1/organizations/{organization}/locations/{location}/{rest...}", cloudQuotaFolderOrOrganizationUnserved)
	srv.HandleFunc("PATCH /v1/organizations/{organization}/locations/{location}/{rest...}", cloudQuotaFolderOrOrganizationUnserved)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/quotaAdjusterSettings", cloudQuotaAdjusterUnserved)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/quotaAdjusterSettings", cloudQuotaAdjusterUnserved)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/gdcZones/{rest...}", cloudQuotaGDCUnserved)

	srv.HandleFunc("GET /v1beta/folders/{folder}/locations/{location}/{rest...}", cloudQuotaFolderOrOrganizationUnserved)
	srv.HandleFunc("POST /v1beta/folders/{folder}/locations/{location}/{rest...}", cloudQuotaFolderOrOrganizationUnserved)
	srv.HandleFunc("PATCH /v1beta/folders/{folder}/locations/{location}/{rest...}", cloudQuotaFolderOrOrganizationUnserved)
	srv.HandleFunc("GET /v1beta/organizations/{organization}/locations/{location}/{rest...}", cloudQuotaFolderOrOrganizationUnserved)
	srv.HandleFunc("POST /v1beta/organizations/{organization}/locations/{location}/{rest...}", cloudQuotaFolderOrOrganizationUnserved)
	srv.HandleFunc("PATCH /v1beta/organizations/{organization}/locations/{location}/{rest...}", cloudQuotaFolderOrOrganizationUnserved)
	srv.HandleFunc("GET /v1beta/projects/{project}/locations/{location}/quotaAdjusterSettings", cloudQuotaAdjusterUnserved)
	srv.HandleFunc("PATCH /v1beta/projects/{project}/locations/{location}/quotaAdjusterSettings", cloudQuotaAdjusterUnserved)
	srv.HandleFunc("GET /v1beta/projects/{project}/locations/{location}/gdcZones/{rest...}", cloudQuotaGDCUnserved)
}

// The Cloud Quotas resources the simulator declares unserved: folder- and
// organization-level quotas, of which the simulator enforces none; the quota
// adjuster, which acts on the usage history it predicts from; and the quotas
// of Google Distributed Cloud zones.
func cloudQuotaFolderOrOrganizationUnserved(w http.ResponseWriter, _ *http.Request) {
	GCPError(w, http.StatusNotImplemented, "the simulator serves no folder- or organization-level Cloud Quotas resources: it enforces no quota at those levels", "UNIMPLEMENTED")
}

func cloudQuotaAdjusterUnserved(w http.ResponseWriter, _ *http.Request) {
	GCPError(w, http.StatusNotImplemented, "the simulator serves no Cloud Quotas quota adjuster: the adjuster acts on predictions from usage history the simulator does not keep", "UNIMPLEMENTED")
}

func cloudQuotaGDCUnserved(w http.ResponseWriter, _ *http.Request) {
	GCPError(w, http.StatusNotImplemented, "the simulator serves no Google Distributed Cloud quota infos: it runs no Google Distributed Cloud zone", "UNIMPLEMENTED")
}

// cloudQuotaContainer resolves the request's project, by ID or number, to the
// number Cloud Quotas names its resources under, and requires the global
// location every Cloud Quotas resource name carries.
func cloudQuotaContainer(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, ok := crmResolveProject(sim.PathParam(r, "project"))
	if !ok || p.State != "ACTIVE" {
		crmProjectPermissionDenied(w)
		return "", false
	}
	if location := sim.PathParam(r, "location"); location != "global" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "the location of a Cloud Quotas resource must be global, not %q", location)
		return "", false
	}
	return p.Name, true
}

// cloudQuotaProjectName resolves a project ID or number to the project's
// resource name, projects/{number}, which keys its quota preferences.
func cloudQuotaProjectName(ref string) string {
	if p, ok := crmResolveProject(ref); ok {
		return p.Name
	}
	return "projects/" + ref
}

func cloudQuotaPreferenceName(container, id string) string {
	return container + "/locations/global/quotaPreferences/" + id
}

// cloudQuotaPreferencesOf are a container's preferences for one quota.
func cloudQuotaPreferencesOf(container string, def *cloudQuotaDefinition) []QuotaPreference {
	var out []QuotaPreference
	for _, kp := range cloudQuotaPreferences.ListPrefix(container + "/locations/global/quotaPreferences/") {
		if kp.Item.Service == def.service && kp.Item.QuotaID == def.quotaID {
			out = append(out, kp.Item)
		}
	}
	return out
}

// cloudQuotaGranted is a preference's granted value, -1 for unlimited.
func cloudQuotaGranted(p QuotaPreference) int64 {
	if p.QuotaConfig.GrantedValue == nil {
		return int64(p.QuotaConfig.PreferredValue)
	}
	return int64(*p.QuotaConfig.GrantedValue)
}

// cloudQuotaEffective is the value in effect for dims in a container: the
// preference for exactly those dimensions, else the one for none of them,
// else the default. Of two preferences for one combination the latest
// written is in effect, since the API is declarative.
func cloudQuotaEffective(container string, def *cloudQuotaDefinition, dims map[string]string, except string) int64 {
	var exact, general *QuotaPreference
	for _, p := range cloudQuotaPreferencesOf(container, def) {
		if p.Name == except {
			continue
		}
		switch {
		case cloudQuotaSameDimensions(p.Dimensions, dims):
			if exact == nil || p.UpdateTime > exact.UpdateTime {
				exact = &p
			}
		case len(p.Dimensions) == 0:
			if general == nil || p.UpdateTime > general.UpdateTime {
				general = &p
			}
		}
	}
	switch {
	case exact != nil:
		return cloudQuotaGranted(*exact)
	case general != nil:
		return cloudQuotaGranted(*general)
	}
	return def.defaultValue
}

func cloudQuotaSameDimensions(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// cloudQuotaLimit is the value in effect for def in a project's region, by
// project ID or number; -1 is unlimited.
func cloudQuotaLimit(def *cloudQuotaDefinition, project, region string) int64 {
	return cloudQuotaEffective(cloudQuotaProjectName(project), def, map[string]string{"region": region}, "")
}

// cloudQuotaSafetyChecks are the checks a request may waive, by the enum's
// name and number.
var cloudQuotaSafetyChecks = map[string]string{
	"QUOTA_SAFETY_CHECK_UNSPECIFIED": "QUOTA_SAFETY_CHECK_UNSPECIFIED", "0": "QUOTA_SAFETY_CHECK_UNSPECIFIED",
	"QUOTA_DECREASE_BELOW_USAGE": "QUOTA_DECREASE_BELOW_USAGE", "1": "QUOTA_DECREASE_BELOW_USAGE",
	"QUOTA_DECREASE_PERCENTAGE_TOO_HIGH": "QUOTA_DECREASE_PERCENTAGE_TOO_HIGH", "2": "QUOTA_DECREASE_PERCENTAGE_TOO_HIGH",
}

func cloudQuotaIgnoredChecks(w http.ResponseWriter, r *http.Request) (map[string]bool, bool) {
	ignored := map[string]bool{}
	for _, raw := range r.URL.Query()["ignoreSafetyChecks"] {
		for _, v := range strings.Split(raw, ",") {
			check, ok := cloudQuotaSafetyChecks[strings.TrimSpace(v)]
			if !ok {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid value for ignoreSafetyChecks: %q", v)
				return nil, false
			}
			ignored[check] = true
		}
	}
	return ignored, true
}

// cloudQuotaValidate checks a preference against the quota it names: a
// service and quota the catalog holds, dimensions the quota is defined on, and
// a preferred value of at least -1.
func cloudQuotaValidate(w http.ResponseWriter, p QuotaPreference) (*cloudQuotaDefinition, bool) {
	if p.Service == "" || p.QuotaID == "" {
		GCPError(w, http.StatusBadRequest, "service and quotaId are required", "INVALID_ARGUMENT")
		return nil, false
	}
	def := cloudQuotaLookup(p.Service, p.QuotaID)
	if def == nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "quota %q is not defined in service %q", p.QuotaID, p.Service)
		return nil, false
	}
	for k := range p.Dimensions {
		if !slices.Contains(def.dimensions, k) {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "quota %s of %s is not defined on dimension %q", p.QuotaID, p.Service, k)
			return nil, false
		}
	}
	if p.QuotaConfig.PreferredValue < -1 {
		GCPError(w, http.StatusBadRequest, "quotaConfig.preferredValue must be greater than or equal to -1", "INVALID_ARGUMENT")
		return nil, false
	}
	return def, true
}

// cloudQuotaUsage is a quota's usage for dims: the region's usage for a
// preference that names one, else the largest in any region it governs.
func cloudQuotaUsage(def *cloudQuotaDefinition, container string, dims map[string]string) int64 {
	if region := dims["region"]; region != "" {
		return def.usage(container, region)
	}
	var most int64
	for _, region := range cloudRunAllocatedRegions(container) {
		if cloudQuotaEffective(container, def, map[string]string{"region": region}, "") == cloudQuotaEffective(container, def, nil, "") {
			most = max(most, def.usage(container, region))
		}
	}
	return most
}

// cloudQuotaAdmit applies the safety checks the discovery document's
// ignoreSafetyChecks enum describes, and grants the preference. A decrease is
// fulfilled at once, as gcloud's help for `quotas preferences` states; an
// increase needs a contact email, as quotaPreference.contactEmail states.
// Google's evaluation of an increase is not disclosed
// (cloudquotas-overview.md.gz), and the simulator grants it in full.
func cloudQuotaAdmit(w http.ResponseWriter, r *http.Request, def *cloudQuotaDefinition, container string, pref *QuotaPreference, contactEmail string) bool {
	ignored, ok := cloudQuotaIgnoredChecks(w, r)
	if !ok {
		return false
	}
	current := cloudQuotaEffective(container, def, pref.Dimensions, pref.Name)
	if existing, found := cloudQuotaPreferences.Get(pref.Name); found {
		current = cloudQuotaGranted(existing)
	}
	preferred := int64(pref.QuotaConfig.PreferredValue)
	increase := current != -1 && (preferred == -1 || preferred > current)
	decrease := preferred != -1 && (current == -1 || preferred < current)
	if increase && contactEmail == "" {
		GCPError(w, http.StatusBadRequest, "contactEmail is required to request a quota increase", "INVALID_ARGUMENT")
		return false
	}
	if decrease && !ignored["QUOTA_DECREASE_PERCENTAGE_TOO_HIGH"] && (current == -1 || preferred*10 < current*9) {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"the preferred value %d decreases the effective limit %s by more than 10 percent; set ignoreSafetyChecks to QUOTA_DECREASE_PERCENTAGE_TOO_HIGH to apply it", preferred, cloudQuotaValueText(current))
		return false
	}
	if decrease && !ignored["QUOTA_DECREASE_BELOW_USAGE"] {
		if usage := cloudQuotaUsage(def, container, pref.Dimensions); preferred < usage {
			GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
				"the preferred value %d is below the quota usage %d; set ignoreSafetyChecks to QUOTA_DECREASE_BELOW_USAGE to apply it", preferred, usage)
			return false
		}
	}
	granted := pref.QuotaConfig.PreferredValue
	pref.QuotaConfig.GrantedValue = &granted
	pref.QuotaConfig.RequestOrigin = "ORIGIN_UNSPECIFIED"
	pref.QuotaConfig.StateDetail = ""
	pref.QuotaConfig.TraceID = ""
	if increase {
		pref.QuotaConfig.TraceID = sim.RandomHex(16)
	}
	pref.Reconciling = false
	return true
}

func cloudQuotaValueText(v int64) string {
	if v == -1 {
		return "unlimited"
	}
	return strconv.FormatInt(v, 10)
}

func handleCreateQuotaPreference(w http.ResponseWriter, r *http.Request) {
	container, ok := cloudQuotaContainer(w, r)
	if !ok {
		return
	}
	var in cloudQuotaPreferenceInput
	if err := sim.ReadJSON(r, &in); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	id := r.URL.Query().Get("quotaPreferenceId")
	if id == "" {
		id = sim.NewUUID()
	}
	pref, ok := cloudQuotaFromInput(w, in)
	if !ok {
		return
	}
	pref.Name = cloudQuotaPreferenceName(container, id)
	if _, exists := cloudQuotaPreferences.Get(pref.Name); exists {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "QuotaPreference %s already exists", pref.Name)
		return
	}
	if !cloudQuotaStore(w, r, container, pref, in.ContactEmail, "") {
		return
	}
}

// cloudQuotaFromInput builds a preference from a request body that sets every
// member.
func cloudQuotaFromInput(w http.ResponseWriter, in cloudQuotaPreferenceInput) (QuotaPreference, bool) {
	if in.QuotaConfig == nil || in.QuotaConfig.PreferredValue == nil {
		GCPError(w, http.StatusBadRequest, "quotaConfig.preferredValue is required", "INVALID_ARGUMENT")
		return QuotaPreference{}, false
	}
	return QuotaPreference{
		Dimensions:    in.Dimensions,
		QuotaConfig:   QuotaConfig{PreferredValue: *in.QuotaConfig.PreferredValue, Annotations: in.QuotaConfig.Annotations},
		Service:       in.Service,
		QuotaID:       in.QuotaID,
		Justification: in.Justification,
	}, true
}

// cloudQuotaStore validates, admits and, unless the request only validates,
// stores pref, answering with the preference as it stands.
func cloudQuotaStore(w http.ResponseWriter, r *http.Request, container string, pref QuotaPreference, contactEmail, createTime string) bool {
	def, ok := cloudQuotaValidate(w, pref)
	if !ok {
		return false
	}
	if !cloudQuotaAdmit(w, r, def, container, &pref, contactEmail) {
		return false
	}
	now := nowTimestamp()
	pref.CreateTime = createTime
	if pref.CreateTime == "" {
		pref.CreateTime = now
	}
	pref.UpdateTime = now
	pref.Etag = sim.NewUUID()
	if r.URL.Query().Get("validateOnly") != "true" {
		cloudQuotaPreferences.Put(pref.Name, pref)
	}
	sim.WriteJSON(w, http.StatusOK, pref)
	return true
}

func handleGetQuotaPreference(w http.ResponseWriter, r *http.Request) {
	container, ok := cloudQuotaContainer(w, r)
	if !ok {
		return
	}
	name := cloudQuotaPreferenceName(container, sim.PathParam(r, "preference"))
	pref, found := cloudQuotaPreferences.Get(name)
	if !found {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "QuotaPreference %s not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, pref)
}

// cloudQuotaMaskFields are the members an updateMask path names, in the
// camelCase or snake_case spelling, keyed by the top-level member.
var cloudQuotaMaskFields = map[string]string{
	"name": "name", "service": "service", "quotaId": "quotaId", "quota_id": "quotaId",
	"quotaConfig": "quotaConfig", "quota_config": "quotaConfig",
	"dimensions": "dimensions", "justification": "justification",
	"contactEmail": "contactEmail", "contact_email": "contactEmail", "etag": "etag",
}

func handlePatchQuotaPreference(w http.ResponseWriter, r *http.Request) {
	container, ok := cloudQuotaContainer(w, r)
	if !ok {
		return
	}
	name := cloudQuotaPreferenceName(container, sim.PathParam(r, "preference"))
	var in cloudQuotaPreferenceInput
	if err := sim.ReadJSON(r, &in); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	existing, found := cloudQuotaPreferences.Get(name)
	if !found {
		if r.URL.Query().Get("allowMissing") != "true" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "QuotaPreference %s not found", name)
			return
		}
		pref, ok := cloudQuotaFromInput(w, in)
		if !ok {
			return
		}
		pref.Name = name
		cloudQuotaStore(w, r, container, pref, in.ContactEmail, "")
		return
	}
	if in.Etag != "" && in.Etag != existing.Etag {
		GCPErrorf(w, http.StatusConflict, "ABORTED", "the etag %q does not match the current etag of QuotaPreference %s", in.Etag, name)
		return
	}
	fields := map[string]bool{}
	if mask := r.URL.Query().Get("updateMask"); mask != "" {
		for _, path := range strings.Split(mask, ",") {
			top, _, _ := strings.Cut(strings.TrimSpace(path), ".")
			field, known := cloudQuotaMaskFields[top]
			if !known {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid updateMask path %q", path)
				return
			}
			fields[field] = true
		}
	} else {
		for _, field := range cloudQuotaMaskFields {
			fields[field] = true
		}
	}
	updated := existing
	if fields["service"] && in.Service != "" {
		updated.Service = in.Service
	}
	if fields["quotaId"] && in.QuotaID != "" {
		updated.QuotaID = in.QuotaID
	}
	if fields["dimensions"] {
		updated.Dimensions = in.Dimensions
	}
	if updated.Service != existing.Service || updated.QuotaID != existing.QuotaID || !cloudQuotaSameDimensions(updated.Dimensions, existing.Dimensions) {
		GCPError(w, http.StatusBadRequest, "the service, quotaId and dimensions of a QuotaPreference are immutable", "INVALID_ARGUMENT")
		return
	}
	updated.Dimensions = existing.Dimensions
	if fields["quotaConfig"] {
		if in.QuotaConfig == nil || in.QuotaConfig.PreferredValue == nil {
			GCPError(w, http.StatusBadRequest, "quotaConfig.preferredValue is required", "INVALID_ARGUMENT")
			return
		}
		updated.QuotaConfig.PreferredValue = *in.QuotaConfig.PreferredValue
		updated.QuotaConfig.Annotations = in.QuotaConfig.Annotations
	}
	if fields["justification"] {
		updated.Justification = in.Justification
	}
	// contactEmail is input only: the store never holds it, so the request
	// carries it whatever its updateMask names.
	cloudQuotaStore(w, r, container, updated, in.ContactEmail, existing.CreateTime)
}

func handleListQuotaPreferences(w http.ResponseWriter, r *http.Request) {
	container, ok := cloudQuotaContainer(w, r)
	if !ok {
		return
	}
	filter, err := gcpParseFilterExpr(r.URL.Query().Get("filter"))
	if err != nil {
		GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
		return
	}
	var prefs []QuotaPreference
	for _, kp := range cloudQuotaPreferences.ListPrefix(container + "/locations/global/quotaPreferences/") {
		doc, err := listq.ToDoc(kp.Item)
		if err != nil {
			GCPError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
			return
		}
		// protobuf JSON leaves a false reconciling out, and a filter on it
		// compares the field's value, false.
		if _, set := doc["reconciling"]; !set {
			doc["reconciling"] = false
		}
		if filter.Eval(cloudQuotaSnakeAliases(doc)) {
			prefs = append(prefs, kp.Item)
		}
	}
	q := r.URL.Query()
	if order := q.Get("orderBy"); order != "" {
		q.Set("orderBy", cloudQuotaCamelCase(order))
		r.URL.RawQuery = q.Encode()
	}
	page, next, ok := paginateList(w, r, prefs,
		gcpAsc(func(p QuotaPreference) string { return p.CreateTime }),
		gcpAsc(func(p QuotaPreference) string { return p.Name }))
	if !ok {
		return
	}
	if page == nil {
		page = []QuotaPreference{}
	}
	resp := map[string]any{"quotaPreferences": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// cloudQuotaSnakeAliases adds each member's snake_case spelling to doc, the
// spelling the API's own filter examples use (request_type, quota_id).
func cloudQuotaSnakeAliases(doc listq.Doc) listq.Doc {
	for k, v := range doc {
		var b strings.Builder
		for _, c := range k {
			if unicode.IsUpper(c) {
				b.WriteByte('_')
				c = unicode.ToLower(c)
			}
			b.WriteRune(c)
		}
		if snake := b.String(); snake != k {
			doc[snake] = v
		}
	}
	return doc
}

// cloudQuotaCamelCase spells the fields an orderBy names, quota_id or
// create_time, as the JSON members they sort by.
func cloudQuotaCamelCase(s string) string {
	var b strings.Builder
	upper := false
	for _, c := range s {
		switch {
		case c == '_':
			upper = true
			continue
		case upper:
			c = unicode.ToUpper(c)
		}
		upper = false
		b.WriteRune(c)
	}
	return b.String()
}

// cloudQuotaInfo renders a quota's QuotaInfo for a container: the
// per-region values its preferences set, most specific first, then the value
// in effect wherever no preference names the region.
func cloudQuotaInfo(container string, def *cloudQuotaDefinition) map[string]any {
	var infos []map[string]any
	regions := map[string]bool{}
	for _, p := range cloudQuotaPreferencesOf(container, def) {
		if region := p.Dimensions["region"]; region != "" && !regions[region] {
			regions[region] = true
		}
	}
	sorted := make([]string, 0, len(regions))
	for region := range regions {
		sorted = append(sorted, region)
	}
	slices.Sort(sorted)
	for _, region := range sorted {
		dims := map[string]string{"region": region}
		infos = append(infos, map[string]any{
			"dimensions":          dims,
			"details":             map[string]any{"value": strconv.FormatInt(cloudQuotaEffective(container, def, dims, ""), 10)},
			"applicableLocations": []string{region},
		})
	}
	infos = append(infos, map[string]any{
		"dimensions": map[string]string{},
		"details":    map[string]any{"value": strconv.FormatInt(cloudQuotaEffective(container, def, nil, ""), 10)},
	})
	return map[string]any{
		"name":                   container + "/locations/global/services/" + def.service + "/quotaInfos/" + def.quotaID,
		"quotaId":                def.quotaID,
		"metric":                 def.metric,
		"service":                def.service,
		"containerType":          "PROJECT",
		"dimensions":             def.dimensions,
		"dimensionsInfos":        infos,
		"serviceRequestQuotaUri": "https://console.cloud.google.com/iam-admin/quotas",
		"metricDisplayName":      def.metricDisplayName,
		"quotaDisplayName":       def.quotaDisplayName,
		"metricUnit":             def.metricUnit,
		"quotaIncreaseEligibility": map[string]any{
			"isEligible":          true,
			"ineligibilityReason": "INELIGIBILITY_REASON_UNSPECIFIED",
		},
	}
}

func handleListQuotaInfos(w http.ResponseWriter, r *http.Request) {
	container, ok := cloudQuotaContainer(w, r)
	if !ok {
		return
	}
	service := sim.PathParam(r, "service")
	infos := []map[string]any{}
	for _, def := range cloudQuotaCatalog {
		if def.service == service {
			infos = append(infos, cloudQuotaInfo(container, def))
		}
	}
	page, next, ok := paginateList(w, r, infos, gcpAsc(gcpField("name")))
	if !ok {
		return
	}
	resp := map[string]any{"quotaInfos": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleGetQuotaInfo(w http.ResponseWriter, r *http.Request) {
	container, ok := cloudQuotaContainer(w, r)
	if !ok {
		return
	}
	service, quotaID := sim.PathParam(r, "service"), sim.PathParam(r, "quotaInfo")
	def := cloudQuotaLookup(service, quotaID)
	if def == nil {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "QuotaInfo %s/locations/global/services/%s/quotaInfos/%s not found", container, service, quotaID)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cloudQuotaInfo(container, def))
}
