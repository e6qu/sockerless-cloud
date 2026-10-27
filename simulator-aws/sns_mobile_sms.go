package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon SNS mobile push, SMS and data protection: platform applications and
// endpoints, the SMS sandbox, account SMS attributes, the opt-out list,
// origination numbers and per-topic data-protection policies, over SNS's
// awsQuery envelope.

// SNSPlatformApplication is a mobile-push platform application
// (arn:aws:sns:<region>:<account>:app/<platform>/<name>). Attributes hold
// PlatformCredential, EventEndpointCreated, Enabled and the rest
// GetPlatformApplicationAttributes reads.
type SNSPlatformApplication struct {
	ARN        string
	Name       string
	Platform   string
	Attributes map[string]string
}

// SNSPlatformEndpoint is a device endpoint under a platform application
// (…:endpoint/<platform>/<name>/<uuid>).
type SNSPlatformEndpoint struct {
	ARN                    string
	PlatformApplicationARN string
	Token                  string
	Attributes             map[string]string
}

// SNSSandboxPhoneNumber is an SMS sandbox number, Pending until the carrier
// delivers its one-time password and Verified after.
type SNSSandboxPhoneNumber struct {
	PhoneNumber  string
	LanguageCode string
	Status       string // "Pending" | "Verified"
}

// SNSOriginationNumber is a number the account sends SMS from, which ListOriginationNumbers lists.
type SNSOriginationNumber struct {
	PhoneNumber  string
	Iso2Country  string
	RouteType    string
	Capabilities []string
	Status       string
	CreatedAt    time.Time
}

var (
	snsPlatformApps      sim.Store[SNSPlatformApplication]
	snsPlatformEndpts    sim.Store[SNSPlatformEndpoint]
	snsSandboxNumbers    sim.Store[SNSSandboxPhoneNumber]
	snsOptedOutNumbers   sim.Store[snsOptOutEntry]
	snsOriginationNums   sim.Store[SNSOriginationNumber]
	snsAccountSMSAttrs   sim.Store[snsKV]
	snsSandboxAccount    sim.Store[snsKV]
	snsTopicDataPolicies sim.Store[snsKV]
)

// snsKV is one keyed string, for the account singletons (SMS attributes, sandbox
// status) and the per-topic data-protection policies.
type snsKV struct {
	Key   string
	Value string
}

type snsOptOutEntry struct {
	PhoneNumber string
}

// An account starts in the SMS sandbox and leaves it only through an AWS support
// request, which no API exposes.
const snsSandboxAccountKey = "account"

