package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	"github.com/e6qu/sockerless-cloud/sim"
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

func computeOpKnown(name string) bool { _, ok := computeOpRegistry.Get(name); return ok }

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

// computeInstanceBootBudget is how long the boot behind an insert operation is
// given before it is abandoned and the operation reports the failure. It is
// deliberately longer than computeOperationWaitBudget: a boot that overruns a
// client's patience must still reach a verdict the next poll can read, rather
// than being cut short by whoever stopped waiting first.
const computeInstanceBootBudget = 5 * time.Minute

// computeWaitOperation implements that contract: it polls the record until the
// operation is DONE, the budget runs out, or the client goes away, then answers
// with whatever state the operation is in.
func computeWaitOperation(w http.ResponseWriter, r *http.Request, name string) {
	deadline := time.Now().Add(computeOperationWaitBudget)
	for {
		rec, ok := computeOpRegistry.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "notFound", "operation %q not found", name)
			return
		}
		if rec.Status == "DONE" || time.Now().After(deadline) {
			sim.WriteJSON(w, http.StatusOK, computeOpJSON(rec))
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
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
		Name:          "operation-" + generateUUID()[:8],
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
	TcpHealthCheck     *ComputeTCPHealthCheck  `json:"tcpHealthCheck,omitempty"`
}

type ComputeHTTPHealthCheck struct {
	Port        int64  `json:"port,omitempty"`
	RequestPath string `json:"requestPath,omitempty"`
	ProxyHeader string `json:"proxyHeader,omitempty"`
}

