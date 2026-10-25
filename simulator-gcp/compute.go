package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/netrules"
)

// gcpInt64 round-trips int64 quoted-as-string (real GCP discovery
// shape) AND unquoted numbers (terraform-provider shape). Always emits
// quoted-string on output.
type gcpInt64 string

func (g gcpInt64) MarshalJSON() ([]byte, error) {
	if g == "" {
		return []byte(`""`), nil
	}
	return json.Marshal(string(g))
}

func (g *gcpInt64) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*g = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*g = gcpInt64(s)
		return nil
	}
	*g = gcpInt64(string(b))
	return nil
}

func computeNumericID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%d", binary.BigEndian.Uint64(b)>>1)
}

var gcpAutoModeSubnetCIDRs = map[string]string{
	"africa-south1":           "10.218.0.0/20",
	"asia-east1":              "10.140.0.0/20",
	"asia-east2":              "10.170.0.0/20",
	"asia-northeast1":         "10.146.0.0/20",
	"asia-northeast2":         "10.174.0.0/20",
	"asia-northeast3":         "10.178.0.0/20",
	"asia-south1":             "10.160.0.0/20",
	"asia-south2":             "10.190.0.0/20",
	"asia-southeast1":         "10.148.0.0/20",
	"asia-southeast2":         "10.184.0.0/20",
	"asia-southeast3":         "10.232.0.0/20",
	"australia-southeast1":    "10.152.0.0/20",
	"australia-southeast2":    "10.192.0.0/20",
	"europe-central2":         "10.186.0.0/20",
	"europe-north1":           "10.166.0.0/20",
	"europe-north2":           "10.226.0.0/20",
	"europe-west1":            "10.132.0.0/20",
	"europe-west2":            "10.154.0.0/20",
	"europe-west3":            "10.156.0.0/20",
	"europe-west4":            "10.164.0.0/20",
	"europe-west6":            "10.172.0.0/20",
	"europe-west8":            "10.198.0.0/20",
	"europe-west9":            "10.200.0.0/20",
	"europe-west10":           "10.214.0.0/20",
	"europe-west12":           "10.210.0.0/20",
	"europe-southwest1":       "10.204.0.0/20",
	"me-central1":             "10.212.0.0/20",
	"me-central2":             "10.216.0.0/20",
	"me-west1":                "10.208.0.0/20",
	"northamerica-northeast1": "10.162.0.0/20",
	"northamerica-northeast2": "10.188.0.0/20",
	"northamerica-south1":     "10.224.0.0/20",
	"southamerica-east1":      "10.158.0.0/20",
	"southamerica-west1":      "10.194.0.0/20",
	"us-central1":             "10.128.0.0/20",
	"us-east1":                "10.142.0.0/20",
	"us-east4":                "10.150.0.0/20",
	"us-east5":                "10.202.0.0/20",
	"us-south1":               "10.206.0.0/20",
	"us-west1":                "10.138.0.0/20",
	"us-west2":                "10.168.0.0/20",
	"us-west3":                "10.180.0.0/20",
	"us-west4":                "10.182.0.0/20",
}

// ComputeOperationRecord is one operation the sim has handed out. It carries
// the operation's whole observable state, so a GET or a wait reports what the
// operation is actually doing rather than a fabricated verdict, and so an
// operation issued before a restart still resolves.
//
// The members are the `compute#operation` resource's own, from the Compute
// Engine v1 Discovery document: status is one of `PENDING`, `RUNNING` or
// `DONE`; progress "ranges from 0 to 100"; endTime is "the time that this
// operation was completed"; and error, httpErrorStatusCode and
// httpErrorMessage are populated only "if errors are generated during
// processing of the operation".
type ComputeOperationRecord struct {
	Name          string `json:"name"`
	ID            string `json:"id,omitempty"`
	Project       string `json:"project,omitempty"`
	Scope         string `json:"scope,omitempty"` // "global", "zones/{zone}" or "regions/{region}"
	TargetLink    string `json:"targetLink,omitempty"`
	TargetID      string `json:"targetId,omitempty"`
	OperationType string `json:"operationType,omitempty"`
	Status        string `json:"status,omitempty"`
	Progress      int    `json:"progress"`
	InsertTime    string `json:"insertTime,omitempty"`
	StartTime     string `json:"startTime,omitempty"`
	EndTime       string `json:"endTime,omitempty"`

	ErrorCode           string `json:"errorCode,omitempty"`
	ErrorMessage        string `json:"errorMessage,omitempty"`
	HTTPErrorStatusCode int    `json:"httpErrorStatusCode,omitempty"`
	HTTPErrorMessage    string `json:"httpErrorMessage,omitempty"`
}

// computeOpRegistry records every operation the sim hands out, in the same
// store family as the other services' LROs, so polling an operation name
// issued before a restart still resolves. registerCompute wires it.
var computeOpRegistry sim.Store[ComputeOperationRecord]

func recordComputeOp(rec ComputeOperationRecord) {
	computeOpRegistry.Put(rec.Name, rec)
}

// computeOpFinish moves a running operation to DONE. A nil err completes it
// successfully; a non-nil one populates the error members Compute Engine
// reports for an operation that failed while it was being processed.
//
// `INTERNAL_ERROR` is the error type identifier Compute Engine reports when
// the work behind an operation does not complete. Google's Compute Engine
// known-issues page describes the shape exactly — an operation that "gets
// stuck in a RUNNING state at 0% progress and eventually fails with an
// INTERNAL_ERROR" — which is also the state an operation starts in here. The
// accompanying HTTP status is the one the simulator answers when it cannot
// bring a machine up.
func computeOpFinish(name string, err error) {
	defer gcpOperationDone.Announce(name)
	computeOpRegistry.Update(name, func(rec *ComputeOperationRecord) {
		rec.Status = "DONE"
		rec.Progress = 100
		rec.EndTime = time.Now().UTC().Format(time.RFC3339)
		if err == nil {
			return
		}
		rec.ErrorCode = "INTERNAL_ERROR"
		rec.ErrorMessage = err.Error()
		rec.HTTPErrorStatusCode = http.StatusServiceUnavailable
		rec.HTTPErrorMessage = "SERVICE UNAVAILABLE"
	})
}

// computeOpJSON renders a recorded operation as the `compute#operation`
// resource, which is the identical shape whether it is returned by the method
// that started the operation or by a later poll of it.
func computeOpJSON(rec ComputeOperationRecord) map[string]any {
	path := fmt.Sprintf("projects/%s/%s/operations/%s", rec.Project, rec.Scope, rec.Name)
	op := map[string]any{
		"kind":       "compute#operation",
		"id":         rec.ID,
		"name":       rec.Name,
		"status":     rec.Status,
		"selfLink":   "https://www.googleapis.com/compute/v1/" + path,
		"progress":   rec.Progress,
		"insertTime": rec.InsertTime,
		"startTime":  rec.StartTime,
	}
	if rec.OperationType != "" {
		op["operationType"] = rec.OperationType
	}
	if rec.TargetLink != "" {
		op["targetLink"] = rec.TargetLink
	}
	if rec.TargetID != "" {
		op["targetId"] = rec.TargetID
	}
	if rec.EndTime != "" {
		op["endTime"] = rec.EndTime
	}
	if region, ok := strings.CutPrefix(rec.Scope, "regions/"); ok {
		op["region"] = fmt.Sprintf("https://www.googleapis.com/compute/v1/projects/%s/regions/%s", rec.Project, region)
	}
	if zone, ok := strings.CutPrefix(rec.Scope, "zones/"); ok {
		op["zone"] = fmt.Sprintf("https://www.googleapis.com/compute/v1/projects/%s/zones/%s", rec.Project, zone)
	}
	if rec.ErrorCode != "" {
		op["error"] = map[string]any{
			"errors": []map[string]any{{"code": rec.ErrorCode, "message": rec.ErrorMessage}},
		}
		op["httpErrorStatusCode"] = rec.HTTPErrorStatusCode
		op["httpErrorMessage"] = rec.HTTPErrorMessage
	}
	return op
}

