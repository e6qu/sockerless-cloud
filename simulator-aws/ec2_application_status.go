package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon EC2 application status checks: operator-defined health checks the
// platform runs against instances, and the per-instance application status
// they produce.
//
// The check and its associations are control-plane state, stored and returned
// as the API defines them. The status is measured on the check's own
// schedule: a checker probes each associated instance over the check's
// protocol, port and path every Interval once the instance's initialization
// grace period has passed, and moves the check to passed or failed after
// SuccessThreshold or FailureThreshold consecutive results. DescribeApplicationStatus
// reports what the latest checks recorded; a suppressed instance reports its
// suppression, not a verdict.

type EC2ApplicationStatusCheck struct {
	ApplicationStatusCheckId  string   `json:"applicationStatusCheckId"`
	Aggregation               string   `json:"aggregation,omitempty"`
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
	srv.StartBackground("EC2 application status checker", func(ctx context.Context) {
		lbplane.SweepEvery(ctx, ec2AppStatusSweep, ec2CheckApplicationStatus)
	})
}

// ec2AppStatusSweep is how often the checker looks for checks that have come
// due; each check runs on its own Interval.
const ec2AppStatusSweep = time.Second

var ec2AppStatusTracker = lbplane.NewHealthTracker[string]()

func appStatusCheckXML(check EC2ApplicationStatusCheck) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<applicationStatusCheckId>%s</applicationStatusCheckId>", check.ApplicationStatusCheckId)
	if check.Aggregation != "" {
		fmt.Fprintf(&b, "<aggregation>%s</aggregation>", check.Aggregation)
	}
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
	if check.FailureThreshold > 0 {
		fmt.Fprintf(&b, "<failureThreshold>%d</failureThreshold>", check.FailureThreshold)
	}
	if check.SuccessThreshold > 0 {
		fmt.Fprintf(&b, "<successThreshold>%d</successThreshold>", check.SuccessThreshold)
	}
	if check.StatusCodeMatcher != "" {
		fmt.Fprintf(&b, "<statusCodeMatcher>%s</statusCodeMatcher>", xmlEscape(check.StatusCodeMatcher))
	}
	if check.InitializationGracePeriod > 0 {
		fmt.Fprintf(&b, "<initializationGracePeriodSeconds>%d</initializationGracePeriodSeconds>", check.InitializationGracePeriod)
	}
	b.WriteString(writeTagSetXML(check.Tags))
	return b.String()
}

// ec2ValidateAppStatusCheck applies the constraints the Amazon EC2 model
// documents for a check's schedule and thresholds.
func ec2ValidateAppStatusCheck(check EC2ApplicationStatusCheck) string {
	switch {
	case check.Aggregation != "included" && check.Aggregation != "excluded":
		return fmt.Sprintf("Aggregation must be included or excluded; %q is not a value AggregationStatusEnum admits", check.Aggregation)
	case check.Interval != 60:
		return fmt.Sprintf("Interval %d is not valid; the valid value is 60", check.Interval)
	case check.Timeout < 1 || check.Timeout > 30 || check.Timeout >= check.Interval:
		return fmt.Sprintf("Timeout %d is not valid; it must be 1 to 30 and less than Interval", check.Timeout)
	case check.FailureThreshold < 1:
		return "FailureThreshold must be greater than 0"
	case check.SuccessThreshold < 1:
		return "SuccessThreshold must be greater than 0"
	case check.InitializationGracePeriod < 0 || check.InitializationGracePeriod > 600:
		return fmt.Sprintf("InitializationGracePeriodSeconds %d is not valid; valid values are 1 to 600", check.InitializationGracePeriod)
	}
	if _, err := lbplane.ParseStatusMatcher(check.StatusCodeMatcher); err != nil || len(check.StatusCodeMatcher) > 64 {
		return fmt.Sprintf("StatusCodeMatcher %q is not a comma-separated list of status codes and ranges of at most 64 characters", check.StatusCodeMatcher)
	}
	return ""
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
		Aggregation:               r.FormValue("Aggregation"),
		Protocol:                  protocol,
		Port:                      port,
		Path:                      r.FormValue("Path"),
		Interval:                  atoiDefault(r.FormValue("Interval"), 60),
		Timeout:                   atoiDefault(r.FormValue("Timeout"), 5),
		FailureThreshold:          atoiDefault(r.FormValue("FailureThreshold"), 3),
		SuccessThreshold:          atoiDefault(r.FormValue("SuccessThreshold"), 2),
		StatusCodeMatcher:         r.FormValue("StatusCodeMatcher"),
		InitializationGracePeriod: atoiDefault(r.FormValue("InitializationGracePeriodSeconds"), 0),
		Tags:                      parseTags(r),
	}
	if check.Aggregation == "" {
		check.Aggregation = "included"
	}
	if check.StatusCodeMatcher == "" {
		check.StatusCodeMatcher = "200"
	}
	if problem := ec2ValidateAppStatusCheck(check); problem != "" {
		ec2ErrorXML(w, "InvalidParameterValue", problem, http.StatusBadRequest)
		return
	}
	ec2AppStatusChecks.Put(check.ApplicationStatusCheckId, check)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateApplicationStatusCheckResponse %s>
  <requestId>%s</requestId>
  <applicationStatusCheck>%s</applicationStatusCheck>