func registerSNSMobileSMS(r *AWSQueryRouter, srv *sim.Server) {
	snsPlatformApps = sim.MakeStore[SNSPlatformApplication](srv.DB(), "sns_platform_apps")
	snsPlatformEndpts = sim.MakeStore[SNSPlatformEndpoint](srv.DB(), "sns_platform_endpoints")
	snsSandboxNumbers = sim.MakeStore[SNSSandboxPhoneNumber](srv.DB(), "sns_sandbox_numbers")
	snsOptedOutNumbers = sim.MakeStore[snsOptOutEntry](srv.DB(), "sns_opted_out")
	snsOriginationNums = sim.MakeStore[SNSOriginationNumber](srv.DB(), "sns_origination_numbers")
	snsAccountSMSAttrs = sim.MakeStore[snsKV](srv.DB(), "sns_sms_attributes")
	snsSandboxAccount = sim.MakeStore[snsKV](srv.DB(), "sns_sandbox_account")
	snsTopicDataPolicies = sim.MakeStore[snsKV](srv.DB(), "sns_data_protection_policies")

	r.RegisterVersioned(snsAPIVersion, "CreatePlatformApplication", handleSNSCreatePlatformApplication)
	r.RegisterVersioned(snsAPIVersion, "DeletePlatformApplication", handleSNSDeletePlatformApplication)
	r.RegisterVersioned(snsAPIVersion, "GetPlatformApplicationAttributes", handleSNSGetPlatformApplicationAttributes)
	r.RegisterVersioned(snsAPIVersion, "SetPlatformApplicationAttributes", handleSNSSetPlatformApplicationAttributes)
	r.RegisterVersioned(snsAPIVersion, "ListPlatformApplications", handleSNSListPlatformApplications)

	r.RegisterVersioned(snsAPIVersion, "CreatePlatformEndpoint", handleSNSCreatePlatformEndpoint)
	r.RegisterVersioned(snsAPIVersion, "DeleteEndpoint", handleSNSDeleteEndpoint)
	r.RegisterVersioned(snsAPIVersion, "GetEndpointAttributes", handleSNSGetEndpointAttributes)
	r.RegisterVersioned(snsAPIVersion, "SetEndpointAttributes", handleSNSSetEndpointAttributes)
	r.RegisterVersioned(snsAPIVersion, "ListEndpointsByPlatformApplication", handleSNSListEndpointsByPlatformApplication)

	r.RegisterVersioned(snsAPIVersion, "CreateSMSSandboxPhoneNumber", handleSNSCreateSMSSandboxPhoneNumber)
	r.RegisterVersioned(snsAPIVersion, "DeleteSMSSandboxPhoneNumber", handleSNSDeleteSMSSandboxPhoneNumber)
	r.RegisterVersioned(snsAPIVersion, "VerifySMSSandboxPhoneNumber", handleSNSVerifySMSSandboxPhoneNumber)
	r.RegisterVersioned(snsAPIVersion, "ListSMSSandboxPhoneNumbers", handleSNSListSMSSandboxPhoneNumbers)
	r.RegisterVersioned(snsAPIVersion, "GetSMSSandboxAccountStatus", handleSNSGetSMSSandboxAccountStatus)

	r.RegisterVersioned(snsAPIVersion, "GetSMSAttributes", handleSNSGetSMSAttributes)
	r.RegisterVersioned(snsAPIVersion, "SetSMSAttributes", handleSNSSetSMSAttributes)
	r.RegisterVersioned(snsAPIVersion, "CheckIfPhoneNumberIsOptedOut", handleSNSCheckIfPhoneNumberIsOptedOut)
	r.RegisterVersioned(snsAPIVersion, "ListPhoneNumbersOptedOut", handleSNSListPhoneNumbersOptedOut)
	r.RegisterVersioned(snsAPIVersion, "OptInPhoneNumber", handleSNSOptInPhoneNumber)
	r.RegisterVersioned(snsAPIVersion, "ListOriginationNumbers", handleSNSListOriginationNumbers)

	r.RegisterVersioned(snsAPIVersion, "PutDataProtectionPolicy", handleSNSPutDataProtectionPolicy)
	r.RegisterVersioned(snsAPIVersion, "GetDataProtectionPolicy", handleSNSGetDataProtectionPolicy)
}

// snsAttributesMap parses the awsQuery-flattened Attributes map
// (<field>.entry.N.key / .value) into a Go map.
func snsAttributesMap(r *http.Request, field string) map[string]string {
	out := map[string]string{}
	for i := 1; i <= 100; i++ {
		k := r.FormValue(fmt.Sprintf("%s.entry.%d.key", field, i))
		if k == "" {
			break
		}
		out[k] = r.FormValue(fmt.Sprintf("%s.entry.%d.value", field, i))
	}
	return out
}

// snsAttributesXML renders a map into the <Attributes><entry><key>/<value>
// shape the awsQuery map deserializer expects.
func snsAttributesXML(b *strings.Builder, wrapper string, attrs map[string]string) {
	fmt.Fprintf(b, "<%s>", wrapper)
	for k, v := range attrs {
		fmt.Fprintf(b, "<entry><key>%s</key><value>%s</value></entry>",
			xmlEscape(k), xmlEscape(v))
	}
	fmt.Fprintf(b, "</%s>", wrapper)
}

func snsPlatformApplicationARN(platform, name string) string {
	return fmt.Sprintf("arn:aws:sns:%s:%s:app/%s/%s",
		awsRegion(), awsAccountID(), platform, name)
}