// computeWriteOperation answers an operations GET from the record. An
// operation name the sim never handed out is not found, rather than answered
// with a fabricated verdict.
func computeWriteOperation(w http.ResponseWriter, name string) {
	rec, ok := computeOpRegistry.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "notFound", "operation %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, computeOpJSON(rec))
}

// computeOperationWaitBudget is how long a `wait` call blocks before returning
// the operation's current state. Compute Engine documents that
// zoneOperations.wait "waits for the specified Operation resource to return as
// DONE or for the request to approach the 2 minute deadline, and retrieves the
// specified Operation resource", and that it "waits for no more than the 2
// minutes and then returns the current state of the operation, which might be
// DONE or still in progress".
const computeOperationWaitBudget = 2 * time.Minute

// computeWaitOperation implements that contract: it blocks until the operation
// reaches DONE, the budget runs out, or the client goes away, then answers with
// whatever state the operation is in.
func computeWaitOperation(w http.ResponseWriter, r *http.Request, name string) {
	sim.DeclareWait(r.Context(), computeOperationWaitBudget)
	// Subscribe before reading, so a finish between the read and the wait still
	// wakes it.
	done := gcpOperationDone.Changed(name)
	rec, ok := computeOpRegistry.Get(name)
	if !ok {
		gcpOperationDone.Announce(name)
		GCPErrorf(w, http.StatusNotFound, "notFound", "operation %q not found", name)
		return
	}
	if rec.Status == "DONE" {
		gcpOperationDone.Announce(name)
	} else {
		budget := time.NewTimer(computeOperationWaitBudget)
		defer budget.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-done:
		case <-budget.C:
		}
		if rec, ok = computeOpRegistry.Get(name); !ok {
			GCPErrorf(w, http.StatusNotFound, "notFound", "operation %q not found", name)
			return
		}
	}
	sim.WriteJSON(w, http.StatusOK, computeOpJSON(rec))
}

func newComputeOp(project, scope string, targetLink string) map[string]any {
	return newComputeOpWithType(project, scope, targetLink, "operation")
}

// newComputeOpWithType records and renders an operation whose work the request
// that produced it already completed, so it is DONE the moment it is handed
// out — which is what Compute Engine returns for the resource writes that do
// not have to bring anything up.
func newComputeOpWithType(project, scope string, targetLink string, operationType string) map[string]any {
	rec := newComputeOpRecord(project, scope, targetLink, operationType)
	now := time.Now().UTC().Format(time.RFC3339)
	rec.Status = "DONE"
	rec.Progress = 100
	rec.EndTime = now
	recordComputeOp(rec)
	return computeOpJSON(rec)
}

// newComputeOpRecord mints an operation record in the state Compute Engine
// starts one in: RUNNING, with no end time and no progress yet. A caller that
// completes the work inside the request marks it DONE before handing it out;
// one that continues behind the response leaves it running and calls
// computeOpFinish when the work settles.
func newComputeOpRecord(project, scope, targetLink, operationType string) ComputeOperationRecord {
	now := time.Now().UTC().Format(time.RFC3339)
	return ComputeOperationRecord{
		Name:          "operation-" + sim.NewUUID()[:8],
		ID:            computeNumericID(),
		Project:       project,
		Scope:         scope,
		TargetLink:    targetLink,
		TargetID:      computeNumericID(),
		OperationType: operationType,
		Status:        "RUNNING",
		Progress:      0,
		InsertTime:    now,
		StartTime:     now,
	}
}

// computeConflict writes the 409 ALREADY_EXISTS response real GCP returns
// when an insert names a resource that already exists. exists reports
// whether the store already holds the key; when true the handler must
// return immediately.
func computeConflict(w http.ResponseWriter, exists bool, resource, name string) bool {
	if exists {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "The resource '%s' named '%s' already exists", resource, name)
		return true
	}
	return false
}

// computeResourceNamePattern is the RFC 1035 name the Discovery document
// declares for a Compute Engine resource's name.
const computeResourceNamePattern = `(?:[a-z](?:[-a-z0-9]{0,61}[a-z0-9])?)`

var computeResourceNameRE = regexp.MustCompile(`^` + computeResourceNamePattern + `$`)

// computeRefuseResourceName answers an insert whose resource name is missing
// or breaks the declared pattern, and reports whether it did.
func computeRefuseResourceName(w http.ResponseWriter, name string) bool {
	switch {
	case name == "":
		computeFieldError(w, "required", "Required field 'resource.name' not specified")
	case !computeResourceNameRE.MatchString(name):
		computeFieldError(w, "invalid", fmt.Sprintf(
			"Invalid value for field 'resource.name': '%s'. Must be a match of regex '%s'", name, computeResourceNamePattern))
	default:
		return false
	}
	return true
}

// computeUnpatternedNameKinds are the insertable resources whose name the
// Discovery document gives no pattern.
var computeUnpatternedNameKinds = map[string]bool{
	"compute#nodeTemplate":         true,
	"compute#nodeGroup":            true,
	"compute#networkEndpointGroup": true,
	"compute#firewallPolicy":       true,
}

// computeRefuseNameOf is computeRefuseResourceName for a resource of the given
// kind, which judges the name's pattern only where the document declares one.
func computeRefuseNameOf(w http.ResponseWriter, kind, name string) bool {
	if computeUnpatternedNameKinds[kind] {
		if name == "" {
			computeFieldError(w, "required", "Required field 'resource.name' not specified")
			return true
		}
		return false
	}
	return computeRefuseResourceName(w, name)
}

// computeWriteNormalizeRefusal answers an instance the insert path could not
// fill: a field Compute Engine refuses carries its reason, anything else is
// an invalid argument.
func computeWriteNormalizeRefusal(w http.ResponseWriter, err error) {
	var field computeFieldRefusal
	if errors.As(err, &field) {
		computeFieldError(w, field.reason, field.message)
		return
	}
	GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
}

// computeFieldError writes the 400 Compute Engine answers a request field it
// refuses with; clients branch on errors[].reason.
func computeFieldError(w http.ResponseWriter, reason, message string) {
	sim.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{
			"code":    http.StatusBadRequest,
			"message": message,
			"status":  "INVALID_ARGUMENT",
			"errors": []map[string]string{{
				"message": message,
				"domain":  "global",
				"reason":  reason,
			}},
		},
	})
}

// computeNotFound writes the 404 NOT_FOUND response real GCP returns when a
// delete targets a resource that is absent. deleted is the bool the store's
// Delete reported; when false the handler must return immediately.
func computeNotFound(w http.ResponseWriter, deleted bool, resource, name string) bool {
	if !deleted {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The resource '%s' named '%s' was not found", resource, name)
		return true
	}
	return false
}