</CreateApplicationStatusCheckResponse>`, ec2Xmlns(), sim.NewUUID(), appStatusCheckXML(check))
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
</DescribeApplicationStatusChecksResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
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
		protocol := strings.ToLower(v)
		if protocol != "http" && protocol != "https" {
			ec2ErrorXML(w, "InvalidParameterValue",
				fmt.Sprintf("Protocol must be http or https; %q is not a value NetworkProtocolEnum admits", protocol),
				http.StatusBadRequest)
			return
		}
		check.Protocol = protocol
	}
	if v := r.FormValue("Aggregation"); v != "" {
		check.Aggregation = v
	} else if check.Aggregation == "" {
		check.Aggregation = "included"
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
	if v := r.FormValue("FailureThreshold"); v != "" {
		check.FailureThreshold = atoiDefault(v, check.FailureThreshold)
	}
	if v := r.FormValue("SuccessThreshold"); v != "" {
		check.SuccessThreshold = atoiDefault(v, check.SuccessThreshold)
	}
	if v := r.FormValue("InitializationGracePeriodSeconds"); v != "" {
		check.InitializationGracePeriod = atoiDefault(v, check.InitializationGracePeriod)
	}
	if v := r.FormValue("StatusCodeMatcher"); v != "" {
		check.StatusCodeMatcher = v
	}
	if problem := ec2ValidateAppStatusCheck(check); problem != "" {
		ec2ErrorXML(w, "InvalidParameterValue", problem, http.StatusBadRequest)
		return
	}
	ec2AppStatusChecks.Put(id, check)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyApplicationStatusCheckResponse %s>
  <requestId>%s</requestId>
  <applicationStatusCheck>%s</applicationStatusCheck>
</ModifyApplicationStatusCheckResponse>`, ec2Xmlns(), sim.NewUUID(), appStatusCheckXML(check))
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
</DeleteApplicationStatusCheckResponse>`, ec2Xmlns(), sim.NewUUID())
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
</AssociateApplicationStatusCheckResponse>`, ec2Xmlns(), sim.NewUUID(), successful.String(), unsuccessful.String())
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
</DisassociateApplicationStatusCheckResponse>`, ec2Xmlns(), sim.NewUUID(), successful.String(), unsuccessful.String())
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
</DescribeApplicationStatusCheckAssociationsResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}

// ec2CheckApplicationStatus is one sweep of the checker: every association
// past its instance's initialization grace period whose check has come due at
// now is probed, and the result folded into the association's record.
func ec2CheckApplicationStatus(ctx context.Context, now time.Time) {
	var targets []lbplane.HealthTarget[string]
	for _, association := range ec2AppStatusAssociations.List() {
		check, ok := ec2AppStatusChecks.Get(association.ApplicationStatusCheckId)
		if !ok || ec2AppStatusInGracePeriod(association.InstanceId, check, now) {
			continue
		}
		instanceID := association.InstanceId
		targets = append(targets, lbplane.HealthTarget[string]{
			Key: ec2AppStatusAssociationKey(association.ApplicationStatusCheckId, association.InstanceId),
			Policy: lbplane.HealthPolicy{
				Interval:                time.Duration(check.Interval) * time.Second,
				InitialHealthyThreshold: check.SuccessThreshold,
				HealthyThreshold:        check.SuccessThreshold,
				UnhealthyThreshold:      check.FailureThreshold,
			},
			Probe: func(ctx context.Context) (int, error) {
				return ec2ProbeApplicationCheck(ctx, instanceID, check)
			},
		})
	}
	ec2AppStatusTracker.Sweep(ctx, now, targets)
}

// ec2AppStatusInGracePeriod reports whether the check still waits out its
// InitializationGracePeriodSeconds after the instance launched.
func ec2AppStatusInGracePeriod(instanceID string, check EC2ApplicationStatusCheck, now time.Time) bool {
	if check.InitializationGracePeriod <= 0 {
		return false
	}
	instance, ok := ec2Instances.Get(instanceID)
	if !ok {
		return false
	}
	launched, err := time.Parse(time.RFC3339, instance.LaunchTime)
	if err != nil {
		return false
	}
	return now.Before(launched.Add(time.Duration(check.InitializationGracePeriod) * time.Second))
}

// ec2AppStatusReasonXML renders the reason the latest check recorded, in the
// codes the ApplicationStatusReason shape documents.
func ec2AppStatusReasonXML(health lbplane.Health, protocol string) string {
	var code string
	status := health.LastStatus
	var mismatch *lbplane.StatusMismatchError
	var netError net.Error
	var opError *net.OpError
	switch err := health.LastErr; {
	case err == nil:
		code = "ResponseCodeMatched"
	case errors.As(err, &mismatch):
		code, status = "ResponseCodeMismatch", mismatch.StatusCode
	case errors.Is(err, syscall.ECONNREFUSED):
		code = "ConnectionRefused"
	case errors.Is(err, syscall.ECONNRESET):
		code = "ConnectionReset"
	case errors.As(err, &opError) && opError.Op == "dial" && opError.Timeout():
		code = "ConnectionTimeout"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netError) && netError.Timeout()):
		code = "ResponseTimeout"
	default:
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<reason><code>%s</code>", code)
	if status > 0 {
		fmt.Fprintf(&b, "<statusCode>%d</statusCode>", status)
	}
	fmt.Fprintf(&b, "<protocol>%s</protocol></reason>", strings.ToUpper(protocol))
	return b.String()
}

// handleDescribeApplicationStatus reports what the checker last recorded for
// each associated instance, in the response shape the SDK deserialises:
// instanceSet → applicationStatus → detailSet. A check the checker has not yet
// run, or that has not reached a threshold, is initializing.
func handleDescribeApplicationStatus(w http.ResponseWriter, r *http.Request) {
	instanceIDs := ec2ParamList(r, "InstanceId")
	byInstance := map[string][]EC2ApplicationStatusCheckAssociation{}
	for _, association := range ec2AppStatusAssociations.List() {
		if len(instanceIDs) > 0 && !ec2StrInValues(association.InstanceId, instanceIDs) {
			continue
		}
		byInstance[association.InstanceId] = append(byInstance[association.InstanceId], association)
	}
	const layout = "2006-01-02T15:04:05.000Z"
	now := time.Now().UTC()
	var instances strings.Builder
	for instanceID, associations := range byInstance {
		suppressed, included, impaired, initializing := false, false, false, false
		latest := time.Time{}
		var details strings.Builder
		for _, association := range associations {
			check, ok := ec2AppStatusChecks.Get(association.ApplicationStatusCheckId)
			if !ok {
				continue
			}
			suppressed = suppressed || association.Suppressed
			health, recorded := ec2AppStatusTracker.Health(ec2AppStatusAssociationKey(check.ApplicationStatusCheckId, instanceID))
			checkStatus := "initializing"
			switch {
			case recorded && health.State == lbplane.HealthHealthy:
				checkStatus = "passed"
			case recorded && health.State == lbplane.HealthUnhealthy:
				checkStatus = "failed"
			}
			stamp := time.Unix(int64(association.AssociatedAt), 0).UTC()
			reason := ""
			if recorded && health.Checks > 0 {
				stamp = health.LastChecked.UTC()
				reason = ec2AppStatusReasonXML(health, check.Protocol)
			}
			if stamp.After(latest) {
				latest = stamp
			}
			aggregation := check.Aggregation
			if aggregation == "" {
				aggregation = "included"
			}
			if aggregation == "included" {
				included = true
				impaired = impaired || checkStatus == "failed"
				initializing = initializing || checkStatus == "initializing"
			}
			fmt.Fprintf(&details,
				"<item><applicationStatusCheckId>%s</applicationStatusCheckId><aggregation>%s</aggregation><status>%s</status><statusTimeStamp>%s</statusTimeStamp>%s</item>",
				check.ApplicationStatusCheckId, aggregation, checkStatus, stamp.Format(layout), reason)
		}
		instanceStatus := "ok"
		switch {
		case suppressed:
			instanceStatus = "suppressed"
		case !included:
			instanceStatus = "not-applicable"
		case impaired:
			instanceStatus = "impaired"
		case initializing:
			instanceStatus = "initializing"
		}
		if latest.IsZero() {
			latest = now
		}
		fmt.Fprintf(&instances,
			"<item><instanceId>%s</instanceId><applicationStatus><status>%s</status><statusTimeStamp>%s</statusTimeStamp><detailSet>%s</detailSet></applicationStatus></item>",
			instanceID, instanceStatus, latest.Format(layout), details.String())
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeApplicationStatusResponse %s>
  <requestId>%s</requestId>
  <applicationStatusesResponseType><instanceSet>%s</instanceSet></applicationStatusesResponseType>
</DescribeApplicationStatusResponse>`, ec2Xmlns(), sim.NewUUID(), instances.String())
}