type ComputeTCPHealthCheck struct {
	Port        int64  `json:"port,omitempty"`
	ProxyHeader string `json:"proxyHeader,omitempty"`
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

	// Create network
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/networks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")

		var req struct {
			Name                  string `json:"name"`
			AutoCreateSubnetworks bool   `json:"autoCreateSubnetworks"`
			// Peerings is the network's peerings, which addPeering and its siblings
			// write and listPeeringRoutes reads the exchanged ranges from.
			Peerings      []ComputeNetworkPeering `json:"peerings,omitempty"`
			RoutingConfig struct {
				RoutingMode string `json:"routingMode"`
			} `json:"routingConfig"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		selfLink := computeNetworkSelfLink(project, req.Name)
		net := ComputeNetwork{
			Kind:                  "compute#network",
			Id:                    computeNumericID(),
			Name:                  req.Name,
			SelfLink:              selfLink,
			AutoCreateSubnetworks: req.AutoCreateSubnetworks,
			CreationTimestamp:     time.Now().UTC().Format(time.RFC3339),
		}
		net.RoutingConfig.RoutingMode = req.RoutingConfig.RoutingMode
		if net.RoutingConfig.RoutingMode == "" {
			net.RoutingConfig.RoutingMode = "REGIONAL"
		}
		net.NetworkFirewallPolicyEnforcementOrder = "AFTER_CLASSIC_FIREWALL"
		if _, exists := networks.Get(selfLink); computeConflict(w, exists, "network", req.Name) {
			return
		}
		if !gcpRequireNetworkHost(w) {
			return
		}
		if err := gcpCreateRealNetwork(r.Context(), selfLink); err != nil {
			GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to create real VPC network fabric: %v", err)
			return
		}
		networks.Put(selfLink, net)

		op := newComputeOp(project, "global", selfLink)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Get network
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/networks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := computeNetworkSelfLink(project, name)

		net, ok := networks.Get(selfLink)
		if !ok {
			GCPErrorf(w, 404, "NOT_FOUND", "Network %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, net)
	})

	// List networks
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/networks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/global/networks/", project)
		all := networks.Filter(func(n ComputeNetwork) bool {
			return strings.HasPrefix(n.SelfLink, prefix)
		})
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		all = gcpApplyListParams(all, r)
		page, next, ok := paginateListCompute(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "compute#networkList", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// Delete network
	srv.HandleFunc("DELETE /compute/v1/projects/{project}/global/networks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := computeNetworkSelfLink(project, name)

		if computeNotFound(w, networks.Delete(selfLink), "network", name) {
			return
		}
		if err := gcpDeleteRealNetwork(r.Context(), selfLink); err != nil {
			GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to delete real VPC network fabric: %v", err)
			return
		}
		// Real Compute Engine stamps a delete operation `operationType: "delete"`;
		// gcloud keys off it to report the resource as deleted instead of
		// re-fetching it (which then 404s and fails the command).
		op := newComputeOpWithType(project, "global", selfLink, "delete")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Patch network (for updates)
	srv.HandleFunc("PATCH /compute/v1/projects/{project}/global/networks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := computeNetworkSelfLink(project, name)

		var req struct {
			RoutingConfig struct {
				RoutingMode string `json:"routingMode"`
			} `json:"routingConfig"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		networks.Update(selfLink, func(n *ComputeNetwork) {
			if req.RoutingConfig.RoutingMode != "" {
				n.RoutingConfig.RoutingMode = req.RoutingConfig.RoutingMode
			}
		})

		op := newComputeOp(project, "global", selfLink)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Instance templates — global, no networking side-effects.
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/instanceTemplates", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var req ComputeInstanceTemplate
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if req.Name == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "name is required")
			return
		}
		selfLink := fmt.Sprintf("https://www.googleapis.com/compute/v1/projects/%s/global/instanceTemplates/%s", project, req.Name)
		req.Kind = "compute#instanceTemplate"
		req.Id = computeNumericID()
		req.SelfLink = selfLink
		req.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		if req.Properties == nil {
			req.Properties = map[string]any{}
		}
		storeKey := fmt.Sprintf("projects/%s/global/instanceTemplates/%s", project, req.Name)
		instanceTemplates.Put(storeKey, req)
		op := newComputeOp(project, "global", selfLink)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/instanceTemplates/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		storeKey := fmt.Sprintf("projects/%s/global/instanceTemplates/%s", project, name)
		tmpl, ok := instanceTemplates.Get(storeKey)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instanceTemplate %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, tmpl)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/instanceTemplates", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/global/instanceTemplates/", project)
		all := instanceTemplates.Filter(func(t ComputeInstanceTemplate) bool {
			return strings.HasPrefix(t.SelfLink, "https://www.googleapis.com/compute/v1/"+prefix)
		})
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		all = gcpApplyListParams(all, r)
		page, next, ok := paginateListCompute(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "compute#instanceTemplateList", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/global/instanceTemplates/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		storeKey := fmt.Sprintf("projects/%s/global/instanceTemplates/%s", project, name)
		selfLink := fmt.Sprintf("https://www.googleapis.com/compute/v1/%s", storeKey)
		instanceTemplates.Delete(storeKey)
		op := newComputeOpWithType(project, "global", selfLink, "delete")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Aggregated list — gcloud instance-templates list calls this endpoint.
	// Real GCP returns items keyed by scope ("global" for global resources).
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/instanceTemplates", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/global/instanceTemplates/", project)
		all := instanceTemplates.Filter(func(t ComputeInstanceTemplate) bool {
			return strings.HasPrefix(t.SelfLink, "https://www.googleapis.com/compute/v1/"+prefix)
		})
		if all == nil {
			all = []ComputeInstanceTemplate{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#instanceTemplateAggregatedList",
			"items": map[string]any{
				"global": map[string]any{"instanceTemplates": all},
			},
		})
	})

	// Create subnetwork
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/subnetworks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")

		var req struct {
			Name                  string `json:"name"`
			Network               string `json:"network"`
			IpCidrRange           string `json:"ipCidrRange"`
			PrivateIpGoogleAccess bool   `json:"privateIpGoogleAccess"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		selfLink := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, req.Name)
		subnet := ComputeSubnetwork{
			Kind:                  "compute#subnetwork",
			Id:                    computeNumericID(),
			Name:                  req.Name,
			SelfLink:              selfLink,
			Network:               req.Network,
			IpCidrRange:           req.IpCidrRange,
			Region:                fmt.Sprintf("projects/%s/regions/%s", project, region),
			GatewayAddress:        gcpSubnetGateway(req.IpCidrRange),
			PrivateIpGoogleAccess: req.PrivateIpGoogleAccess,
			CreationTimestamp:     time.Now().UTC().Format(time.RFC3339),
		}
		if _, exists := subnetworks.Get(selfLink); computeConflict(w, exists, "subnetwork", req.Name) {
			return
		}
		if !gcpRequireNetworkHost(w) {
			return
		}
		if err := gcpCreateRealSubnetwork(r.Context(), subnet); err != nil {
			GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to create real subnet network fabric: %v", err)
			return
		}
		subnetworks.Put(selfLink, subnet)

		op := newComputeOp(project, "regions/"+region, selfLink)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Get subnetwork
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, name)

		subnet, ok := subnetworks.Get(selfLink)
		if !ok {
			GCPErrorf(w, 404, "NOT_FOUND", "Subnetwork %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, subnet)
	})

	// List subnetworks in a region (compute.subnetworks.list). Regional, so
	// the reply is a SubnetworkList — `kind` plus `items` — rather than the
	// scope-keyed map subnetworks.aggregatedList returns.
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/subnetworks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		prefix := fmt.Sprintf("projects/%s/regions/%s/subnetworks/", project, region)
		all := subnetworks.Filter(func(subnet ComputeSubnetwork) bool {
			return strings.HasPrefix(subnet.SelfLink, prefix)
		})
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		all = gcpApplyListParams(all, r)
		page, next, ok := paginateListCompute(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "compute#subnetworkList", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// Delete subnetwork
	// A subnetwork's range only grows: shrinking it would strand the addresses
	// already handed out of it, so Compute Engine refuses that and so does
	// this. The comparison is on the prefix length, where a smaller number is
	// a larger range.
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}/expandIpCidrRange", func(w http.ResponseWriter, r *http.Request) {
		project, region, name := sim.PathParam(r, "project"), sim.PathParam(r, "region"), sim.PathParam(r, "name")
		var req struct {
			IpCidrRange string `json:"ipCidrRange"`
		}
		if err := sim.ReadJSON(r, &req); err != nil || req.IpCidrRange == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"expandIpCidrRange needs the range to expand to")
			return
		}
		selfLink := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, name)
		held, ok := subnetworks.Get(selfLink)
		if !ok {
			GCPErrorf(w, 404, "NOT_FOUND", "Subnetwork %s not found", name)
			return
		}
		wider, err := computeRangeIsWider(held.IpCidrRange, req.IpCidrRange)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		if !wider {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"a subnetwork range can only expand: %s is not wider than %s", req.IpCidrRange, held.IpCidrRange)
			return
		}
		held.IpCidrRange = req.IpCidrRange
		subnetworks.Put(selfLink, held)
		sim.WriteJSON(w, http.StatusOK,
			newComputeOpWithType(project, "regions/"+region, selfLink, "expandIpCidrRange"))
	})

	// A subnetwork's patch, which edits the members that can change without
	// the subnetwork being recreated.
	srv.HandleFunc("PATCH /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project, region, name := sim.PathParam(r, "project"), sim.PathParam(r, "region"), sim.PathParam(r, "name")
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		selfLink := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, name)
		found, err := computeTypedWrite(subnetworks, selfLink, body, false)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid subnetwork: %v", err)
			return
		}
		if !found {
			GCPErrorf(w, 404, "NOT_FOUND", "Subnetwork %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK,
			newComputeOpWithType(project, "regions/"+region, selfLink, "patch"))
	})

	// Private Google Access is a member of the subnetwork, and the verb that
	// turns it on is how a client changes it without rewriting the resource.
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}/setPrivateIpGoogleAccess", func(w http.ResponseWriter, r *http.Request) {
		project, region, name := sim.PathParam(r, "project"), sim.PathParam(r, "region"), sim.PathParam(r, "name")
		var req struct {
			PrivateIpGoogleAccess bool `json:"privateIpGoogleAccess"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		selfLink := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, name)
		if !subnetworks.Update(selfLink, func(s *ComputeSubnetwork) { s.PrivateIpGoogleAccess = req.PrivateIpGoogleAccess }) {
			GCPErrorf(w, 404, "NOT_FOUND", "Subnetwork %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK,
			newComputeOpWithType(project, "regions/"+region, selfLink, "setPrivateIpGoogleAccess"))
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, name)

		if computeNotFound(w, subnetworks.Delete(selfLink), "subnetwork", name) {
			return
		}
		if err := gcpDeleteRealSubnetwork(r.Context(), selfLink); err != nil {
			GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to delete real subnet network fabric: %v", err)
			return
		}
		// Real Compute Engine stamps a delete operation `operationType: "delete"`;
		// gcloud keys off it to report the resource as deleted instead of
		// re-fetching it (which then 404s and fails the command).
		op := newComputeOpWithType(project, "regions/"+region, selfLink, "delete")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Firewalls — `compute#firewall` resource. Real GCP scopes firewall
	// rules to a Network (VPC) and tracks ingress/egress separately.
	// Sockerless workloads provision firewall rules to allow runner
	// traffic between VPCs and the build host; without this surface,
	// terraform's `google_compute_firewall` and runner setup scripts
	// hit a 404 against the sim.
	firewalls := sim.MakeStore[ComputeFirewall](srv.DB(), "compute_firewalls")
	gcpFirewalls = firewalls

	srv.HandleFunc("POST /compute/v1/projects/{project}/global/firewalls", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var fw ComputeFirewall
		if err := sim.ReadJSON(r, &fw); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if fw.Name == "" {
			GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		fw.Kind = "compute#firewall"
		fw.Id = computeNumericID()
		fw.SelfLink = fmt.Sprintf("projects/%s/global/firewalls/%s", project, fw.Name)
		fw.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		if fw.Direction == "" {
			fw.Direction = "INGRESS"
		}
		if fw.Priority == 0 {
			fw.Priority = 1000
		}
		if _, exists := firewalls.Get(fw.SelfLink); computeConflict(w, exists, "firewall", fw.Name) {
			return
		}
		firewalls.Put(fw.SelfLink, fw)
		if err := gcpReapplyRealFirewalls(r.Context()); err != nil {
			GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to apply real firewall filters: %v", err)
			return
		}
		op := newComputeOp(project, "global", fw.SelfLink)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/firewalls/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/global/firewalls/%s", project, name)
		fw, ok := firewalls.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "firewall %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, fw)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/firewalls", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/global/firewalls/", project)
		all := firewalls.Filter(func(f ComputeFirewall) bool {
			return strings.HasPrefix(f.SelfLink, prefix)
		})
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		all = gcpApplyListParams(all, r)
		page, next, ok := paginateListCompute(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "compute#firewallList", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/global/firewalls/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/global/firewalls/%s", project, name)
		if computeNotFound(w, firewalls.Delete(selfLink), "firewall", name) {
			return
		}
		if err := gcpReapplyRealFirewalls(r.Context()); err != nil {
			GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to apply real firewall filters: %v", err)
			return
		}
		// Real Compute Engine stamps a delete operation `operationType: "delete"`;
		// gcloud keys off it to report the resource as deleted instead of
		// re-fetching it (which then 404s and fails the command).
		op := newComputeOpWithType(project, "global", selfLink, "delete")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("PATCH /compute/v1/projects/{project}/global/firewalls/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/global/firewalls/%s", project, name)
		var patch ComputeFirewall
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		ok := firewalls.Update(selfLink, func(fw *ComputeFirewall) {
			if patch.Description != "" {
				fw.Description = patch.Description
			}
			if patch.SourceRanges != nil {
				fw.SourceRanges = patch.SourceRanges
			}
			if patch.SourceTags != nil {
				fw.SourceTags = patch.SourceTags
			}
			if patch.TargetTags != nil {
				fw.TargetTags = patch.TargetTags
			}
			if patch.Allowed != nil {
				fw.Allowed = patch.Allowed
			}
			if patch.Denied != nil {
				fw.Denied = patch.Denied
			}
			if patch.Direction != "" {
				fw.Direction = patch.Direction
			}
			if patch.Priority != 0 {
				fw.Priority = patch.Priority
			}
			fw.Disabled = patch.Disabled
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "firewall %q not found", name)
			return
		}
		if err := gcpReapplyRealFirewalls(r.Context()); err != nil {
			GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to apply real firewall filters: %v", err)
			return
		}
		op := newComputeOp(project, "global", selfLink)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Routers + Cloud NAT — `compute#router` is a regional resource;
	// Cloud NAT configs are embedded in `router.nats[]`. Serverless egress —
	// Cloud Run or Cloud Run Functions reaching the Internet through a VPC
	// connector — is provisioned as a Router carrying a NAT;
	// without these handlers, terraform's `google_compute_router` and
	// `google_compute_router_nat` 404.
	addresses := sim.MakeStore[ComputeAddress](srv.DB(), "compute_addresses")
	gcpComputeRegionAddresses = addresses
	routers := sim.MakeStore[ComputeRouter](srv.DB(), "compute_routers")
	gcpAddresses = addresses
	gcpRouters = routers

	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/addresses", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		var addr ComputeAddress
		if err := sim.ReadJSON(r, &addr); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if addr.Name == "" {
			GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		addr.Kind = "compute#address"
		addr.Id = computeNumericID()
		addr.SelfLink = computeRegionalAddressLink(project, region, addr.Name)
		if _, exists := addresses.Get(addr.SelfLink); computeConflict(w, exists, "address", addr.Name) {
			return
		}
		addr.Region = fmt.Sprintf("projects/%s/regions/%s", project, region)
		addr.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		if addr.AddressType == "" {
			addr.AddressType = "EXTERNAL"
		}
		if addr.IPVersion == "" {
			addr.IPVersion = "IPV4"
		}
		if addr.NetworkTier == "" {
			addr.NetworkTier = "PREMIUM"
		}
		if addr.Status == "" {
			addr.Status = "RESERVED"
		}
		if addr.Address == "" && strings.EqualFold(addr.IPVersion, "IPV4") {
			ip, err := realexec.ReserveGCPPublicIPv4(addr.SelfLink, nil)
			if err != nil {
				GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to reserve real public IPv4 lease: %v", err)
				return
			}
			addr.Address = ip.String()
		}
		if addr.LabelFingerprint == "" {
			addr.LabelFingerprint = computeFingerprint()
		}
		addresses.Put(addr.SelfLink, addr)
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "regions/"+region, addr.SelfLink, "insert"))
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/addresses/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := computeRegionalAddressLink(project, region, name)
		addr, ok := addresses.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "address %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, addr)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/addresses", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		prefix := fmt.Sprintf("projects/%s/regions/%s/addresses/", project, region)
		all := addresses.Filter(func(addr ComputeAddress) bool {
			return strings.HasPrefix(addr.SelfLink, prefix)
		})
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		all = gcpApplyListParams(all, r)
		page, next, ok := paginateListCompute(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "compute#addressList", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/addresses/{name}/setLabels", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := computeRegionalAddressLink(project, region, name)
		var req struct {
			Labels           map[string]string `json:"labels"`
			LabelFingerprint string            `json:"labelFingerprint"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		ok := addresses.Update(selfLink, func(addr *ComputeAddress) {
			addr.Labels = req.Labels
			addr.LabelFingerprint = computeFingerprint()
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "address %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "regions/"+region, selfLink, "setLabels"))
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/regions/{region}/addresses/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := computeRegionalAddressLink(project, region, name)
		addr, ok := addresses.Get(selfLink)
		if computeNotFound(w, ok, "address", name) {
			return
		}
		realexec.ReleasePublicIPv4(net.ParseIP(addr.Address))
		addresses.Delete(selfLink)
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "regions/"+region, selfLink, "delete"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/routers", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		var rt ComputeRouter
		if err := sim.ReadJSON(r, &rt); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if rt.Name == "" {
			GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		if err := validateRouterNATAddresses(project, region, rt.Nats, addresses); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err)
			return
		}
		rt.Kind = "compute#router"
		rt.Id = computeNumericID()
		rt.SelfLink = fmt.Sprintf("projects/%s/regions/%s/routers/%s", project, region, rt.Name)
		rt.Region = fmt.Sprintf("projects/%s/regions/%s", project, region)
		rt.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		if _, exists := routers.Get(rt.SelfLink); computeConflict(w, exists, "router", rt.Name) {
			return
		}
		defaultRouterNATTypes(rt.Nats)
		if len(rt.Nats) > 0 {
			if !gcpRequireNetworkHost(w) {
				return
			}
			if err := gcpConfigureRealRouterNAT(r.Context(), rt); err != nil {
				GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to program real Cloud NAT fabric: %v", err)
				return
			}
		}
		routers.Put(rt.SelfLink, rt)
		op := newComputeOpWithType(project, "regions/"+region, rt.SelfLink, "insert")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/routers/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/regions/%s/routers/%s", project, region, name)
		rt, ok := routers.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "router %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rt)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/routers", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		prefix := fmt.Sprintf("projects/%s/regions/%s/routers/", project, region)
		all := routers.Filter(func(rt ComputeRouter) bool {
			return strings.HasPrefix(rt.SelfLink, prefix)
		})
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		all = gcpApplyListParams(all, r)
		page, next, ok := paginateListCompute(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "compute#routerList", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/regions/{region}/routers/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/regions/%s/routers/%s", project, region, name)
		if computeNotFound(w, routers.Delete(selfLink), "router", name) {
			return
		}
		op := newComputeOpWithType(project, "regions/"+region, selfLink, "delete")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("PATCH /compute/v1/projects/{project}/regions/{region}/routers/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/regions/%s/routers/%s", project, region, name)
		var patch ComputeRouter
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if err := validateRouterNATAddresses(project, region, patch.Nats, addresses); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err)
			return
		}
		ok := routers.Update(selfLink, func(rt *ComputeRouter) {
			if patch.Description != "" {
				rt.Description = patch.Description
			}
			if patch.Network != "" {
				rt.Network = patch.Network
			}
			if patch.Bgp != nil {
				rt.Bgp = patch.Bgp
			}
			if patch.Nats != nil {
				defaultRouterNATTypes(patch.Nats)
				rt.Nats = patch.Nats
			}
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "router %q not found", name)
			return
		}
		if rt, ok := routers.Get(selfLink); ok && len(rt.Nats) > 0 {
			if !gcpRequireNetworkHost(w) {
				return
			}
			if err := gcpConfigureRealRouterNAT(r.Context(), rt); err != nil {
				GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to program real Cloud NAT fabric: %v", err)
				return
			}
		}
		op := newComputeOpWithType(project, "regions/"+region, selfLink, "patch")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/routers/{name}/getRouterStatus", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/regions/%s/routers/%s", project, region, name)
		rt, ok := routers.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "router %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#routerStatusResponse",
			"result": map[string]any{
				"network":    rt.Network,
				"bestRoutes": []map[string]any{},
			},
		})
	})

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

func gcpReapplyRealFirewalls(ctx context.Context) error {
	if gcpInstances == nil {
		return nil
	}
	for _, inst := range gcpInstances.List() {
		for _, ni := range inst.NetworkInterfaces {
			nicID := inst.SelfLink + "/" + ni.Name
			gcpRealMu.Lock()
			nic := gcpRealNICs[nicID]
			tap := gcpRealVMNICs[nicID]
			gcpRealMu.Unlock()
			if nic == nil && tap == nil {
				continue
			}
			rules := gcpIngressPacketRules(inst, ni)
			if nic != nil {
				if err := nic.ConfigureIngressFilter(ctx, rules); err != nil {
					return fmt.Errorf("configure firewall on %s: %w", nicID, err)
				}
			}
			if tap != nil {
				if err := tap.ConfigureIngressFilter(ctx, rules); err != nil {
					return fmt.Errorf("configure firewall on %s: %w", nicID, err)
				}
			}
		}
	}
	return nil
}

func gcpIngressPacketRules(target ComputeInstance, targetNIC ComputeNetworkInterface) []realexec.PacketRule {
	if gcpFirewalls == nil {
		return nil
	}
	firewalls := gcpFirewalls.Filter(func(fw ComputeFirewall) bool {
		if fw.Disabled || !strings.EqualFold(gcpDefaultString(fw.Direction, "INGRESS"), "INGRESS") {
			return false
		}
		return gcpCanonicalComputeRef(fw.Network, gcpProjectFromSelfLink(fw.SelfLink), "global", "networks", "default") == targetNIC.Network
	})
	sort.SliceStable(firewalls, func(i, j int) bool {
		if firewalls[i].Priority == firewalls[j].Priority {
			return firewalls[i].Name < firewalls[j].Name
		}
		return firewalls[i].Priority < firewalls[j].Priority
	})
	var rules []realexec.PacketRule
	for _, fw := range firewalls {
		if !gcpFirewallTargetsInstance(fw, target) {
			continue
		}
		sources := gcpFirewallSources(fw, targetNIC.Network)
		for _, action := range fw.Denied {
			rules = append(rules, gcpPacketRulesForAction(action, sources, "drop")...)
		}
		for _, action := range fw.Allowed {
			rules = append(rules, gcpPacketRulesForAction(action, sources, "accept")...)
		}
	}
	return rules
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
		for _, inst := range gcpInstances.List() {
			if !gcpInstanceHasAnyTag(inst, fw.SourceTags) {
				continue
			}
			for _, ni := range inst.NetworkInterfaces {
				if ni.Network == network && ni.NetworkIP != "" {
					out = append(out, ni.NetworkIP+"/32")
				}
			}
		}
	}
	if len(out) == 0 {
		return []string{"0.0.0.0/0"}
	}
	return out
}

func gcpPacketRulesForAction(action ComputeFirewallAction, sources []string, verdict string) []realexec.PacketRule {
	ports := action.Ports
	if len(ports) == 0 {
		ports = []string{""}
	}
	var rules []realexec.PacketRule
	for _, source := range sources {
		for _, port := range ports {
			from, to := parsePortRange(port)
			rules = append(rules, realexec.PacketRule{
				Protocol:   action.IPProtocol,
				SourceCIDR: source,
				FromPort:   from,
				ToPort:     to,
				Action:     verdict,
			})
		}
	}
	return rules
}

func gcpInstanceHasAnyTag(inst ComputeInstance, tags []string) bool {
	for _, tag := range tags {
		if gcpInstanceHasTag(inst, tag) {
			return true
		}
	}
	return false
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

func parsePortRange(port string) (int, int) {
	if port == "" || port == "*" {
		return 0, 0
	}
	if from, to, ok := strings.Cut(port, "-"); ok {
		start, _ := strconv.Atoi(from)
		end, _ := strconv.Atoi(to)
		return start, end
	}
	value, _ := strconv.Atoi(port)
	return value, value
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

func registerComputeInstanceGroups(srv *sim.Server) {
	groups := sim.MakeStore[storedComputeInstanceGroup](srv.DB(), "compute_instance_groups")
	// Shared so the instance verbs can report which groups refer to an
	// instance, as gcpFirewalls is shared for the same reason.
	gcpComputeInstanceGroups = groups
	gcpInstanceGroups = groups

	instanceGroupSelfLink := func(project, zone, name string) string {
		return fmt.Sprintf("projects/%s/zones/%s/instanceGroups/%s", project, zone, name)
	}

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		var group ComputeInstanceGroup
		if err := sim.ReadJSON(r, &group); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if group.Name == "" {
			GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		group.Kind = "compute#instanceGroup"
		group.Id = computeNumericID()
		group.SelfLink = instanceGroupSelfLink(project, zone, group.Name)
		group.Zone = fmt.Sprintf("projects/%s/zones/%s", project, zone)
		group.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		group.Fingerprint = computeFingerprint()
		groups.Put(group.SelfLink, storedComputeInstanceGroup{ComputeInstanceGroup: group})
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, group.SelfLink, "insert"))
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		group, ok := groups.Get(instanceGroupSelfLink(project, zone, name))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance group %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, wireInstanceGroup(group))
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instanceGroups", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		prefix := fmt.Sprintf("projects/%s/zones/%s/instanceGroups/", project, zone)
		stored := groups.Filter(func(group storedComputeInstanceGroup) bool { return strings.HasPrefix(group.SelfLink, prefix) })
		items := make([]ComputeInstanceGroup, 0, len(stored))
		for _, group := range stored {
			items = append(items, wireInstanceGroup(group))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#instanceGroupList", "items": items})
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceGroupSelfLink(project, zone, name)
		groups.Delete(selfLink)
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "delete"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/addInstances", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceGroupSelfLink(project, zone, name)
		var req struct {
			Instances []ComputeInstanceGroupInstance `json:"instances"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if !groups.Update(selfLink, func(group *storedComputeInstanceGroup) {
			for _, inst := range req.Instances {
				if !gcpInstanceGroupHasInstance(*group, inst.Instance) {
					group.Instances = append(group.Instances, inst)
				}
			}
			group.Fingerprint = computeFingerprint()
		}) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance group %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "addInstances"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/removeInstances", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceGroupSelfLink(project, zone, name)
		var req struct {
			Instances []ComputeInstanceGroupInstance `json:"instances"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		remove := map[string]bool{}
		for _, inst := range req.Instances {
			remove[inst.Instance] = true
		}
		if !groups.Update(selfLink, func(group *storedComputeInstanceGroup) {
			filtered := group.Instances[:0]
			for _, inst := range group.Instances {
				if !remove[inst.Instance] {
					filtered = append(filtered, inst)
				}
			}
			group.Instances = filtered
			group.Fingerprint = computeFingerprint()
		}) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance group %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "removeInstances"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/listInstances", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceGroupSelfLink(project, zone, name)
		group, ok := groups.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance group %q not found", name)
			return
		}
		items := make([]map[string]any, 0, len(group.Instances))
		for _, inst := range group.Instances {
			items = append(items, map[string]any{
				"instance": inst.Instance,
				"status":   "RUNNING",
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":  "compute#instanceGroupsListInstances",
			"id":    computeNumericID(),
			"items": items,
		})
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/setNamedPorts", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceGroupSelfLink(project, zone, name)
		var req struct {
			Fingerprint string                          `json:"fingerprint"`
			NamedPorts  []ComputeInstanceGroupNamedPort `json:"namedPorts"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		var conflict bool
		if !groups.Update(selfLink, func(group *storedComputeInstanceGroup) {
			// setNamedPorts honors the fingerprint for optimistic concurrency
			// (a stale read-modify-write must 412).
			if !fingerprintMatches(group.Fingerprint, req.Fingerprint) {
				conflict = true
				return
			}
			group.NamedPorts = req.NamedPorts
			group.Fingerprint = computeFingerprint()
		}) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance group %q not found", name)
			return
		}
		if conflict {
			GCPErrorf(w, http.StatusPreconditionFailed, "conditionNotMet", "named ports fingerprint mismatch; the resource was modified concurrently")
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "setNamedPorts"))
	})
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
		if err := gcpCreateRealNetwork(ctx, networkLink); err != nil {
			return "", err
		}
		networks.Put(networkLink, net)
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
	if err := gcpCreateRealSubnetwork(ctx, subnet); err != nil {
		return "", err
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
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":        "compute#machineType",
			"id":          computeNumericID(),
			"name":        name,
			"description": name,
			"guestCpus":   2,
			"memoryMb":    1024,
			"zone":        fmt.Sprintf("projects/%s/zones/%s", project, zone),
			"selfLink":    fmt.Sprintf("projects/%s/zones/%s/machineTypes/%s", project, zone, name),
		})
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/machineTypes", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		items := []map[string]any{}
		for _, name := range []string{"e2-micro", "e2-small", "n1-standard-1"} {
			items = append(items, map[string]any{
				"kind":      "compute#machineType",
				"id":        computeNumericID(),
				"name":      name,
				"guestCpus": 2,
				"memoryMb":  1024,
				"zone":      fmt.Sprintf("projects/%s/zones/%s", project, zone),
				"selfLink":  fmt.Sprintf("projects/%s/zones/%s/machineTypes/%s", project, zone, name),
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#machineTypeList", "items": items})
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
	imageJSON := computeImageJSON
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/images/{image}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "image")
		if gcpComputeImages != nil {
			if m, ok := gcpComputeImages.Get(fmt.Sprintf("projects/%s/global/images/%s", project, name)); ok {
				sim.WriteJSON(w, http.StatusOK, m)
				return
			}
		}
		sim.WriteJSON(w, http.StatusOK, imageJSON(project, name))
	})
	// An image's family lookup and its IAM policy read are the same shape to a
	// path router — "images/family/{family}" against
	// "images/{resource}/getIamPolicy" — so one handler serves both. Compute
	// Engine resolves the overlap by its templates' literal segments, and the
	// family segment comes first, so a request for the family named
	// "getIamPolicy" is a family lookup.
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/images/{first}/{second}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		first, second := sim.PathParam(r, "first"), sim.PathParam(r, "second")
		if first == "family" {
			name := second
			if !strings.HasSuffix(name, "-12") {
				name += "-12"
			}
			sim.WriteJSON(w, http.StatusOK, imageJSON(project, name))
			return
		}
		if second != "getIamPolicy" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no such image method %q", second)
			return
		}
		handleResourceIAM(w, r, gcpResourcePolicies,
			"compute/"+computeGlobalLink(project, "images", first), "getIamPolicy")
	})
	for verb, method := range map[string]string{"setIamPolicy": "POST", "testIamPermissions": "POST"} {
		verb := verb
		srv.HandleFunc(method+" /compute/v1/projects/{project}/global/images/{resource}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			handleResourceIAM(w, r, gcpResourcePolicies,
				"compute/"+computeGlobalLink(sim.PathParam(r, "project"), "images", sim.PathParam(r, "resource")), verb)
		})
	}
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