// ComputeFirewall mirrors `compute#firewall`. Field set covers what
// terraform-provider-google's `google_compute_firewall` and the Go
// SDK's `compute.NewFirewallsRESTClient` round-trip; runner setup
// flows that grant ingress to the build host hit Create/Get/Delete.
type ComputeFirewall struct {
	Kind                  string                  `json:"kind,omitempty"`
	Id                    string                  `json:"id,omitempty"`
	Name                  string                  `json:"name"`
	SelfLink              string                  `json:"selfLink,omitempty"`
	CreationTimestamp     string                  `json:"creationTimestamp,omitempty"`
	Description           string                  `json:"description,omitempty"`
	Network               string                  `json:"network,omitempty"`
	Direction             string                  `json:"direction,omitempty"` // INGRESS / EGRESS
	Priority              int32                   `json:"priority,omitempty"`
	Disabled              bool                    `json:"disabled,omitempty"`
	SourceRanges          []string                `json:"sourceRanges,omitempty"`
	DestinationRanges     []string                `json:"destinationRanges,omitempty"`
	SourceTags            []string                `json:"sourceTags,omitempty"`
	TargetTags            []string                `json:"targetTags,omitempty"`
	SourceServiceAccounts []string                `json:"sourceServiceAccounts,omitempty"`
	TargetServiceAccounts []string                `json:"targetServiceAccounts,omitempty"`
	Allowed               []ComputeFirewallAction `json:"allowed,omitempty"`
	Denied                []ComputeFirewallAction `json:"denied,omitempty"`
	LogConfig             *ComputeFirewallLog     `json:"logConfig,omitempty"`
}

// ComputeFirewallAction is the allow/deny rule shape — protocol +
// optional port list. Matches `compute#firewallAllowed` / `Denied`.
type ComputeFirewallAction struct {
	IPProtocol string   `json:"IPProtocol"`
	Ports      []string `json:"ports,omitempty"`
}

// ComputeFirewallLog enables logging on a firewall rule.
type ComputeFirewallLog struct {
	Enable   bool   `json:"enable"`
	Metadata string `json:"metadata,omitempty"`
}

// ComputeRouter mirrors `compute#router`. Cloud NAT lives on a Router
// — a workload that needs serverless egress (Cloud Run, Cloud
// Functions with VPC connector) provisions a Router with a `nats[]`
// entry. Without router CRUD, terraform's `google_compute_router` and
// `google_compute_router_nat` 404 against the sim.
type ComputeRouter struct {
	Kind              string             `json:"kind,omitempty"`
	Id                string             `json:"id,omitempty"`
	Name              string             `json:"name"`
	SelfLink          string             `json:"selfLink,omitempty"`
	CreationTimestamp string             `json:"creationTimestamp,omitempty"`
	Description       string             `json:"description,omitempty"`
	Network           string             `json:"network,omitempty"`
	Region            string             `json:"region,omitempty"`
	Bgp               *ComputeRouterBgp  `json:"bgp,omitempty"`
	Nats              []ComputeRouterNAT `json:"nats,omitempty"`
}

// ComputeRouterBgp mirrors `compute#router.bgp` — the Border Gateway
// Protocol settings for the router. The simulator stores them and reports them
// back; it speaks no BGP.
type ComputeRouterBgp struct {
	Asn               int32  `json:"asn,omitempty"`
	AdvertiseMode     string `json:"advertiseMode,omitempty"`
	KeepaliveInterval int32  `json:"keepaliveInterval,omitempty"`
}

// ComputeRouterNAT mirrors `compute#routerNat` — a Cloud NAT config
// embedded in a router. Real GCP supports per-NAT IP allocation
// (auto/manual), source-subnetwork-IP-ranges-to-NAT (LIST_OF_SUBNET-
// WORKS / ALL_SUBNETWORKS_ALL_IP_RANGES), TCP/UDP timeout overrides.
// The fields below cover what `google_compute_router_nat` round-trips.
type ComputeRouterNAT struct {
	Name string `json:"name"`
	// Type defaults to PUBLIC. terraform-provider-google treats this as a
	// forces-replacement field; the read-back must echo the default or every
	// refresh plans a NAT replacement.
	Type                          string                       `json:"type,omitempty"`
	NatIpAllocateOption           string                       `json:"natIpAllocateOption,omitempty"`
	NatIps                        []string                     `json:"natIps,omitempty"`
	SourceSubnetworkIpRangesToNat string                       `json:"sourceSubnetworkIpRangesToNat,omitempty"`
	Subnetworks                   []ComputeRouterNATSubnetwork `json:"subnetworks,omitempty"`
	MinPortsPerVm                 int32                        `json:"minPortsPerVm,omitempty"`
	UdpIdleTimeoutSec             int32                        `json:"udpIdleTimeoutSec,omitempty"`
	TcpEstablishedIdleTimeoutSec  int32                        `json:"tcpEstablishedIdleTimeoutSec,omitempty"`
	IcmpIdleTimeoutSec            int32                        `json:"icmpIdleTimeoutSec,omitempty"`
	LogConfig                     *ComputeRouterNATLogConfig   `json:"logConfig,omitempty"`
}

// defaultRouterNATTypes stamps the PUBLIC default onto any NAT that omits
// the type field, matching real Cloud NAT read-back (the field is
// forces-replacement in terraform-provider-google).
func defaultRouterNATTypes(nats []ComputeRouterNAT) {
	for i := range nats {
		if nats[i].Type == "" {
			nats[i].Type = "PUBLIC"
		}
	}
}

// ComputeRouterNATSubnetwork picks a specific subnet for NAT'ing.
type ComputeRouterNATSubnetwork struct {
	Name                  string   `json:"name"`
	SourceIpRangesToNat   []string `json:"sourceIpRangesToNat,omitempty"`
	SecondaryIpRangeNames []string `json:"secondaryIpRangeNames,omitempty"`
}

// ComputeRouterNATLogConfig enables NAT logging.
type ComputeRouterNATLogConfig struct {
	Enable bool   `json:"enable"`
	Filter string `json:"filter,omitempty"`
}

// ComputeAddress mirrors compute#address for regional external IP
// reservations. Cloud NAT manual mode references these resources from
// router.nats[].natIps, and Terraform/gcloud both use the regional
// addresses collection.
type ComputeAddress struct {
	Kind              string            `json:"kind,omitempty"`
	Id                string            `json:"id,omitempty"`
	Name              string            `json:"name"`
	SelfLink          string            `json:"selfLink,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
	Description       string            `json:"description,omitempty"`
	Address           string            `json:"address,omitempty"`
	AddressType       string            `json:"addressType,omitempty"`
	IPVersion         string            `json:"ipVersion,omitempty"`
	NetworkTier       string            `json:"networkTier,omitempty"`
	PrefixLength      int64             `json:"prefixLength,omitempty"`
	Region            string            `json:"region,omitempty"`
	Status            string            `json:"status,omitempty"`
	Users             []string          `json:"users,omitempty"`
	Purpose           string            `json:"purpose,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	LabelFingerprint  string            `json:"labelFingerprint,omitempty"`
}

type ComputeNetwork struct {
	Kind                  string `json:"kind"`
	Id                    string `json:"id"`
	Name                  string `json:"name"`
	SelfLink              string `json:"selfLink"`
	AutoCreateSubnetworks bool   `json:"autoCreateSubnetworks"`
	// Peerings is the network's peerings, which addPeering and its siblings
	// write and listPeeringRoutes reads the exchanged ranges from.
	Peerings      []ComputeNetworkPeering `json:"peerings,omitempty"`
	RoutingConfig struct {
		RoutingMode string `json:"routingMode"`
	} `json:"routingConfig"`
	// NetworkFirewallPolicyEnforcementOrder defaults to AFTER_CLASSIC_FIREWALL.
	// terraform-provider-google reads this back as a computed default; omitting
	// it makes every refresh plan an in-place update.
	NetworkFirewallPolicyEnforcementOrder string `json:"networkFirewallPolicyEnforcementOrder,omitempty"`
	CreationTimestamp                     string `json:"creationTimestamp"`
}

// ComputeNetworkPeering is one side of a peering between two networks.
type ComputeNetworkPeering struct {
	Name                 string `json:"name"`
	Network              string `json:"network,omitempty"`
	State                string `json:"state,omitempty"`
	StateDetails         string `json:"stateDetails,omitempty"`
	AutoCreateRoutes     bool   `json:"autoCreateRoutes,omitempty"`
	ExchangeSubnetRoutes bool   `json:"exchangeSubnetRoutes,omitempty"`
	ImportCustomRoutes   bool   `json:"importCustomRoutes,omitempty"`
	ExportCustomRoutes   bool   `json:"exportCustomRoutes,omitempty"`
}