func handleSNSCreatePlatformApplication(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	name := r.FormValue("Name")
	platform := r.FormValue("Platform")
	if name == "" || platform == "" {
		snsErrorXML(w, "InvalidParameter", "Name and Platform are required", http.StatusBadRequest, reqID)
		return
	}
	arn := snsPlatformApplicationARN(platform, name)
	app := SNSPlatformApplication{
		ARN:        arn,
		Name:       name,
		Platform:   platform,
		Attributes: snsAttributesMap(r, "Attributes"),
	}
	if app.Attributes == nil {
		app.Attributes = map[string]string{}
	}
	// Enabled defaults to true on a new application, as GetPlatformApplicationAttributes reports.
	if _, ok := app.Attributes["Enabled"]; !ok {
		app.Attributes["Enabled"] = "true"
	}
	snsPlatformApps.Put(arn, app)
	body := fmt.Sprintf(
		"<CreatePlatformApplicationResult><PlatformApplicationArn>%s</PlatformApplicationArn></CreatePlatformApplicationResult>",
		xmlEscape(arn))
	snsXMLResponse(w, "CreatePlatformApplication", body, reqID)
}

func handleSNSDeletePlatformApplication(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("PlatformApplicationArn")
	// DeletePlatformApplication succeeds even when the application is absent.
	snsPlatformApps.Delete(arn)
	for _, ep := range snsPlatformEndpts.List() {
		if ep.PlatformApplicationARN == arn {
			snsPlatformEndpts.Delete(ep.ARN)
		}
	}
	snsXMLResponse(w, "DeletePlatformApplication", "", sim.RequestID(r.Context()))
}

func handleSNSGetPlatformApplicationAttributes(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	arn := r.FormValue("PlatformApplicationArn")
	app, ok := snsPlatformApps.Get(arn)
	if !ok {
		snsErrorXML(w, "NotFound", "PlatformApplication does not exist", http.StatusNotFound, reqID)
		return
	}
	var b strings.Builder
	b.WriteString("<GetPlatformApplicationAttributesResult>")
	snsAttributesXML(&b, "Attributes", app.Attributes)
	b.WriteString("</GetPlatformApplicationAttributesResult>")
	snsXMLResponse(w, "GetPlatformApplicationAttributes", b.String(), reqID)
}

func handleSNSSetPlatformApplicationAttributes(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	arn := r.FormValue("PlatformApplicationArn")
	app, ok := snsPlatformApps.Get(arn)
	if !ok {
		snsErrorXML(w, "NotFound", "PlatformApplication does not exist", http.StatusNotFound, reqID)
		return
	}
	for k, v := range snsAttributesMap(r, "Attributes") {
		if app.Attributes == nil {
			app.Attributes = map[string]string{}
		}
		app.Attributes[k] = v
	}
	snsPlatformApps.Put(arn, app)
	snsXMLResponse(w, "SetPlatformApplicationAttributes", "", reqID)
}

func handleSNSListPlatformApplications(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	token := r.FormValue("NextToken")
	all := snsPlatformApps.List()
	page, next, pageOK := awsKeyPage(w, snsBadToken(r), all, func(it SNSPlatformApplication) string { return it.ARN }, token, 0, 100)
	if !pageOK {
		return
	}

	var b strings.Builder
	b.WriteString("<ListPlatformApplicationsResult><PlatformApplications>")
	for _, a := range page {
		b.WriteString("<member>")
		fmt.Fprintf(&b, "<PlatformApplicationArn>%s</PlatformApplicationArn>", xmlEscape(a.ARN))
		snsAttributesXML(&b, "Attributes", a.Attributes)
		b.WriteString("</member>")
	}
	b.WriteString("</PlatformApplications>")
	if next != "" {
		fmt.Fprintf(&b, "<NextToken>%s</NextToken>", xmlEscape(next))
	}
	b.WriteString("</ListPlatformApplicationsResult>")
	snsXMLResponse(w, "ListPlatformApplications", b.String(), sim.RequestID(r.Context()))
}

// snsPlatformEndpointARN derives an endpoint ARN from its application's by
// swapping the `app/` segment for `endpoint/` and appending a UUID, SNS's
// endpoint ARN shape.
func snsPlatformEndpointARN(appARN string) string {
	base := strings.Replace(appARN, ":app/", ":endpoint/", 1)
	return fmt.Sprintf("%s/%s", base, sim.NewUUID())
}

