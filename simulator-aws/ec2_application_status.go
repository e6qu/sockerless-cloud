package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon EC2 application status checks: operator-defined health checks the
// platform runs against instances, and the per-instance application status
// they produce.
//
// The check and its associations are control-plane state, stored and returned
// as the API defines them. The status is measured, not declared: describing
// an instance's application status probes the check's protocol, port and path
// against the instance's own address, exactly as the Elastic Load Balancing
// health checker in this simulator probes its targets. An instance that is
// not running is unhealthy without a probe — there is nothing to probe — and
// a suppressed instance reports its suppression, not a verdict.

type EC2ApplicationStatusCheck struct {
	ApplicationStatusCheckId  string   `json:"applicationStatusCheckId"`
	Protocol                  string   `json:"protocol"`
	Port                      int      `json:"port"`
	Path                      string   `json:"path,omitempty"`
	Interval                  int      `json:"interval,omitempty"`
	Timeout                   int      `json:"timeout,omitempty"`
	FailureThreshold          int      `json:"failureThreshold,omitempty"`
	SuccessThreshold          int      `json:"successThreshold,omitempty"`
	StatusCodeMatcher         string   `json:"statusCodeMatcher,omitempty"`
	InitializationGracePeriod int      `json:"initializationGracePeriodSeconds,omitempty"`
	Tags                      []EC2Tag `json:"tags,omitempty"`
}

// EC2ApplicationStatusCheckAssociation binds a check to one instance. The
// model assigns associations no identifier of their own — disassociation
// names the check and the instance — so the store key is that pair.
type EC2ApplicationStatusCheckAssociation struct {
	ApplicationStatusCheckId string  `json:"applicationStatusCheckId"`
	InstanceId               string  `json:"instanceId"`
	Suppressed               bool    `json:"suppressed"`
	AssociatedAt             float64 `json:"associatedAt"`
}

func ec2AppStatusAssociationKey(checkID, instanceID string) string {
	return checkID + "/" + instanceID
}

var (
	ec2AppStatusChecks       sim.Store[EC2ApplicationStatusCheck]
	ec2AppStatusAssociations sim.Store[EC2ApplicationStatusCheckAssociation]
)

func registerEC2ApplicationStatus(r *AWSQueryRouter, srv *sim.Server) {
	ec2AppStatusChecks = sim.MakeStore[EC2ApplicationStatusCheck](srv.DB(), "ec2_app_status_checks")
	ec2AppStatusAssociations = sim.MakeStore[EC2ApplicationStatusCheckAssociation](srv.DB(), "ec2_app_status_associations")

	r.Register("CreateApplicationStatusCheck", handleCreateApplicationStatusCheck)
	r.Register("DescribeApplicationStatusChecks", handleDescribeApplicationStatusChecks)
	r.Register("ModifyApplicationStatusCheck", handleModifyApplicationStatusCheck)
	r.Register("DeleteApplicationStatusCheck", handleDeleteApplicationStatusCheck)
	r.Register("AssociateApplicationStatusCheck", handleAssociateApplicationStatusCheck)
	r.Register("DisassociateApplicationStatusCheck", handleDisassociateApplicationStatusCheck)
	r.Register("DescribeApplicationStatusCheckAssociations", handleDescribeApplicationStatusCheckAssociations)
	r.Register("DescribeApplicationStatus", handleDescribeApplicationStatus)
	r.Register("EnableApplicationStatusCheckSuppression", handleEnableApplicationStatusCheckSuppression)
	r.Register("DisableApplicationStatusCheckSuppression", handleDisableApplicationStatusCheckSuppression)
}

func appStatusCheckXML(check EC2ApplicationStatusCheck) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<applicationStatusCheckId>%s</applicationStatusCheckId>", check.ApplicationStatusCheckId)
	fmt.Fprintf(&b, "<protocol>%s</protocol><port>%d</port>", check.Protocol, check.Port)
	if check.Path != "" {
		fmt.Fprintf(&b, "<path>%s</path>", xmlEscape(check.Path))
	}
	if check.Interval > 0 {
		fmt.Fprintf(&b, "<interval>%d</interval>", check.Interval)
	}
	if check.Timeout > 0 {
		fmt.Fprintf(&b, "<timeout>%d</timeout>", check.Timeout)
	}
	if check.StatusCodeMatcher != "" {
		fmt.Fprintf(&b, "<statusCodeMatcher>%s</statusCodeMatcher>", xmlEscape(check.StatusCodeMatcher))
	}
	b.WriteString(writeTagSetXML(check.Tags))
	return b.String()
}