type ComputeSubnetwork struct {
	Kind                  string `json:"kind"`
	Id                    string `json:"id"`
	Name                  string `json:"name"`
	SelfLink              string `json:"selfLink"`
	Network               string `json:"network"`
	IpCidrRange           string `json:"ipCidrRange"`
	Region                string `json:"region"`
	GatewayAddress        string `json:"gatewayAddress"`
	PrivateIpGoogleAccess bool   `json:"privateIpGoogleAccess"`
	CreationTimestamp     string `json:"creationTimestamp"`
}

// ComputeDisk mirrors `compute#disk` — the zonal persistent-disk resource a
// client creates and attaches to an instance. Field set covers what the Go
// SDK's `compute.NewDisksRESTClient` round-trips for create / get / list /
// delete / resize / setLabels — the subset terraform's `google_compute_disk`
// exercises.
type ComputeDisk struct {
	Kind              string `json:"kind,omitempty"`
	Id                string `json:"id,omitempty"`
	Name              string `json:"name"`
	SelfLink          string `json:"selfLink,omitempty"`
	CreationTimestamp string `json:"creationTimestamp,omitempty"`
	Description       string `json:"description,omitempty"`
	// SizeGb is int64-as-string in real GCP discovery docs (the SDK's
	// disk struct uses `int64,string` tag). The terraform provider
	// sends it as an unquoted number. gcpInt64 accepts both shapes on
	// input and always emits quoted-string on output (matches real GCP).
	SizeGb            gcpInt64          `json:"sizeGb,omitempty"`
	Zone              string            `json:"zone,omitempty"`
	Status            string            `json:"status,omitempty"`
	Type              string            `json:"type,omitempty"`
	SourceImage       string            `json:"sourceImage,omitempty"`
	SourceImageId     string            `json:"sourceImageId,omitempty"`
	SourceSnapshot    string            `json:"sourceSnapshot,omitempty"`
	Users             []string          `json:"users,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	LabelFingerprint  string            `json:"labelFingerprint,omitempty"`
	PhysicalBlockSize string            `json:"physicalBlockSizeBytes,omitempty"`
	// StoragePool is the pool the disk draws its capacity from, which is what
	// storagePools.listDisks reads to find a pool's disks.
	StoragePool string `json:"storagePool,omitempty"`

	// The members the disk's own verbs write: the schedules attached to it,
	// the key it is encrypted under, the replication it takes part in and the
	// state that reports.
	ResourcePolicies  []string       `json:"resourcePolicies,omitempty"`
	DiskEncryptionKey map[string]any `json:"diskEncryptionKey,omitempty"`
	AsyncPrimaryDisk  map[string]any `json:"asyncPrimaryDisk,omitempty"`
	ResourceStatus    map[string]any `json:"resourceStatus,omitempty"`
}

// ComputeInstanceStatus is a Compute Engine instance lifecycle status. Using a
// named type makes a mistyped status literal a compile error.
type ComputeInstanceStatus string

const (
	ComputeInstanceProvisioning ComputeInstanceStatus = "PROVISIONING"
	ComputeInstanceStaging      ComputeInstanceStatus = "STAGING"
	ComputeInstanceRunning      ComputeInstanceStatus = "RUNNING"
	ComputeInstanceStopping     ComputeInstanceStatus = "STOPPING"
	ComputeInstanceStopped      ComputeInstanceStatus = "STOPPED"
	ComputeInstanceSuspending   ComputeInstanceStatus = "SUSPENDING"
	ComputeInstanceSuspended    ComputeInstanceStatus = "SUSPENDED"
	ComputeInstanceTerminated   ComputeInstanceStatus = "TERMINATED"
)

type ComputeInstance struct {
	Kind              string                    `json:"kind,omitempty"`
	Id                string                    `json:"id,omitempty"`
	Name              string                    `json:"name"`
	SelfLink          string                    `json:"selfLink,omitempty"`
	CreationTimestamp string                    `json:"creationTimestamp,omitempty"`
	Description       string                    `json:"description,omitempty"`
	Zone              string                    `json:"zone,omitempty"`
	MachineType       string                    `json:"machineType,omitempty"`
	Status            ComputeInstanceStatus     `json:"status,omitempty"`
	StatusMessage     string                    `json:"statusMessage,omitempty"`
	Tags              *ComputeInstanceTags      `json:"tags,omitempty"`
	Labels            map[string]string         `json:"labels,omitempty"`
	LabelFingerprint  string                    `json:"labelFingerprint,omitempty"`
	Metadata          *ComputeInstanceMetadata  `json:"metadata,omitempty"`
	Disks             []ComputeInstanceDisk     `json:"disks,omitempty"`
	NetworkInterfaces []ComputeNetworkInterface `json:"networkInterfaces,omitempty"`
	CanIpForward      bool                      `json:"canIpForward,omitempty"`
	Scheduling        map[string]any            `json:"scheduling,omitempty"`
	ServiceAccounts   []map[string]any          `json:"serviceAccounts,omitempty"`

	// The members the instance's own set-verbs write. Each is stored because
	// the verb that sets it is only meaningful if a later read returns it.
	DeletionProtection              bool             `json:"deletionProtection,omitempty"`
	MinCpuPlatform                  string           `json:"minCpuPlatform,omitempty"`
	GuestAccelerators               []map[string]any `json:"guestAccelerators,omitempty"`
	ResourcePolicies                []string         `json:"resourcePolicies,omitempty"`
	ShieldedInstanceConfig          map[string]any   `json:"shieldedInstanceConfig,omitempty"`
	ShieldedInstanceIntegrityPolicy map[string]any   `json:"shieldedInstanceIntegrityPolicy,omitempty"`
	DisplayDevice                   map[string]any   `json:"displayDevice,omitempty"`
}

type ComputeInstanceTags struct {
	Items       []string `json:"items,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
}

type ComputeInstanceMetadata struct {
	Kind        string                        `json:"kind,omitempty"`
	Fingerprint string                        `json:"fingerprint,omitempty"`
	Items       []ComputeInstanceMetadataItem `json:"items,omitempty"`
}

type ComputeInstanceMetadataItem struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

type ComputeInstanceDisk struct {
	Kind             string         `json:"kind,omitempty"`
	Type             string         `json:"type,omitempty"`
	Mode             string         `json:"mode,omitempty"`
	Source           string         `json:"source,omitempty"`
	DeviceName       string         `json:"deviceName,omitempty"`
	Index            int64          `json:"index,omitempty"`
	Boot             bool           `json:"boot,omitempty"`
	AutoDelete       bool           `json:"autoDelete,omitempty"`
	InitializeParams map[string]any `json:"initializeParams,omitempty"`
	Interface        string         `json:"interface,omitempty"`
}

type ComputeNetworkInterface struct {
	Kind          string                `json:"kind,omitempty"`
	Name          string                `json:"name,omitempty"`
	Network       string                `json:"network,omitempty"`
	Subnetwork    string                `json:"subnetwork,omitempty"`
	NetworkIP     string                `json:"networkIP,omitempty"`
	StackType     string                `json:"stackType,omitempty"`
	AccessConfigs []ComputeAccessConfig `json:"accessConfigs,omitempty"`
	AliasIpRanges []map[string]string   `json:"aliasIpRanges,omitempty"`
	Fingerprint   string                `json:"fingerprint,omitempty"`
}