func handleSNSCreatePlatformEndpoint(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	appARN := r.FormValue("PlatformApplicationArn")
	token := r.FormValue("Token")
	if appARN == "" || token == "" {
		snsErrorXML(w, "InvalidParameter", "PlatformApplicationArn and Token are required", http.StatusBadRequest, reqID)
		return
	}
	if _, ok := snsPlatformApps.Get(appARN); !ok {
		snsErrorXML(w, "NotFound", "PlatformApplication does not exist", http.StatusNotFound, reqID)
		return
	}
	// CreatePlatformEndpoint is idempotent on (application, token): a registered
	// token returns its existing endpoint ARN.
	for _, ep := range snsPlatformEndpts.List() {
		if ep.PlatformApplicationARN == appARN && ep.Token == token {
			body := fmt.Sprintf(
				"<CreatePlatformEndpointResult><EndpointArn>%s</EndpointArn></CreatePlatformEndpointResult>",
				xmlEscape(ep.ARN))
			snsXMLResponse(w, "CreatePlatformEndpoint", body, reqID)
			return
		}
	}
	attrs := snsAttributesMap(r, "Attributes")
	if attrs == nil {
		attrs = map[string]string{}
	}
	attrs["Token"] = token
	if cud := r.FormValue("CustomUserData"); cud != "" {
		attrs["CustomUserData"] = cud
	}
	if _, ok := attrs["Enabled"]; !ok {
		attrs["Enabled"] = "true"
	}
	ep := SNSPlatformEndpoint{
		ARN:                    snsPlatformEndpointARN(appARN),
		PlatformApplicationARN: appARN,
		Token:                  token,
		Attributes:             attrs,
	}
	snsPlatformEndpts.Put(ep.ARN, ep)
	body := fmt.Sprintf(
		"<CreatePlatformEndpointResult><EndpointArn>%s</EndpointArn></CreatePlatformEndpointResult>",
		xmlEscape(ep.ARN))
	snsXMLResponse(w, "CreatePlatformEndpoint", body, reqID)
}

func handleSNSDeleteEndpoint(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("EndpointArn")
	// DeleteEndpoint succeeds even when the endpoint is absent.
	snsPlatformEndpts.Delete(arn)
	snsXMLResponse(w, "DeleteEndpoint", "", sim.RequestID(r.Context()))
}

func handleSNSGetEndpointAttributes(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	arn := r.FormValue("EndpointArn")
	ep, ok := snsPlatformEndpts.Get(arn)
	if !ok {
		snsErrorXML(w, "NotFound", "Endpoint does not exist", http.StatusNotFound, reqID)
		return
	}
	var b strings.Builder
	b.WriteString("<GetEndpointAttributesResult>")
	snsAttributesXML(&b, "Attributes", ep.Attributes)
	b.WriteString("</GetEndpointAttributesResult>")
	snsXMLResponse(w, "GetEndpointAttributes", b.String(), reqID)
}

func handleSNSSetEndpointAttributes(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	arn := r.FormValue("EndpointArn")
	ep, ok := snsPlatformEndpts.Get(arn)
	if !ok {
		snsErrorXML(w, "NotFound", "Endpoint does not exist", http.StatusNotFound, reqID)
		return
	}
	for k, v := range snsAttributesMap(r, "Attributes") {
		if ep.Attributes == nil {
			ep.Attributes = map[string]string{}
		}
		ep.Attributes[k] = v
	}
	snsPlatformEndpts.Put(arn, ep)
	snsXMLResponse(w, "SetEndpointAttributes", "", reqID)
}