func handleCreateApplicationStatusCheck(w http.ResponseWriter, r *http.Request) {
	// The model's NetworkProtocolEnum admits exactly http and https: an
	// application status check is an HTTP check by definition.
	protocol := strings.ToLower(r.FormValue("Protocol"))
	if protocol == "" {
		protocol = "http"
	}
	if protocol != "http" && protocol != "https" {
		ec2ErrorXML(w, "InvalidParameterValue",
			fmt.Sprintf("Protocol must be http or https; %q is not a value NetworkProtocolEnum admits", protocol),
			http.StatusBadRequest)
		return
	}
	port := atoiDefault(r.FormValue("Port"), 0)
	if port <= 0 {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter Port", http.StatusBadRequest)
		return
	}
	check := EC2ApplicationStatusCheck{
		ApplicationStatusCheckId:  ec2ID("app-status-check"),
		Protocol:                  protocol,
		Port:                      port,
		Path:                      r.FormValue("Path"),
		Interval:                  atoiDefault(r.FormValue("Interval"), 30),
		Timeout:                   atoiDefault(r.FormValue("Timeout"), 5),
		FailureThreshold:          atoiDefault(r.FormValue("FailureThreshold"), 3),
		SuccessThreshold:          atoiDefault(r.FormValue("SuccessThreshold"), 2),
		StatusCodeMatcher:         r.FormValue("StatusCodeMatcher"),
		InitializationGracePeriod: atoiDefault(r.FormValue("InitializationGracePeriodSeconds"), 0),
		Tags:                      parseTags(r),
	}
	ec2AppStatusChecks.Put(check.ApplicationStatusCheckId, check)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateApplicationStatusCheckResponse %s>
  <requestId>%s</requestId>
  <applicationStatusCheck>%s</applicationStatusCheck>
</CreateApplicationStatusCheckResponse>`, ec2Xmlns(), generateUUID(), appStatusCheckXML(check))
}

func handleDescribeApplicationStatusChecks(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ApplicationStatusCheckId")
	var items strings.Builder
	for _, check := range ec2AppStatusChecks.List() {
		if len(ids) > 0 && !ec2StrInValues(check.ApplicationStatusCheckId, ids) {
			continue
		}
		fmt.Fprintf(&items, "<item>%s</item>", appStatusCheckXML(check))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeApplicationStatusChecksResponse %s>
  <requestId>%s</requestId>
  <applicationStatusCheckSet>%s</applicationStatusCheckSet>
</DescribeApplicationStatusChecksResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyApplicationStatusCheck(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ApplicationStatusCheckId")
	check, ok := ec2AppStatusChecks.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidParameterValue",
			fmt.Sprintf("The application status check ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("Protocol"); v != "" {
		check.Protocol = strings.ToUpper(v)
	}
	if v := r.FormValue("Port"); v != "" {
		check.Port = atoiDefault(v, check.Port)
	}
	if v := r.FormValue("Path"); v != "" {
		check.Path = v
	}
	if v := r.FormValue("Interval"); v != "" {
		check.Interval = atoiDefault(v, check.Interval)
	}
	if v := r.FormValue("Timeout"); v != "" {
		check.Timeout = atoiDefault(v, check.Timeout)
	}
	if v := r.FormValue("StatusCodeMatcher"); v != "" {
		check.StatusCodeMatcher = v
	}
	ec2AppStatusChecks.Put(id, check)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyApplicationStatusCheckResponse %s>
  <requestId>%s</requestId>
  <applicationStatusCheck>%s</applicationStatusCheck>
</ModifyApplicationStatusCheckResponse>`, ec2Xmlns(), generateUUID(), appStatusCheckXML(check))
}