type ComputeAccessConfig struct {
	Kind        string `json:"kind,omitempty"`
	Name        string `json:"name,omitempty"`
	Type        string `json:"type,omitempty"`
	NatIP       string `json:"natIP,omitempty"`
	NetworkTier string `json:"networkTier,omitempty"`
	// SecurityPolicy is where an instance records the Cloud Armor policy
	// applied to its external address. instances.setSecurityPolicy names the
	// interfaces to apply it to, and this is the member the instance reports it
	// back on — the Instance schema itself declares no such field.
	SecurityPolicy string `json:"securityPolicy,omitempty"`
}

type ComputeHealthCheck struct {
	Kind               string                  `json:"kind,omitempty"`
	Id                 string                  `json:"id,omitempty"`
	Name               string                  `json:"name"`
	SelfLink           string                  `json:"selfLink,omitempty"`
	CreationTimestamp  string                  `json:"creationTimestamp,omitempty"`
	Description        string                  `json:"description,omitempty"`
	Type               string                  `json:"type,omitempty"`
	CheckIntervalSec   int64                   `json:"checkIntervalSec,omitempty"`
	TimeoutSec         int64                   `json:"timeoutSec,omitempty"`
	HealthyThreshold   int64                   `json:"healthyThreshold,omitempty"`
	UnhealthyThreshold int64                   `json:"unhealthyThreshold,omitempty"`
	HttpHealthCheck    *ComputeHTTPHealthCheck `json:"httpHealthCheck,omitempty"`
	HttpsHealthCheck   *ComputeHTTPHealthCheck `json:"httpsHealthCheck,omitempty"`
	Http2HealthCheck   *ComputeHTTPHealthCheck `json:"http2HealthCheck,omitempty"`
	TcpHealthCheck     *ComputeTCPHealthCheck  `json:"tcpHealthCheck,omitempty"`
}

// ComputeHTTPHealthCheck is the shape the HTTP, HTTPS and HTTP/2 health check
// settings share.
type ComputeHTTPHealthCheck struct {
	Port              int64  `json:"port,omitempty"`
	PortName          string `json:"portName,omitempty"`
	PortSpecification string `json:"portSpecification,omitempty"`
	Host              string `json:"host,omitempty"`
	RequestPath       string `json:"requestPath,omitempty"`
	Response          string `json:"response,omitempty"`
	ProxyHeader       string `json:"proxyHeader,omitempty"`
}

type ComputeTCPHealthCheck struct {
	Port              int64  `json:"port,omitempty"`
	PortName          string `json:"portName,omitempty"`
	PortSpecification string `json:"portSpecification,omitempty"`
	ProxyHeader       string `json:"proxyHeader,omitempty"`
}

type ComputeBackendService struct {
	Kind              string `json:"kind,omitempty"`
	Id                string `json:"id,omitempty"`
	Name              string `json:"name"`
	SelfLink          string `json:"selfLink,omitempty"`
	CreationTimestamp string `json:"creationTimestamp,omitempty"`
	Description       string `json:"description,omitempty"`
	Protocol          string `json:"protocol,omitempty"`
	PortName          string `json:"portName,omitempty"`
	// The Cloud Armor policies attached to the service, and the CDN policy the
	// signed-URL key names live in. setSecurityPolicy, setEdgeSecurityPolicy
	// and the signing-key verbs each write one of these.
	SecurityPolicy      string                         `json:"securityPolicy,omitempty"`
	EdgeSecurityPolicy  string                         `json:"edgeSecurityPolicy,omitempty"`
	CdnPolicy           map[string]any                 `json:"cdnPolicy,omitempty"`
	TimeoutSec          int64                          `json:"timeoutSec,omitempty"`
	LoadBalancingScheme string                         `json:"loadBalancingScheme,omitempty"`
	HealthChecks        []string                       `json:"healthChecks,omitempty"`
	Backends            []ComputeBackendServiceBackend `json:"backends,omitempty"`
	Fingerprint         string                         `json:"fingerprint,omitempty"`
}

type ComputeBackendServiceBackend struct {
	Group          string  `json:"group,omitempty"`
	BalancingMode  string  `json:"balancingMode,omitempty"`
	CapacityScaler float64 `json:"capacityScaler,omitempty"`
}

type ComputeInstanceGroup struct {
	Kind              string                          `json:"kind,omitempty"`
	Id                string                          `json:"id,omitempty"`
	Name              string                          `json:"name"`
	SelfLink          string                          `json:"selfLink,omitempty"`
	CreationTimestamp string                          `json:"creationTimestamp,omitempty"`
	Description       string                          `json:"description,omitempty"`
	Zone              string                          `json:"zone,omitempty"`
	Network           string                          `json:"network,omitempty"`
	NamedPorts        []ComputeInstanceGroupNamedPort `json:"namedPorts,omitempty"`
	Size              int64                           `json:"size"`
	Fingerprint       string                          `json:"fingerprint,omitempty"`
}

// storedComputeInstanceGroup is the persisted row backing an instance
// group: the wire-shape InstanceGroup (which has no instances member —
// membership rides the listInstances method and is summarized by the
// output-only size member) plus the membership the addInstances /
// removeInstances / listInstances handlers maintain. The embedding
// flattens on json.Marshal, so sim.Store persistence keeps the same row
// shape the membership has always been recovered from.
type storedComputeInstanceGroup struct {
	ComputeInstanceGroup
	// Instances is the group membership. Store-only: never emitted as
	// an InstanceGroup member.
	Instances []ComputeInstanceGroupInstance `json:"instances,omitempty"`
}

// wireInstanceGroup is the InstanceGroup resource emitted on the wire,
// with the output-only size member computed from the stored membership.
func wireInstanceGroup(g storedComputeInstanceGroup) ComputeInstanceGroup {
	out := g.ComputeInstanceGroup
	out.Size = int64(len(g.Instances))
	return out
}

type ComputeInstanceGroupNamedPort struct {
	Name string `json:"name,omitempty"`
	Port int64  `json:"port,omitempty"`
}

type ComputeInstanceGroupInstance struct {
	Instance string `json:"instance,omitempty"`
}

type ComputeURLMap struct {
	Kind              string                     `json:"kind,omitempty"`
	Id                string                     `json:"id,omitempty"`
	Name              string                     `json:"name"`
	SelfLink          string                     `json:"selfLink,omitempty"`
	CreationTimestamp string                     `json:"creationTimestamp,omitempty"`
	Description       string                     `json:"description,omitempty"`
	DefaultService    string                     `json:"defaultService,omitempty"`
	HostRules         []ComputeURLMapHostRule    `json:"hostRules,omitempty"`
	PathMatchers      []ComputeURLMapPathMatcher `json:"pathMatchers,omitempty"`
	Tests             []ComputeURLMapTest        `json:"tests,omitempty"`
	Fingerprint       string                     `json:"fingerprint,omitempty"`
}

// ComputeURLMapTest is one of the request-to-service expectations a URL map
// carries, which urlMaps.validate checks against the map's own routing.
type ComputeURLMapTest struct {
	Description string `json:"description,omitempty"`
	Host        string `json:"host,omitempty"`
	Path        string `json:"path,omitempty"`
	Service     string `json:"service,omitempty"`
}

type ComputeURLMapHostRule struct {
	Hosts       []string `json:"hosts,omitempty"`
	PathMatcher string   `json:"pathMatcher,omitempty"`
}

type ComputeURLMapPathMatcher struct {
	Name           string                  `json:"name,omitempty"`
	DefaultService string                  `json:"defaultService,omitempty"`
	PathRules      []ComputeURLMapPathRule `json:"pathRules,omitempty"`
}

type ComputeURLMapPathRule struct {
	Paths   []string `json:"paths,omitempty"`
	Service string   `json:"service,omitempty"`
}

type ComputeTargetHTTPProxy struct {
	Kind              string `json:"kind,omitempty"`
	Id                string `json:"id,omitempty"`
	Name              string `json:"name"`
	SelfLink          string `json:"selfLink,omitempty"`
	CreationTimestamp string `json:"creationTimestamp,omitempty"`
	Description       string `json:"description,omitempty"`
	UrlMap            string `json:"urlMap,omitempty"`
}