func handleSNSListEndpointsByPlatformApplication(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	appARN := r.FormValue("PlatformApplicationArn")
	if _, ok := snsPlatformApps.Get(appARN); !ok {
		snsErrorXML(w, "NotFound", "PlatformApplication does not exist", http.StatusNotFound, reqID)
		return
	}
	matching := snsPlatformEndpts.Filter(func(ep SNSPlatformEndpoint) bool {
		return ep.PlatformApplicationARN == appARN
	})
	page, next, pageOK := awsKeyPage(w, snsBadToken(r), matching, func(it SNSPlatformEndpoint) string { return it.ARN }, r.FormValue("NextToken"), 0, 100)
	if !pageOK {
		return
	}

	var b strings.Builder
	b.WriteString("<ListEndpointsByPlatformApplicationResult><Endpoints>")
	for _, ep := range page {
		b.WriteString("<member>")
		fmt.Fprintf(&b, "<EndpointArn>%s</EndpointArn>", xmlEscape(ep.ARN))
		snsAttributesXML(&b, "Attributes", ep.Attributes)
		b.WriteString("</member>")
	}
	b.WriteString("</Endpoints>")
	if next != "" {
		fmt.Fprintf(&b, "<NextToken>%s</NextToken>", xmlEscape(next))
	}
	b.WriteString("</ListEndpointsByPlatformApplicationResult>")
	snsXMLResponse(w, "ListEndpointsByPlatformApplication", b.String(), reqID)
}

func handleSNSCreateSMSSandboxPhoneNumber(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	phone := r.FormValue("PhoneNumber")
	if phone == "" {
		snsErrorXML(w, "InvalidParameter", "PhoneNumber is required", http.StatusBadRequest, reqID)
		return
	}
	// The sandbox verifies a number by sending it a one-time password over SMS,
	// which needs the carrier Publish needs; failing here keeps a derivable or
	// logged code from standing in for a delivered message.
	_ = reqID
	snsExternalDeliveryUnavailable(w, r, snsSMSDeliveryReason)
}

func handleSNSDeleteSMSSandboxPhoneNumber(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	phone := r.FormValue("PhoneNumber")
	if !snsSandboxNumbers.Delete(phone) {
		snsErrorXML(w, "ResourceNotFound",
			fmt.Sprintf("Destination phone number %s not found", phone),
			http.StatusNotFound, reqID)
		return
	}
	snsXMLResponse(w, "DeleteSMSSandboxPhoneNumber", "<DeleteSMSSandboxPhoneNumberResult/>", reqID)
}

func handleSNSVerifySMSSandboxPhoneNumber(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	phone := r.FormValue("PhoneNumber")
	_, ok := snsSandboxNumbers.Get(phone)
	if !ok {
		snsErrorXML(w, "ResourceNotFound",
			fmt.Sprintf("Destination phone number %s not found", phone),
			http.StatusNotFound, reqID)
		return
	}
	snsExternalDeliveryUnavailable(w, r, snsSMSDeliveryReason)
}

func handleSNSListSMSSandboxPhoneNumbers(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	all := snsSandboxNumbers.List()
	page, next, pageOK := awsKeyPage(w, snsBadToken(r), all, func(it SNSSandboxPhoneNumber) string { return it.PhoneNumber }, r.FormValue("NextToken"), snsMaxResults(r), 0)
	if !pageOK {
		return
	}

	var b strings.Builder
	b.WriteString("<ListSMSSandboxPhoneNumbersResult><PhoneNumbers>")
	for _, n := range page {
		fmt.Fprintf(&b,
			"<member><PhoneNumber>%s</PhoneNumber><Status>%s</Status></member>",
			xmlEscape(n.PhoneNumber), xmlEscape(n.Status))
	}
	b.WriteString("</PhoneNumbers>")
	if next != "" {
		fmt.Fprintf(&b, "<NextToken>%s</NextToken>", xmlEscape(next))
	}
	b.WriteString("</ListSMSSandboxPhoneNumbersResult>")
	snsXMLResponse(w, "ListSMSSandboxPhoneNumbers", b.String(), sim.RequestID(r.Context()))
}

// snsMaxResults reads an optional MaxResults query param, defaulting to 100.
func snsMaxResults(r *http.Request) int {
	v := r.FormValue("MaxResults")
	if v == "" {
		return 100
	}
	n := 0
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return 100
	}
	return n
}

func handleSNSGetSMSSandboxAccountStatus(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	inSandbox := "true"
	if kv, ok := snsSandboxAccount.Get(snsSandboxAccountKey); ok && kv.Value == "false" {
		inSandbox = "false"
	}
	body := fmt.Sprintf(
		"<GetSMSSandboxAccountStatusResult><IsInSandbox>%s</IsInSandbox></GetSMSSandboxAccountStatusResult>",
		inSandbox)
	snsXMLResponse(w, "GetSMSSandboxAccountStatus", body, reqID)
}