func handleDeleteApplicationStatusCheck(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ApplicationStatusCheckId")
	if _, ok := ec2AppStatusChecks.Get(id); !ok {
		ec2ErrorXML(w, "InvalidParameterValue",
			fmt.Sprintf("The application status check ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// The check's associations go with it: they bind to a check that no
	// longer exists.
	for _, association := range ec2AppStatusAssociations.List() {
		if association.ApplicationStatusCheckId == id {
			ec2AppStatusAssociations.Delete(
				ec2AppStatusAssociationKey(association.ApplicationStatusCheckId, association.InstanceId))
		}
	}
	ec2AppStatusChecks.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteApplicationStatusCheckResponse %s>
  <requestId>%s</requestId>
  <return>true</return>
</DeleteApplicationStatusCheckResponse>`, ec2Xmlns(), generateUUID())
}

func handleAssociateApplicationStatusCheck(w http.ResponseWriter, r *http.Request) {
	checkID := r.FormValue("ApplicationStatusCheckId")
	if _, ok := ec2AppStatusChecks.Get(checkID); !ok {
		ec2ErrorXML(w, "InvalidParameterValue",
			fmt.Sprintf("The application status check ID '%s' does not exist", checkID), http.StatusBadRequest)
		return
	}
	instanceIDs := ec2ParamList(r, "InstanceId")
	if len(instanceIDs) == 0 {
		ec2ErrorXML(w, "MissingParameter", "The request must contain at least one InstanceId", http.StatusBadRequest)
		return
	}
	now := float64(time.Now().Unix())
	var successful, unsuccessful strings.Builder
	for _, instanceID := range instanceIDs {
		if _, ok := ec2Instances.Get(instanceID); !ok {
			fmt.Fprintf(&unsuccessful,
				"<item><applicationStatusCheckId>%s</applicationStatusCheckId><associationType>instance-id</associationType><associationValue>%s</associationValue><reason>The instance ID does not exist</reason></item>",
				checkID, instanceID)
			continue
		}
		ec2AppStatusAssociations.Put(ec2AppStatusAssociationKey(checkID, instanceID),
			EC2ApplicationStatusCheckAssociation{
				ApplicationStatusCheckId: checkID,
				InstanceId:               instanceID,
				AssociatedAt:             now,
			})
		fmt.Fprintf(&successful,
			"<item><applicationStatusCheckId>%s</applicationStatusCheckId><associationType>instance-id</associationType><associationValue>%s</associationValue></item>",
			checkID, instanceID)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateApplicationStatusCheckResponse %s>
  <requestId>%s</requestId>
  <successfulResultSet>%s</successfulResultSet>
  <unsuccessfulResultSet>%s</unsuccessfulResultSet>
</AssociateApplicationStatusCheckResponse>`, ec2Xmlns(), generateUUID(), successful.String(), unsuccessful.String())
}

func handleDisassociateApplicationStatusCheck(w http.ResponseWriter, r *http.Request) {
	checkID := r.FormValue("ApplicationStatusCheckId")
	instanceIDs := ec2ParamList(r, "InstanceId")
	if checkID == "" || len(instanceIDs) == 0 {
		ec2ErrorXML(w, "MissingParameter",
			"The request must contain ApplicationStatusCheckId and at least one InstanceId", http.StatusBadRequest)
		return
	}
	var successful, unsuccessful strings.Builder
	for _, instanceID := range instanceIDs {
		key := ec2AppStatusAssociationKey(checkID, instanceID)
		if _, ok := ec2AppStatusAssociations.Get(key); !ok {
			fmt.Fprintf(&unsuccessful,
				"<item><applicationStatusCheckId>%s</applicationStatusCheckId><associationType>instance-id</associationType><associationValue>%s</associationValue><reason>The association does not exist</reason></item>",
				checkID, instanceID)
			continue
		}
		ec2AppStatusAssociations.Delete(key)
		fmt.Fprintf(&successful,
			"<item><applicationStatusCheckId>%s</applicationStatusCheckId><associationType>instance-id</associationType><associationValue>%s</associationValue></item>",
			checkID, instanceID)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisassociateApplicationStatusCheckResponse %s>
  <requestId>%s</requestId>
  <successfulResultSet>%s</successfulResultSet>
  <unsuccessfulResultSet>%s</unsuccessfulResultSet>
</DisassociateApplicationStatusCheckResponse>`, ec2Xmlns(), generateUUID(), successful.String(), unsuccessful.String())
}

func handleDescribeApplicationStatusCheckAssociations(w http.ResponseWriter, r *http.Request) {
	checkIDs := ec2ParamList(r, "ApplicationStatusCheckId")
	var items strings.Builder
	for _, association := range ec2AppStatusAssociations.List() {
		if len(checkIDs) > 0 && !ec2StrInValues(association.ApplicationStatusCheckId, checkIDs) {
			continue
		}
		fmt.Fprintf(&items,
			"<item><applicationStatusCheckId>%s</applicationStatusCheckId><associationType>instance-id</associationType><value>%s</value></item>",
			association.ApplicationStatusCheckId, association.InstanceId)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeApplicationStatusCheckAssociationsResponse %s>
  <requestId>%s</requestId>
  <associationSet>%s</associationSet>
</DescribeApplicationStatusCheckAssociationsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

// handleDescribeApplicationStatus reports each associated instance's measured
// status, in the exact response shape the SDK deserialises: instanceSet →
// applicationStatus → detailSet. The verdict is real: a stopped instance's
// checks fail because there is nothing to probe, a suppressed instance
// reports `suppressed` at the instance level, and a running instance is
// probed over the check's own protocol, port and path against its address — a
// listener that answers passes, and nothing listening fails, whichever host
// this runs on.
func handleDescribeApplicationStatus(w http.ResponseWriter, r *http.Request) {
	instanceIDs := ec2ParamList(r, "InstanceId")
	byInstance := map[string][]EC2ApplicationStatusCheckAssociation{}
	for _, association := range ec2AppStatusAssociations.List() {
		if len(instanceIDs) > 0 && !ec2StrInValues(association.InstanceId, instanceIDs) {
			continue
		}
		byInstance[association.InstanceId] = append(byInstance[association.InstanceId], association)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	var instances strings.Builder
	for instanceID, associations := range byInstance {
		suppressed := false
		anyFailed := false
		var details strings.Builder
		for _, association := range associations {
			check, ok := ec2AppStatusChecks.Get(association.ApplicationStatusCheckId)
			if !ok {
				continue
			}
			if association.Suppressed {
				suppressed = true
			}
			checkStatus := "passed"
			if !ec2ProbeApplicationCheck(r.Context(), association, check) {
				checkStatus = "failed"
				anyFailed = true
			}
			fmt.Fprintf(&details,
				"<item><applicationStatusCheckId>%s</applicationStatusCheckId><aggregation>included</aggregation><status>%s</status><statusTimeStamp>%s</statusTimeStamp></item>",
				check.ApplicationStatusCheckId, checkStatus, now)
		}
		instanceStatus := "ok"
		switch {
		case suppressed:
			instanceStatus = "suppressed"
		case anyFailed:
			instanceStatus = "impaired"
		}
		fmt.Fprintf(&instances,
			"<item><instanceId>%s</instanceId><applicationStatus><status>%s</status><statusTimeStamp>%s</statusTimeStamp><detailSet>%s</detailSet></applicationStatus></item>",
			instanceID, instanceStatus, now, details.String())
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeApplicationStatusResponse %s>
  <requestId>%s</requestId>
  <applicationStatusesResponseType><instanceSet>%s</instanceSet></applicationStatusesResponseType>
</DescribeApplicationStatusResponse>`, ec2Xmlns(), generateUUID(), instances.String())
}

// ec2ProbeApplicationCheck performs the check against the instance and
// reports whether it passed.
func ec2ProbeApplicationCheck(ctx context.Context, association EC2ApplicationStatusCheckAssociation, check EC2ApplicationStatusCheck) bool {
	instance, ok := ec2Instances.Get(association.InstanceId)
	if !ok || instance.State != "running" || instance.PrivateIpAddress == "" {
		return false
	}
	timeout := time.Duration(check.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	target := net.JoinHostPort(instance.PrivateIpAddress, fmt.Sprintf("%d", check.Port))
	client := &http.Client{Timeout: timeout}
	response, err := client.Get(strings.ToLower(check.Protocol) + "://" + target + check.Path)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	if check.StatusCodeMatcher != "" {
		return healthCheckCodeMatchesMatcher(check.StatusCodeMatcher, response.StatusCode)
	}
	return response.StatusCode < 400
}

func ec2SetAppStatusSuppression(w http.ResponseWriter, r *http.Request, action string, suppressed bool) {
	instanceIDs := ec2ParamList(r, "InstanceId")
	if len(instanceIDs) == 0 {
		ec2ErrorXML(w, "MissingParameter", "The request must contain at least one InstanceId", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	var successful, unsuccessful strings.Builder
	for _, instanceID := range instanceIDs {
		changed := false
		for _, association := range ec2AppStatusAssociations.List() {
			if association.InstanceId != instanceID {
				continue
			}
			association.Suppressed = suppressed
			ec2AppStatusAssociations.Put(
				ec2AppStatusAssociationKey(association.ApplicationStatusCheckId, association.InstanceId), association)
			changed = true
		}
		if !changed {
			fmt.Fprintf(&unsuccessful,
				"<item><instanceId>%s</instanceId><reason>The instance has no application status check association</reason></item>",
				instanceID)
			continue
		}
		if suppressed {
			fmt.Fprintf(&successful, "<item><instanceId>%s</instanceId><suppressAt>%s</suppressAt></item>", instanceID, now)
		} else {
			fmt.Fprintf(&successful, "<item><instanceId>%s</instanceId><resumeAt>%s</resumeAt></item>", instanceID, now)
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s>
  <requestId>%s</requestId>
  <successfulResultSet>%s</successfulResultSet>
  <unsuccessfulResultSet>%s</unsuccessfulResultSet>
</%sResponse>`, action, ec2Xmlns(), generateUUID(), successful.String(), unsuccessful.String(), action)
}

func handleEnableApplicationStatusCheckSuppression(w http.ResponseWriter, r *http.Request) {
	ec2SetAppStatusSuppression(w, r, "EnableApplicationStatusCheckSuppression", true)
}

func handleDisableApplicationStatusCheckSuppression(w http.ResponseWriter, r *http.Request) {
	ec2SetAppStatusSuppression(w, r, "DisableApplicationStatusCheckSuppression", false)
}