type ComputeForwardingRule struct {
	Kind              string `json:"kind,omitempty"`
	Id                string `json:"id,omitempty"`
	Name              string `json:"name"`
	SelfLink          string `json:"selfLink,omitempty"`
	CreationTimestamp string `json:"creationTimestamp,omitempty"`
	Description       string `json:"description,omitempty"`
	IPAddress         string `json:"IPAddress,omitempty"`
	IPProtocol        string `json:"IPProtocol,omitempty"`
	PortRange         string `json:"portRange,omitempty"`
	// The labels setLabels writes, with the fingerprint that guards them.
	Labels              map[string]string `json:"labels,omitempty"`
	LabelFingerprint    string            `json:"labelFingerprint,omitempty"`
	Target              string            `json:"target,omitempty"`
	LoadBalancingScheme string            `json:"loadBalancingScheme,omitempty"`
	NetworkTier         string            `json:"networkTier,omitempty"`
}

// ComputeInstanceTemplate mirrors `compute#instanceTemplate`. Field set covers
// what terraform-provider-google `google_compute_instance_template` and the
// Go SDK's InstanceTemplates REST client round-trip.
type ComputeInstanceTemplate struct {
	Kind              string         `json:"kind"`
	Id                string         `json:"id"`
	Name              string         `json:"name"`
	SelfLink          string         `json:"selfLink"`
	CreationTimestamp string         `json:"creationTimestamp"`
	Description       string         `json:"description,omitempty"`
	Properties        map[string]any `json:"properties"`
}

var (
	gcpSubnetworks sim.Store[ComputeSubnetwork]
	gcpAddresses   sim.Store[ComputeAddress]
	gcpFirewalls   sim.Store[ComputeFirewall]
	gcpInstances   sim.Store[ComputeInstance]
	// gcpNormalizeInstance fills an instance the way instances.insert fills
	// one — identity, zone, disks, and the real network interface it is
	// attached to. instances.bulkInsert builds its run through the same
	// function so a bulk-created instance is the same object a singly-created
	// one is, rather than a record shaped like it.
	gcpNormalizeInstance func(ctx context.Context, project, zone string, inst *ComputeInstance) error
	gcpInstanceGroups    sim.Store[storedComputeInstanceGroup]
	gcpHealthChecks      sim.Store[ComputeHealthCheck]
	gcpBackendServices   sim.Store[ComputeBackendService]
	gcpURLMaps           sim.Store[ComputeURLMap]
	gcpTargetHTTPProxies sim.Store[ComputeTargetHTTPProxy]
	gcpForwardingRules   sim.Store[ComputeForwardingRule]
	gcpRouters           sim.Store[ComputeRouter]
)

func registerCompute(srv *sim.Server) {
	computeOpRegistry = sim.MakeStore[ComputeOperationRecord](srv.DB(), "compute_operations")
	networks := sim.MakeStore[ComputeNetwork](srv.DB(), "compute_networks")
	subnetworks := sim.MakeStore[ComputeSubnetwork](srv.DB(), "compute_subnetworks")
	// Shared so the peering verbs can read a network and the ranges its peer
	// exchanges, as gcpFirewalls is shared for the effective-firewalls reads.
	gcpComputeNetworks, gcpComputeSubnetworks = networks, subnetworks
	gcpSubnetworks = subnetworks
	instanceTemplates := sim.MakeStore[ComputeInstanceTemplate](srv.DB(), "compute_instance_templates")

	firewalls := sim.MakeStore[ComputeFirewall](srv.DB(), "compute_firewalls")
	gcpFirewalls = firewalls
	addresses := sim.MakeStore[ComputeAddress](srv.DB(), "compute_addresses")
	gcpComputeRegionAddresses = addresses
	routers := sim.MakeStore[ComputeRouter](srv.DB(), "compute_routers")
	gcpAddresses = addresses
	gcpRouters = routers

	registerComputeNetworks(srv, networks, subnetworks)
	registerComputeInstanceTemplates(srv, instanceTemplates)
	registerComputeFirewalls(srv, firewalls)
	registerComputeAddressesAndRouters(srv, addresses, routers)

	// Global operations (for network creates, deletes, etc.)
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/operations/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeWriteOperation(w, sim.PathParam(r, "name"))
	})

	// Regional operations (for subnetwork creates, deletes, etc.)
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/operations/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeWriteOperation(w, sim.PathParam(r, "name"))
	})
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/operations/{name}/wait", func(w http.ResponseWriter, r *http.Request) {
		computeWaitOperation(w, r, sim.PathParam(r, "name"))
	})

	registerComputeCatalog(srv)
	registerComputeInstances(srv, networks, subnetworks)
	registerComputeInstanceGroups(srv)
	registerComputeDisks(srv)
	registerComputeZones(srv)
	registerComputeLoadBalancing(srv)
}

// gcpIngressPacketRules compiles the enabled ingress firewall rules of the
// interface's network that target the instance, in Compute Engine's order:
// lower priority value first, and a deny before an allow of equal priority.
func gcpIngressPacketRules(target ComputeInstance, targetNIC ComputeNetworkInterface) ([]netrules.PacketRule, error) {
	if gcpFirewalls == nil {
		return nil, nil
	}
	var firewalls []ComputeFirewall
	for _, fw := range gcpFirewallsByNetwork.LookupAll(gcpFirewalls, targetNIC.Network, func(fw ComputeFirewall) []string {
		return []string{gcpCanonicalComputeRef(fw.Network, gcpProjectFromSelfLink(fw.SelfLink), "global", "networks", "default")}
	}) {
		if fw.Disabled || !strings.EqualFold(gcpDefaultString(fw.Direction, "INGRESS"), "INGRESS") {
			continue
		}
		firewalls = append(firewalls, fw)
	}
	sort.SliceStable(firewalls, func(i, j int) bool { return firewalls[i].Name < firewalls[j].Name })
	var rules []netrules.PrioritizedRule
	add := func(priority int, actions []ComputeFirewallAction, sources []string, verdict string) error {
		for _, action := range actions {
			expanded, err := netrules.ExpandRules(action.IPProtocol, sources, action.Ports, verdict)
			if err != nil {
				return err
			}
			for _, rule := range expanded {
				rules = append(rules, netrules.PrioritizedRule{Priority: priority, Rule: rule})
			}
		}
		return nil
	}
	for _, fw := range firewalls {
		if !gcpFirewallTargetsInstance(fw, target) {
			continue
		}
		sources := gcpFirewallSources(fw, targetNIC.Network)
		if err := add(int(fw.Priority), fw.Denied, sources, "drop"); err != nil {
			return nil, fmt.Errorf("firewall %s: %w", fw.Name, err)
		}
		if err := add(int(fw.Priority), fw.Allowed, sources, "accept"); err != nil {
			return nil, fmt.Errorf("firewall %s: %w", fw.Name, err)
		}
	}
	return netrules.FlattenByPriority(rules), nil
}

// gcpInvalidFirewallPort names the first port specification Compute Engine
// would not accept, as its invalid-field message, or returns "".
func gcpInvalidFirewallPort(fw ComputeFirewall) string {
	for _, list := range []struct {
		field   string
		actions []ComputeFirewallAction
	}{{"allowed", fw.Allowed}, {"denied", fw.Denied}} {
		for i, action := range list.actions {
			for j, port := range action.Ports {
				if _, _, err := netrules.PortRange(port); err != nil || port == "" || port == "*" {
					return fmt.Sprintf("Invalid value for field 'resource.%s[%d].ports[%d]': '%s'.", list.field, i, j, port)
				}
			}
		}
	}
	return ""
}