// snsSMSAttributeList parses the GetSMSAttributes `attributes.member.N` list of
// requested attribute names (empty means "all").
func snsSMSAttributeList(r *http.Request) []string {
	var out []string
	for i := 1; ; i++ {
		v := r.FormValue(fmt.Sprintf("attributes.member.%d", i))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

func handleSNSGetSMSAttributes(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	requested := snsSMSAttributeList(r)
	want := map[string]bool{}
	for _, k := range requested {
		want[k] = true
	}
	attrs := map[string]string{}
	for _, kv := range snsAccountSMSAttrs.List() {
		if len(want) == 0 || want[kv.Key] {
			attrs[kv.Key] = kv.Value
		}
	}
	var b strings.Builder
	b.WriteString("<GetSMSAttributesResult>")
	// The GetSMSAttributes deserializer keys on the lowercase `attributes`
	// wrapper, unlike the other attribute maps.
	snsAttributesXML(&b, "attributes", attrs)
	b.WriteString("</GetSMSAttributesResult>")
	snsXMLResponse(w, "GetSMSAttributes", b.String(), sim.RequestID(r.Context()))
}

func handleSNSSetSMSAttributes(w http.ResponseWriter, r *http.Request) {
	for k, v := range snsAttributesMap(r, "attributes") {
		snsAccountSMSAttrs.Put(k, snsKV{Key: k, Value: v})
	}
	snsXMLResponse(w, "SetSMSAttributes", "<SetSMSAttributesResult/>", sim.RequestID(r.Context()))
}

func handleSNSCheckIfPhoneNumberIsOptedOut(w http.ResponseWriter, r *http.Request) {
	phone := r.FormValue("phoneNumber")
	_, optedOut := snsOptedOutNumbers.Get(phone)
	body := fmt.Sprintf(
		"<CheckIfPhoneNumberIsOptedOutResult><isOptedOut>%t</isOptedOut></CheckIfPhoneNumberIsOptedOutResult>",
		optedOut)
	snsXMLResponse(w, "CheckIfPhoneNumberIsOptedOut", body, sim.RequestID(r.Context()))
}

func handleSNSListPhoneNumbersOptedOut(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	all := snsOptedOutNumbers.List()
	page, next, pageOK := awsKeyPage(w, snsBadToken(r), all, func(it snsOptOutEntry) string { return it.PhoneNumber }, r.FormValue("nextToken"), 0, 100)
	if !pageOK {
		return
	}

	var b strings.Builder
	b.WriteString("<ListPhoneNumbersOptedOutResult><phoneNumbers>")
	for _, e := range page {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(e.PhoneNumber))
	}
	b.WriteString("</phoneNumbers>")
	if next != "" {
		fmt.Fprintf(&b, "<nextToken>%s</nextToken>", xmlEscape(next))
	}
	b.WriteString("</ListPhoneNumbersOptedOutResult>")
	snsXMLResponse(w, "ListPhoneNumbersOptedOut", b.String(), sim.RequestID(r.Context()))
}

func handleSNSOptInPhoneNumber(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	phone := r.FormValue("phoneNumber")
	if phone == "" {
		snsErrorXML(w, "InvalidParameter", "phoneNumber is required", http.StatusBadRequest, reqID)
		return
	}
	// OptInPhoneNumber removes the number from the opt-out list; SNS succeeds whether or not it was opted out.
	snsOptedOutNumbers.Delete(phone)
	snsXMLResponse(w, "OptInPhoneNumber", "<OptInPhoneNumberResult/>", reqID)
}

func handleSNSListOriginationNumbers(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	all := snsOriginationNums.List()
	page, next, pageOK := awsKeyPage(w, snsBadToken(r), all, func(it SNSOriginationNumber) string { return it.PhoneNumber }, r.FormValue("NextToken"), snsMaxResults(r), 0)
	if !pageOK {
		return
	}

	var b strings.Builder
	b.WriteString("<ListOriginationNumbersResult><PhoneNumbers>")
	for _, n := range page {
		b.WriteString("<member>")
		fmt.Fprintf(&b, "<CreatedAt>%s</CreatedAt>", n.CreatedAt.UTC().Format(time.RFC3339))
		fmt.Fprintf(&b, "<PhoneNumber>%s</PhoneNumber>", xmlEscape(n.PhoneNumber))
		fmt.Fprintf(&b, "<Iso2CountryCode>%s</Iso2CountryCode>", xmlEscape(n.Iso2Country))
		fmt.Fprintf(&b, "<RouteType>%s</RouteType>", xmlEscape(n.RouteType))
		fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(n.Status))
		b.WriteString("<NumberCapabilities>")
		for _, c := range n.Capabilities {
			fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(c))
		}
		b.WriteString("</NumberCapabilities>")
		b.WriteString("</member>")
	}
	b.WriteString("</PhoneNumbers>")
	if next != "" {
		fmt.Fprintf(&b, "<NextToken>%s</NextToken>", xmlEscape(next))
	}
	b.WriteString("</ListOriginationNumbersResult>")
	snsXMLResponse(w, "ListOriginationNumbers", b.String(), sim.RequestID(r.Context()))
}