// recoverComputeInstances transitions persisted instances that claim to be
// running, or that a restart caught mid-boot, to TERMINATED when their real
// backing — the Firecracker VM, its tap NIC, and the metadata IP mapping — did
// not survive the control-plane restart. Real Compute Engine reports an
// instance whose VM is gone as TERMINATED with a human-readable statusMessage;
// the sim does the same and never re-adopts a lost VM.
func recoverComputeInstances(instances sim.Store[ComputeInstance]) {
	for _, inst := range instances.List() {
		if inst.Status != ComputeInstanceRunning && !computeInstancePreRunning(inst.Status) {
			continue
		}
		if gcpRealVMAlive(inst.SelfLink) {
			continue
		}
		instances.Update(inst.SelfLink, func(in *ComputeInstance) {
			if in.Status != ComputeInstanceRunning && !computeInstancePreRunning(in.Status) {
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

func registerComputeInstances(srv *sim.Server, networks sim.Store[ComputeNetwork], subnetworks sim.Store[ComputeSubnetwork]) {
	instances := sim.MakeStore[ComputeInstance](srv.DB(), "compute_instances")
	registerComputeInstanceVerbs(srv, instances)
	gcpInstances = instances
	recoverComputeInstances(instances)
	recoverComputeOperations()
	logger := srv.Logger()

	// One spelling of the instance key, shared with the verbs registered
	// alongside these handlers: two spellings would read past each other.
	instanceSelfLink := computeInstanceSelfLink

	normalizeInstance := func(ctx context.Context, project, zone string, inst *ComputeInstance) error {
		inst.Kind = "compute#instance"
		if inst.Id == "" {
			inst.Id = computeNumericID()
		}
		inst.SelfLink = instanceSelfLink(project, zone, inst.Name)
		inst.Zone = fmt.Sprintf("projects/%s/zones/%s", project, zone)
		if inst.CreationTimestamp == "" {
			inst.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		}
		if inst.MachineType == "" {
			inst.MachineType = fmt.Sprintf("projects/%s/zones/%s/machineTypes/e2-micro", project, zone)
		} else if !strings.Contains(inst.MachineType, "/") {
			inst.MachineType = fmt.Sprintf("projects/%s/zones/%s/machineTypes/%s", project, zone, inst.MachineType)
		}
		inst.Status = ComputeInstanceRunning
		if inst.LabelFingerprint == "" {
			inst.LabelFingerprint = generateUUID()[:8]
		}
		if inst.Tags == nil {
			inst.Tags = &ComputeInstanceTags{}
		}
		if inst.Tags.Fingerprint == "" {
			inst.Tags.Fingerprint = generateUUID()[:8]
		}
		if inst.Metadata == nil {
			inst.Metadata = &ComputeInstanceMetadata{}
		}
		inst.Metadata.Kind = "compute#metadata"
		if inst.Metadata.Fingerprint == "" {
			inst.Metadata.Fingerprint = generateUUID()[:8]
		}
		for i := range inst.Disks {
			if inst.Disks[i].Kind == "" {
				inst.Disks[i].Kind = "compute#attachedDisk"
			}
			if inst.Disks[i].Mode == "" {
				inst.Disks[i].Mode = "READ_WRITE"
			}
			if inst.Disks[i].Type == "" {
				inst.Disks[i].Type = "PERSISTENT"
			}
			if inst.Disks[i].Interface == "" {
				inst.Disks[i].Interface = "SCSI"
			}
			if inst.Disks[i].DeviceName == "" {
				inst.Disks[i].DeviceName = inst.Name
			}
			if inst.Disks[i].Source == "" {
				inst.Disks[i].Source = fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, inst.Disks[i].DeviceName)
			}
			inst.Disks[i].Index = int64(i)
		}
		if len(inst.Disks) == 0 {
			inst.Disks = []ComputeInstanceDisk{{
				Kind:       "compute#attachedDisk",
				Type:       "PERSISTENT",
				Mode:       "READ_WRITE",
				Source:     fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, inst.Name),
				DeviceName: inst.Name,
				Index:      0,
				Boot:       true,
				AutoDelete: true,
				Interface:  "SCSI",
			}}
		}
		for i := range inst.NetworkInterfaces {
			if inst.NetworkInterfaces[i].Kind == "" {
				inst.NetworkInterfaces[i].Kind = "compute#networkInterface"
			}
			if inst.NetworkInterfaces[i].Name == "" {
				inst.NetworkInterfaces[i].Name = fmt.Sprintf("nic%d", i)
			}
			if inst.NetworkInterfaces[i].Network == "" {
				inst.NetworkInterfaces[i].Network = fmt.Sprintf("projects/%s/global/networks/default", project)
			} else {
				inst.NetworkInterfaces[i].Network = normalizeComputeGlobalNetworkRef(project, inst.NetworkInterfaces[i].Network)
			}
			if inst.NetworkInterfaces[i].Subnetwork == "" {
				subnetLink, err := ensureGCPAutoModeSubnetwork(ctx, networks, subnetworks, project, regionFromZone(zone), inst.NetworkInterfaces[i].Network)
				if err != nil {
					return err
				}
				inst.NetworkInterfaces[i].Subnetwork = subnetLink
			} else {
				inst.NetworkInterfaces[i].Subnetwork = normalizeComputeSubnetworkRef(project, regionFromZone(zone), inst.NetworkInterfaces[i].Subnetwork)
			}
			if inst.NetworkInterfaces[i].Subnetwork == "" {
				return fmt.Errorf("network interface %s requires a subnetwork for real IP allocation", inst.NetworkInterfaces[i].Name)
			}
			if inst.NetworkInterfaces[i].StackType == "" {
				inst.NetworkInterfaces[i].StackType = "IPV4_ONLY"
			}
			if inst.NetworkInterfaces[i].Fingerprint == "" {
				inst.NetworkInterfaces[i].Fingerprint = generateUUID()[:8]
			}
			for j := range inst.NetworkInterfaces[i].AccessConfigs {
				if inst.NetworkInterfaces[i].AccessConfigs[j].Kind == "" {
					inst.NetworkInterfaces[i].AccessConfigs[j].Kind = "compute#accessConfig"
				}
				if inst.NetworkInterfaces[i].AccessConfigs[j].Name == "" {
					inst.NetworkInterfaces[i].AccessConfigs[j].Name = "External NAT"
				}
				if inst.NetworkInterfaces[i].AccessConfigs[j].Type == "" {
					inst.NetworkInterfaces[i].AccessConfigs[j].Type = "ONE_TO_ONE_NAT"
				}
				if inst.NetworkInterfaces[i].AccessConfigs[j].NetworkTier == "" {
					inst.NetworkInterfaces[i].AccessConfigs[j].NetworkTier = "PREMIUM"
				}
			}
		}
		if len(inst.NetworkInterfaces) == 0 {
			return fmt.Errorf("compute instance requires a network interface with a subnetwork for real IP allocation")
		}
		return nil
	}
	gcpNormalizeInstance = normalizeInstance

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		var inst ComputeInstance
		if err := sim.ReadJSON(r, &inst); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if inst.Name == "" {
			GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		if _, exists := instances.Get(instanceSelfLink(project, zone, inst.Name)); computeConflict(w, exists, "instance", inst.Name) {
			return
		}
		if !gcpRequireNetworkHost(w) {
			return
		}
		if err := normalizeInstance(r.Context(), project, zone, &inst); err != nil {
			GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to attach real instance network interface: %v", err)
			return
		}

		// Compute Engine does not boot the instance inside the insert request.
		// It records the instance as PROVISIONING, answers with a RUNNING zone
		// operation, and brings the virtual machine up behind it; the client
		// polls zoneOperations.get or zoneOperations.wait for the verdict. The
		// boot therefore runs on a context of its own — a client that stops
		// waiting must not take the machine down with it, and the request's
		// context dies with the response.
		inst.Status = ComputeInstanceProvisioning
		instances.Put(inst.SelfLink, inst)

		op := newComputeOpRecord(project, "zones/"+zone, inst.SelfLink, "insert")
		recordComputeOp(op)

		booting := inst
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), computeInstanceBootBudget)
			defer cancel()
			err := gcpStartRealVM(ctx, &booting)
			if err == nil {
				err = gcpReapplyRealFirewalls(ctx)
			}
			if err != nil {
				logger.Error().
					Err(err).
					Str("project", project).
					Str("zone", zone).
					Str("instance", booting.Name).
					Msg("failed to boot real Compute Engine instance")
				// An insert whose machine never came up leaves no instance
				// behind, and the verdict lives on the operation.
				instances.Delete(booting.SelfLink)
				computeOpFinish(op.Name, err)
				return
			}
			booting.Status = ComputeInstanceRunning
			instances.Put(booting.SelfLink, booting)
			computeOpFinish(op.Name, nil)
		}()

		sim.WriteJSON(w, http.StatusOK, computeOpJSON(op))
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instances/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceSelfLink(project, zone, name)
		inst, ok := instances.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found in zone %q", name, zone)
			return
		}
		if inst.Status == ComputeInstanceRunning && !gcpRealVMAlive(inst.SelfLink) {
			inst.Status = ComputeInstanceTerminated
			instances.Put(selfLink, inst)
		}
		sim.WriteJSON(w, http.StatusOK, inst)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instances", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		prefix := fmt.Sprintf("projects/%s/zones/%s/instances/", project, zone)
		all := instances.Filter(func(inst ComputeInstance) bool {
			return strings.HasPrefix(inst.SelfLink, prefix)
		})
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		all = gcpApplyListParams(all, r)
		page, next, ok := paginateListCompute(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "compute#instanceList", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/instances", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/zones/", project)
		all := instances.Filter(func(inst ComputeInstance) bool {
			return strings.HasPrefix(inst.SelfLink, prefix)
		})
		grouped := map[string]map[string]any{}
		for _, inst := range all {
			rest := strings.TrimPrefix(inst.SelfLink, prefix)
			zone, _, ok := strings.Cut(rest, "/")
			if !ok {
				continue
			}
			key := "zones/" + zone
			entry, exists := grouped[key]
			if !exists {
				entry = map[string]any{"instances": []ComputeInstance{}}
				grouped[key] = entry
			}
			list, ok := entry["instances"].([]ComputeInstance)
			if !ok {
				list = []ComputeInstance{}
			}
			entry["instances"] = append(list, inst)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#instanceAggregatedList", "items": grouped})
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/zones/{zone}/instances/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceSelfLink(project, zone, name)
		inst, ok := instances.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found in zone %q", name, zone)
			return
		}
		_ = gcpDeleteRealVM(r.Context(), inst)
		instances.Delete(selfLink)
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "delete"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/stop", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceSelfLink(project, zone, name)
		if err := gcpStopRealVM(r.Context(), selfLink); err != nil {
			GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to stop real Compute Engine instance: %v", err)
			return
		}
		if ok := instances.Update(selfLink, func(inst *ComputeInstance) { inst.Status = ComputeInstanceTerminated }); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found in zone %q", name, zone)
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "stop"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/start", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceSelfLink(project, zone, name)
		inst, ok := instances.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found in zone %q", name, zone)
			return
		}
		if err := gcpStartRealVM(r.Context(), &inst); err != nil {
			logger.Error().
				Err(err).
				Str("project", project).
				Str("zone", zone).
				Str("instance", inst.Name).
				Msg("failed to start real Compute Engine instance")
			GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to start real Compute Engine instance: %v", err)
			return
		}
		inst.Status = ComputeInstanceRunning
		instances.Put(selfLink, inst)
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "start"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setLabels", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceSelfLink(project, zone, name)
		var req struct {
			Labels           map[string]string `json:"labels"`
			LabelFingerprint string            `json:"labelFingerprint"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		var conflict bool
		ok := instances.Update(selfLink, func(inst *ComputeInstance) {
			if !fingerprintMatches(inst.LabelFingerprint, req.LabelFingerprint) {
				conflict = true
				return
			}
			inst.Labels = req.Labels
			inst.LabelFingerprint = computeFingerprint()
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found in zone %q", name, zone)
			return
		}
		if conflict {
			GCPErrorf(w, http.StatusPreconditionFailed, "conditionNotMet", "labelFingerprint mismatch; the resource was modified concurrently")
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "setLabels"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setTags", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceSelfLink(project, zone, name)
		var req ComputeInstanceTags
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		var conflict bool
		ok := instances.Update(selfLink, func(inst *ComputeInstance) {
			current := ""
			if inst.Tags != nil {
				current = inst.Tags.Fingerprint
			}
			if !fingerprintMatches(current, req.Fingerprint) {
				conflict = true
				return
			}
			inst.Tags = &req
			inst.Tags.Fingerprint = computeFingerprint()
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found in zone %q", name, zone)
			return
		}
		if conflict {
			GCPErrorf(w, http.StatusPreconditionFailed, "conditionNotMet", "tags fingerprint mismatch; the resource was modified concurrently")
			return
		}
		if err := gcpReapplyRealFirewalls(r.Context()); err != nil {
			GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to apply real firewall filters: %v", err)
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "setTags"))
	})
}

// registerComputeDisks wires the zonal Compute Disks REST surface. Real
// GCP exposes Disks via `compute#disk` at
// `/compute/v1/projects/{p}/zones/{z}/disks` plus an aggregated list
// across zones at `/compute/v1/projects/{p}/aggregated/disks`. The
// sim mirrors create / get / list / delete / resize / setLabels +
// aggregated-list, all returning zonal operations the SDK polls.
func registerComputeDisks(srv *sim.Server) {
	disks := sim.MakeStore[ComputeDisk](srv.DB(), "compute_disks")
	// Shared so the disk verbs write the same disks the lifecycle serves.
	gcpComputeZoneDisks = disks

	// Insert (create disk) — POST .../zones/{zone}/disks
	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/disks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		var d ComputeDisk
		if err := sim.ReadJSON(r, &d); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if d.Name == "" {
			GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		d.Kind = "compute#disk"
		d.Id = computeNumericID()
		d.SelfLink = fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, d.Name)
		d.Zone = fmt.Sprintf("projects/%s/zones/%s", project, zone)
		d.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		d.Status = "READY"
		if d.SizeGb == "" {
			d.SizeGb = "10"
		}
		if d.Type == "" {
			d.Type = fmt.Sprintf("projects/%s/zones/%s/diskTypes/pd-standard", project, zone)
		}
		d.LabelFingerprint = generateUUID()[:8]
		disks.Put(d.SelfLink, d)
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, d.SelfLink, "insert"))
	})

	// Get
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/disks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, name)
		d, ok := disks.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "disk %q not found in zone %q", name, zone)
			return
		}
		sim.WriteJSON(w, http.StatusOK, d)
	})

	// List (zonal)
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/disks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		prefix := fmt.Sprintf("projects/%s/zones/%s/disks/", project, zone)
		items := disks.Filter(func(d ComputeDisk) bool {
			return strings.HasPrefix(d.SelfLink, prefix)
		})
		if items == nil {
			items = []ComputeDisk{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":  "compute#diskList",
			"items": items,
		})
	})

	// Delete
	srv.HandleFunc("DELETE /compute/v1/projects/{project}/zones/{zone}/disks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, name)
		if _, ok := disks.Get(selfLink); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "disk %q not found in zone %q", name, zone)
			return
		}
		disks.Delete(selfLink)
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "delete"))
	})

	// Resize — POST .../disks/{name}/resize with body {sizeGb}
	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/disks/{name}/resize", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, name)
		var req struct {
			SizeGb gcpInt64 `json:"sizeGb"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		ok := disks.Update(selfLink, func(d *ComputeDisk) {
			if req.SizeGb != "" {
				d.SizeGb = req.SizeGb
			}
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "disk %q not found in zone %q", name, zone)
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "resize"))
	})

	// SetLabels — POST .../disks/{name}/setLabels with body {labels, labelFingerprint}
	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/disks/{name}/setLabels", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, name)
		var req struct {
			Labels           map[string]string `json:"labels"`
			LabelFingerprint string            `json:"labelFingerprint"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		var conflict bool
		ok := disks.Update(selfLink, func(d *ComputeDisk) {
			if !fingerprintMatches(d.LabelFingerprint, req.LabelFingerprint) {
				conflict = true
				return
			}
			d.Labels = req.Labels
			d.LabelFingerprint = computeFingerprint()
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "disk %q not found in zone %q", name, zone)
			return
		}
		if conflict {
			GCPErrorf(w, http.StatusPreconditionFailed, "conditionNotMet", "labelFingerprint mismatch; the resource was modified concurrently")
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "setLabels"))
	})

	// Aggregated list — GET /compute/v1/projects/{p}/aggregated/disks.
	// Real GCP returns map[zone-key]{disks:[…]}; sim groups by zone.
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/disks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/zones/", project)
		all := disks.Filter(func(d ComputeDisk) bool {
			return strings.HasPrefix(d.SelfLink, prefix)
		})
		grouped := map[string]map[string]any{}
		for _, d := range all {
			rest := strings.TrimPrefix(d.SelfLink, prefix)
			zone, _, ok := strings.Cut(rest, "/")
			if !ok {
				continue
			}
			key := "zones/" + zone
			entry, exists := grouped[key]
			if !exists {
				entry = map[string]any{"disks": []ComputeDisk{}}
				grouped[key] = entry
			}
			list, ok := entry["disks"].([]ComputeDisk)
			if !ok {
				list = []ComputeDisk{}
			}
			entry["disks"] = append(list, d)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":  "compute#diskAggregatedList",
			"items": grouped,
		})
	})

	// Zonal operations endpoint. An instance insert leaves its operation
	// RUNNING while the virtual machine boots behind it, so both methods
	// report the operation's real state: the GET answers immediately with
	// whatever it is, and the wait blocks for it the way Compute Engine's does.
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/operations/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeWriteOperation(w, sim.PathParam(r, "name"))
	})
	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/operations/{name}/wait", func(w http.ResponseWriter, r *http.Request) {
		computeWaitOperation(w, r, sim.PathParam(r, "name"))
	})
}

// computeImageJSON renders the public-image catalogue entry for a name. Both
// the image read and the family view answer from it, so a family and the image
// it resolves to cannot describe the same image differently.
func computeImageJSON(project, name string) map[string]any {
	return map[string]any{
		"kind":              "compute#image",
		"id":                computeNumericID(),
		"name":              name,
		"selfLink":          fmt.Sprintf("projects/%s/global/images/%s", project, name),
		"status":            "READY",
		"family":            strings.TrimSuffix(name, "-12"),
		"archiveSizeBytes":  "1073741824",
		"diskSizeGb":        "10",
		"sourceType":        "RAW",
		"creationTimestamp": time.Now().UTC().Format(time.RFC3339),
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