func gcpFirewallTargetsInstance(fw ComputeFirewall, inst ComputeInstance) bool {
	if len(fw.TargetTags) == 0 {
		return true
	}
	for _, want := range fw.TargetTags {
		if gcpInstanceHasTag(inst, want) {
			return true
		}
	}
	return false
}

func gcpFirewallSources(fw ComputeFirewall, network string) []string {
	var out []string
	out = append(out, fw.SourceRanges...)
	if len(fw.SourceTags) > 0 && gcpInstances != nil {
		seen := map[string]bool{}
		for _, tag := range fw.SourceTags {
			for _, inst := range gcpInstancesByTag.LookupAll(gcpInstances, tag, gcpInstanceTagKeys) {
				if seen[inst.SelfLink+"/"+inst.Name] {
					continue
				}
				seen[inst.SelfLink+"/"+inst.Name] = true
				for _, ni := range inst.NetworkInterfaces {
					if ni.Network == network && ni.NetworkIP != "" {
						out = append(out, ni.NetworkIP+"/32")
					}
				}
			}
		}
	}
	if len(out) == 0 {
		return []string{"0.0.0.0/0"}
	}
	return out
}

func gcpInstanceHasTag(inst ComputeInstance, tag string) bool {
	if inst.Tags == nil {
		return false
	}
	for _, got := range inst.Tags.Items {
		if got == tag {
			return true
		}
	}
	return false
}

func gcpProjectFromSelfLink(selfLink string) string {
	parts := strings.Split(strings.TrimPrefix(selfLink, "https://www.googleapis.com/compute/v1/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "projects" {
			return parts[i+1]
		}
	}
	return ""
}

func gcpCanonicalComputeRef(ref, project, scope, collection, fallbackName string) string {
	ref = strings.TrimPrefix(ref, "https://www.googleapis.com/compute/v1/")
	if ref == "" && fallbackName != "" {
		ref = fallbackName
	}
	if strings.Contains(ref, "/") {
		return ref
	}
	if project == "" {
		return ref
	}
	return fmt.Sprintf("projects/%s/%s/%s/%s", project, scope, collection, ref)
}

func gcpDefaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func computeRegionalAddressLink(project, region, name string) string {
	return fmt.Sprintf("projects/%s/regions/%s/addresses/%s", project, region, name)
}

func gcpInstanceGroupHasInstance(group storedComputeInstanceGroup, instance string) bool {
	for _, got := range group.Instances {
		if got.Instance == instance {
			return true
		}
	}
	return false
}

func validateRouterNATAddresses(project, region string, nats []ComputeRouterNAT, addresses sim.Store[ComputeAddress]) error {
	for _, nat := range nats {
		if !strings.EqualFold(nat.NatIpAllocateOption, "MANUAL_ONLY") {
			continue
		}
		if len(nat.NatIps) == 0 {
			return fmt.Errorf("NAT %q uses MANUAL_ONLY but does not reference any regional addresses", nat.Name)
		}
		for _, ref := range nat.NatIps {
			link := normalizeComputeRegionalAddressRef(project, region, ref)
			if _, ok := addresses.Get(link); !ok {
				return fmt.Errorf("NAT %q references missing regional address %q", nat.Name, ref)
			}
		}
	}
	return nil
}

func normalizeComputeRegionalAddressRef(project, region, ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "https://www.googleapis.com/compute/v1/")
	ref = strings.TrimPrefix(ref, "https://compute.googleapis.com/compute/v1/")
	if idx := strings.Index(ref, "/projects/"); idx >= 0 {
		return strings.TrimPrefix(ref[idx:], "/")
	}
	if strings.HasPrefix(ref, "projects/") {
		return ref
	}
	if strings.HasPrefix(ref, "regions/") {
		return "projects/" + project + "/" + ref
	}
	if strings.Contains(ref, "/") {
		return ref
	}
	return computeRegionalAddressLink(project, region, ref)
}

func normalizeComputeGlobalNetworkRef(project, ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "https://www.googleapis.com/compute/v1/")
	ref = strings.TrimPrefix(ref, "https://compute.googleapis.com/compute/v1/")
	if idx := strings.Index(ref, "/projects/"); idx >= 0 {
		return strings.TrimPrefix(ref[idx:], "/")
	}
	if strings.HasPrefix(ref, "projects/") {
		return ref
	}
	if strings.HasPrefix(ref, "global/networks/") {
		return "projects/" + project + "/" + ref
	}
	if strings.Contains(ref, "/") {
		return ref
	}
	return computeNetworkSelfLink(project, ref)
}

func normalizeComputeSubnetworkRef(project, region, ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "https://www.googleapis.com/compute/v1/")
	ref = strings.TrimPrefix(ref, "https://compute.googleapis.com/compute/v1/")
	if idx := strings.Index(ref, "/projects/"); idx >= 0 {
		return strings.TrimPrefix(ref[idx:], "/")
	}
	if strings.HasPrefix(ref, "projects/") {
		return ref
	}
	if strings.HasPrefix(ref, "regions/") {
		return "projects/" + project + "/" + ref
	}
	if strings.Contains(ref, "/") {
		return ref
	}
	return fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, ref)
}

// gcpCreateDefaultNetworkFirewalls creates the ingress rules Compute Engine
// creates with a project's default network.
func gcpCreateDefaultNetworkFirewalls(project, networkLink string) {
	everyPort := func(protocol string) ComputeFirewallAction {
		return ComputeFirewallAction{IPProtocol: protocol, Ports: []string{"0-65535"}}
	}
	rules := []ComputeFirewall{
		{
			Name:         "default-allow-internal",
			Description:  "Allow internal traffic on the default network",
			SourceRanges: []string{"10.128.0.0/9"},
			Allowed:      []ComputeFirewallAction{everyPort("tcp"), everyPort("udp"), {IPProtocol: "icmp"}},
		},
		{
			Name:         "default-allow-ssh",
			Description:  "Allow SSH from anywhere",
			SourceRanges: []string{"0.0.0.0/0"},
			Allowed:      []ComputeFirewallAction{{IPProtocol: "tcp", Ports: []string{"22"}}},
		},
		{
			Name:         "default-allow-rdp",
			Description:  "Allow RDP from anywhere",
			SourceRanges: []string{"0.0.0.0/0"},
			Allowed:      []ComputeFirewallAction{{IPProtocol: "tcp", Ports: []string{"3389"}}},
		},
		{
			Name:         "default-allow-icmp",
			Description:  "Allow ICMP from anywhere",
			SourceRanges: []string{"0.0.0.0/0"},
			Allowed:      []ComputeFirewallAction{{IPProtocol: "icmp"}},
		},
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, fw := range rules {
		fw.Kind = "compute#firewall"
		fw.Id = computeNumericID()
		fw.SelfLink = fmt.Sprintf("projects/%s/global/firewalls/%s", project, fw.Name)
		fw.CreationTimestamp = now
		fw.Network = networkLink
		fw.Direction = "INGRESS"
		fw.Priority = 65534
		if _, exists := gcpFirewalls.Get(fw.SelfLink); exists {
			continue
		}
		gcpFirewalls.Put(fw.SelfLink, fw)
	}
	gcpReapplyIngress()
}

func ensureGCPAutoModeSubnetwork(ctx context.Context, networks sim.Store[ComputeNetwork], subnetworks sim.Store[ComputeSubnetwork], project, region, networkLink string) (string, error) {
	networkLink = normalizeComputeGlobalNetworkRef(project, networkLink)
	net, ok := networks.Get(networkLink)
	if !ok && networkLink == fmt.Sprintf("projects/%s/global/networks/default", project) {
		net = ComputeNetwork{
			Kind:                  "compute#network",
			Id:                    computeNumericID(),
			Name:                  "default",
			SelfLink:              networkLink,
			AutoCreateSubnetworks: true,
			CreationTimestamp:     time.Now().UTC().Format(time.RFC3339),
		}
		net.RoutingConfig.RoutingMode = "REGIONAL"
		networks.Put(networkLink, net)
		gcpCreateDefaultNetworkFirewalls(project, networkLink)
		ok = true
	}
	if !ok {
		return "", fmt.Errorf("network %s not found", networkLink)
	}
	if !net.AutoCreateSubnetworks {
		return "", fmt.Errorf("network %s has no automatic subnet in region %s", networkLink, region)
	}
	cidr, ok := gcpAutoModeSubnetCIDRs[region]
	if !ok {
		return "", fmt.Errorf("auto mode subnet range for region %s is not implemented", region)
	}
	subnetLink := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, net.Name)
	if _, ok := subnetworks.Get(subnetLink); ok {
		return subnetLink, nil
	}
	subnet := ComputeSubnetwork{
		Kind:              "compute#subnetwork",
		Id:                computeNumericID(),
		Name:              net.Name,
		SelfLink:          subnetLink,
		Network:           networkLink,
		IpCidrRange:       cidr,
		Region:            fmt.Sprintf("projects/%s/regions/%s", project, region),
		GatewayAddress:    gcpSubnetGateway(cidr),
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
	}
	subnetworks.Put(subnetLink, subnet)
	return subnetLink, nil
}