func handleSNSPutDataProtectionPolicy(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	arn := r.FormValue("ResourceArn")
	policy := r.FormValue("DataProtectionPolicy")
	if arn == "" {
		snsErrorXML(w, "InvalidParameter", "ResourceArn is required", http.StatusBadRequest, reqID)
		return
	}
	name := snsTopicNameFromARN(arn)
	if _, ok := snsTopics.Get(name); !ok {
		snsErrorXML(w, "NotFound", "Topic does not exist", http.StatusNotFound, reqID)
		return
	}
	if policy == "" {
		snsTopicDataPolicies.Delete(arn)
	} else {
		snsTopicDataPolicies.Put(arn, snsKV{Key: arn, Value: policy})
	}
	snsXMLResponse(w, "PutDataProtectionPolicy", "", reqID)
}

func handleSNSGetDataProtectionPolicy(w http.ResponseWriter, r *http.Request) {
	reqID := sim.RequestID(r.Context())
	arn := r.FormValue("ResourceArn")
	name := snsTopicNameFromARN(arn)
	if _, ok := snsTopics.Get(name); !ok {
		snsErrorXML(w, "NotFound", "Topic does not exist", http.StatusNotFound, reqID)
		return
	}
	policy := ""
	if kv, ok := snsTopicDataPolicies.Get(arn); ok {
		policy = kv.Value
	}
	body := fmt.Sprintf(
		"<GetDataProtectionPolicyResult><DataProtectionPolicy>%s</DataProtectionPolicy></GetDataProtectionPolicyResult>",
		xmlEscape(policy))
	snsXMLResponse(w, "GetDataProtectionPolicy", body, reqID)
}

// SNS delivers SMS through a telecommunications carrier and mobile push through
// Apple's and Google's push hosts; no AWS API configures either, so the
// simulator has nothing to point at. Every path that would reach one fails and
// names the missing external dependency.
const (
	snsSMSDeliveryReason = "Amazon SNS delivers SMS through a telecommunications carrier. " +
		"A carrier is not an AWS-configurable coordinate — no AWS API provisions one — so this " +
		"simulator has no carrier to hand the message to and will not manufacture a delivery. " +
		"Everything up to the carrier is implemented: subscriptions, attributes, opt-outs and " +
		"origination numbers all behave, and only the hand-off is impossible."

	snsMobilePushDeliveryReason = "Amazon SNS delivers mobile push through Apple's and Google's " +
		"own push hosts. Those hosts are not AWS-configurable coordinates, so this simulator has " +
		"nowhere to send the notification and will not manufacture a delivery. The credential " +
		"half is real: platform applications and endpoints are created, stored and returned as " +
		"the API defines them, and only the hand-off to Apple or Google is impossible."
)

// snsExternalDeliveryUnavailable answers a request that would reach an external
// provider with InternalError and the reason. EndpointDisabled would claim a
// disabled endpoint, and a 503 would make the SDK retry a permanent condition.
func snsExternalDeliveryUnavailable(w http.ResponseWriter, r *http.Request, reason string) {
	snsErrorXML(w, "InternalError", reason, http.StatusInternalServerError, sim.RequestID(r.Context()))
}