// ec2ProbeApplicationCheck performs the check against the instance and
// returns the HTTP status it read. An instance that is not running has no
// address to probe.
func ec2ProbeApplicationCheck(ctx context.Context, instanceID string, check EC2ApplicationStatusCheck) (int, error) {
	instance, ok := ec2Instances.Get(instanceID)
	if !ok || instance.State != "running" || instance.PrivateIpAddress == "" {
		return 0, fmt.Errorf("instance %s is not running", instanceID)
	}
	match, err := lbplane.ParseStatusMatcher(check.StatusCodeMatcher)
	if err != nil {
		return 0, err
	}
	return lbplane.ProbeHTTP(ctx, lbplane.HTTPProbe{
		Scheme:            strings.ToLower(check.Protocol),
		VerifyCertificate: true,
		Address:           net.JoinHostPort(instance.PrivateIpAddress, strconv.Itoa(check.Port)),
		Path:              check.Path,
		Timeout:           time.Duration(check.Timeout) * time.Second,
		Match:             match,
	})
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
</%sResponse>`, action, ec2Xmlns(), sim.NewUUID(), successful.String(), unsuccessful.String(), action)
}

func handleEnableApplicationStatusCheckSuppression(w http.ResponseWriter, r *http.Request) {
	ec2SetAppStatusSuppression(w, r, "EnableApplicationStatusCheckSuppression", true)
}

func handleDisableApplicationStatusCheckSuppression(w http.ResponseWriter, r *http.Request) {
	ec2SetAppStatusSuppression(w, r, "DisableApplicationStatusCheckSuppression", false)
}