// registerComputeZones serves GET /compute/v1/projects/{project}/zones
// + .../zones/{zone}. gcloud probes a zone's existence before disk
// CRUD; without these handlers it gets 404 and crashes during retry.
// Real GCE returns a list of all zones in the project.
func registerComputeZones(srv *sim.Server) {
	zoneJSON := func(project, zone string) map[string]any {
		return map[string]any{
			"kind":                  "compute#zone",
			"id":                    computeNumericID(),
			"name":                  zone,
			"description":           zone,
			"status":                "UP",
			"selfLink":              fmt.Sprintf("projects/%s/zones/%s", project, zone),
			"region":                fmt.Sprintf("projects/%s/regions/%s", project, regionFromZone(zone)),
			"availableCpuPlatforms": []string{"Intel Skylake"},
		}
	}
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, zoneJSON(sim.PathParam(r, "project"), sim.PathParam(r, "zone")))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zones := []string{"us-central1-a", "us-central1-b", "us-east1-a", "europe-west1-b", "europe-west1-c"}
		items := make([]map[string]any, 0, len(zones))
		for _, z := range zones {
			items = append(items, zoneJSON(project, z))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":  "compute#zoneList",
			"items": items,
		})
	})
}

func regionFromZone(zone string) string {
	if i := strings.LastIndex(zone, "-"); i > 0 {
		return zone[:i]
	}
	return zone
}

func registerComputeCatalog(srv *sim.Server) {
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/machineTypes/{machineType}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "machineType")
		mt, ok := gcpLookupMachineType(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The resource 'projects/%s/zones/%s/machineTypes/%s' was not found", project, zone, name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, gcpMachineTypeJSON(project, zone, mt))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/machineTypes", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		items := []map[string]any{}
		for _, name := range gcpMachineTypeNames() {
			mt, _ := gcpLookupMachineType(name)
			items = append(items, gcpMachineTypeJSON(project, zone, mt))
		}
		listed, ok := gcpApplyComputeListParams(w, r, items)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#machineTypeList", "items": listed})
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/diskTypes/{diskType}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "diskType")
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":     "compute#diskType",
			"id":       computeNumericID(),
			"name":     name,
			"zone":     fmt.Sprintf("projects/%s/zones/%s", project, zone),
			"selfLink": fmt.Sprintf("projects/%s/zones/%s/diskTypes/%s", project, zone, name),
		})
	})
	// imageViews are a read-only view of the SAME global images, addressed with
	// a regional context: "Returns the specified global ImageView resource,
	// with a regional context." Unlike regions.projectViews — which exists to
	// expose a replica's lag and is therefore declared unserved — a view adds
	// no state of its own, so it is served from the image collection rather
	// than refused.
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/imageViews/{resourceId}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "resourceId")
		image, ok := gcpImageResource(project, name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The resource '%s' was not found", gcpImageLink(project, name))
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"image": image})
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/imageViews", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		items := []any{}
		for _, image := range gcpImageList(project) {
			items = append(items, image)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":     "compute#imageViewsListResponse",
			"id":       computeNumericID(),
			"items":    items,
			"selfLink": fmt.Sprintf("projects/%s/regions/%s/imageViews", project, region),
		})
	})
	registerComputeImages(srv)
}

// computeZoneOp records and renders a zonal operation whose work is already
// complete when the response is written.
func computeZoneOp(project, zone, target, opType string) map[string]any {
	return newComputeOpWithType(project, "zones/"+zone, target, opType)
}

// computeInstancePreRunning is the set of statuses an instance holds while an
// insert operation is still bringing it up, before it reaches RUNNING.
func computeInstancePreRunning(status ComputeInstanceStatus) bool {
	return status == ComputeInstanceProvisioning || status == ComputeInstanceStaging
}

// computeInstanceMoving reports whether an instance in status has a machine
// on its way up or on its way down.
func computeInstanceMoving(status ComputeInstanceStatus) bool {
	switch status {
	case ComputeInstanceStopping, ComputeInstanceSuspending:
		return true
	}
	return computeInstancePreRunning(status)
}

// recoverComputeInstances settles every instance a restart caught moving. The
// goroutine carrying the move died with the previous process and its operation
// finishes with an error (recoverComputeOperations), so the instance lands
// where a failed move leaves it: TERMINATED, with the statusMessage Compute
// Engine gives an instance whose machine went away.
func recoverComputeInstances(instances sim.Store[ComputeInstance]) {
	for _, inst := range instances.List() {
		if !computeInstanceMoving(inst.Status) {
			continue
		}
		instances.Update(inst.SelfLink, func(in *ComputeInstance) {
			if !computeInstanceMoving(in.Status) {
				return
			}
			in.Status = ComputeInstanceTerminated
			in.StatusMessage = "Instance workload not found after control-plane restart"
		})
	}
}

// recoverComputeOperations settles every operation a restart caught still
// running. The goroutine that was carrying the work out died with the previous
// process, so nothing will ever finish it; leaving it RUNNING would have a
// client poll it forever. Compute Engine's own contract is that an operation
// reaches DONE, carrying the error when the work did not complete, so each one
// is finished that way rather than left to hang.
func recoverComputeOperations() {
	for _, rec := range computeOpRegistry.List() {
		if rec.Status == "DONE" {
			continue
		}
		computeOpFinish(rec.Name, fmt.Errorf("operation did not complete before the control plane restarted"))
	}
}

// computeRangeIsWider reports whether one CIDR range covers more addresses than
// another. A subnetwork's range only ever grows, and a range grows by taking a
// shorter prefix — /20 is wider than /24.
func computeRangeIsWider(current, proposed string) (bool, error) {
	prefixLength := func(cidr string) (int, error) {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return 0, fmt.Errorf("%q is not a CIDR range", cidr)
		}
		ones, _ := network.Mask.Size()
		return ones, nil
	}
	held, err := prefixLength(current)
	if err != nil {
		return false, err
	}
	wanted, err := prefixLength(proposed)
	if err != nil {
		return false, err
	}
	return wanted < held, nil
}

var (
	gcpFirewallsByNetwork sim.GenerationIndex[ComputeFirewall]
	gcpInstancesByTag     sim.GenerationIndex[ComputeInstance]
)

func gcpInstanceTagKeys(inst ComputeInstance) []string {
	if inst.Tags == nil {
		return nil
	}
	return inst.Tags.Items
}
