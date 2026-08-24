package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Amazon VPC IP Address Manager internet-registry associations and routing
// policy registrations.
//
// An internet registry association names a Regional Internet Registry — ARIN,
// RIPE or APNIC — which is a third-party organisation outside AWS. The
// association itself is control-plane state and is stored, described and
// deleted for real. Enabling one begins verification WITH the registry, and
// there is no registry to reach from this simulator: that path fails loudly,
// naming the registry as the missing external dependency, rather than
// pretending a verification the RIR never performed.
//
// Routing policy registrations are the caller's own declarations — a CIDR,
// the ASNs allowed to originate it — and are stored, listed, modified and
// deleted for real. The reads that would report the registry's side — deltas
// against RIR state, route origin authorizations from the RPKI repositories —
// return the truth of an installation whose associations were never enabled:
// nothing has been imported, so the collections are empty. Discovered routes
// are different: they come from the account's own route tables, which this
// simulator holds, so they are derived from that real state.

type EC2IpamInternetRegistryAssociation struct {
	OwnerId                            string   `json:"ownerId"`
	IpamInternetRegistryAssociationId  string   `json:"ipamInternetRegistryAssociationId"`
	IpamInternetRegistryAssociationArn string   `json:"ipamInternetRegistryAssociationArn"`
	IpamId                             string   `json:"ipamId"`
	IpamRegion                         string   `json:"ipamRegion"`
	Rir                                string   `json:"rir"`
	OrganizationHandle                 string   `json:"organizationHandle"`
	Description                        string   `json:"description,omitempty"`
	State                              string   `json:"state"`
	Tags                               []EC2Tag `json:"tags,omitempty"`
}

type EC2IpamRoutingPolicyRegistration struct {
	AssociationId                   string   `json:"associationId"`
	Cidr                            string   `json:"cidr"`
	Asns                            []string `json:"asns"`
	PermitMoreSpecificAnnouncements bool     `json:"permitMoreSpecificAnnouncements"`
	MaxLength                       int      `json:"maxLength,omitempty"`
	Description                     string   `json:"description,omitempty"`
	LatestDeltaId                   string   `json:"latestDeltaId,omitempty"`
	State                           string   `json:"state"`
}

// EC2IpamRoutingPolicyRegistrationDelta is one batch modification, as the
// caller submitted it: the deltaJson is the caller's own document, echoed
// back, and the state records whether it applied.
type EC2IpamRoutingPolicyRegistrationDelta struct {
	AssociationId string `json:"associationId"`
	DeltaId       string `json:"deltaId"`
	DeltaJson     string `json:"deltaJson"`
	State         string `json:"state"`
	StateMessage  string `json:"stateMessage,omitempty"`
}

var (
	ec2IpamRegistryAssociations sim.Store[EC2IpamInternetRegistryAssociation]
	ec2IpamRoutingRegistrations sim.Store[EC2IpamRoutingPolicyRegistration]
	ec2IpamRoutingDeltas        sim.Store[EC2IpamRoutingPolicyRegistrationDelta]
)

func ec2IpamRoutingRegistrationKey(associationID, cidr string) string {
	return associationID + "/" + cidr
}

// ipamRegistryUnreachable is why enabling an association cannot proceed here.
const ipamRegistryUnreachable = "enabling an internet registry association begins verification with " +
	"the Regional Internet Registry the association names (ARIN, RIPE or APNIC), which is a " +
	"third-party organisation outside AWS. This simulator has no connection to a registry and will " +
	"not fabricate a verification the registry never performed. The association itself, and the " +
	"routing policy registrations under it, are real state: create, describe, modify and delete " +
	"all behave; only the exchange with the registry is impossible."

func registerEC2IpamRegistry(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2IpamRegistryAssociations = sim.MakeStore[EC2IpamInternetRegistryAssociation](srv.DB(), "ec2_ipam_registry_associations")
	ec2IpamRoutingRegistrations = sim.MakeStore[EC2IpamRoutingPolicyRegistration](srv.DB(), "ec2_ipam_routing_registrations")
	ec2IpamRoutingDeltas = sim.MakeStore[EC2IpamRoutingPolicyRegistrationDelta](srv.DB(), "ec2_ipam_routing_deltas")

	r.Register("CreateIpamInternetRegistryAssociation", handleCreateIpamInternetRegistryAssociation)
	r.Register("DescribeIpamInternetRegistryAssociations", handleDescribeIpamInternetRegistryAssociations)
	r.Register("DeleteIpamInternetRegistryAssociation", handleDeleteIpamInternetRegistryAssociation)
	r.Register("EnableIpamInternetRegistryAssociation", handleEnableIpamInternetRegistryAssociation)
	r.Register("GetIpamInternetRegistryAssociationAsns", handleGetIpamInternetRegistryAssociationAsns)
	r.Register("GetIpamInternetRegistryAssociationCidrs", handleGetIpamInternetRegistryAssociationCidrs)

	r.Register("CreateIpamRoutingPolicyRegistration", handleCreateIpamRoutingPolicyRegistration)
	r.Register("ModifyIpamRoutingPolicyRegistration", handleModifyIpamRoutingPolicyRegistration)
	r.Register("BatchModifyIpamRoutingPolicyRegistrations", handleBatchModifyIpamRoutingPolicyRegistrations)
	r.Register("DeleteIpamRoutingPolicyRegistration", handleDeleteIpamRoutingPolicyRegistration)
	r.Register("GetIpamRoutingPolicyRegistrations", handleGetIpamRoutingPolicyRegistrations)
	r.Register("GetIpamRoutingPolicyRegistrationDeltas", handleGetIpamRoutingPolicyRegistrationDeltas)

	r.Register("GetIpamDiscoveredRoutes", handleGetIpamDiscoveredRoutes)
	r.Register("GetIpamRouteOriginAuthorizations", handleGetIpamRouteOriginAuthorizations)
	r.Register("GetIpamRouteProtectionFindings", handleGetIpamRouteProtectionFindings)
}

func ipamRegistryAssociationXML(a EC2IpamInternetRegistryAssociation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<ownerId>%s</ownerId><ipamInternetRegistryAssociationId>%s</ipamInternetRegistryAssociationId>",
		a.OwnerId, a.IpamInternetRegistryAssociationId)
	fmt.Fprintf(&b, "<ipamInternetRegistryAssociationArn>%s</ipamInternetRegistryAssociationArn>", a.IpamInternetRegistryAssociationArn)
	fmt.Fprintf(&b, "<ipamId>%s</ipamId><ipamRegion>%s</ipamRegion><rir>%s</rir>", a.IpamId, a.IpamRegion, a.Rir)
	fmt.Fprintf(&b, "<organizationHandle>%s</organizationHandle><state>%s</state>", xmlEscape(a.OrganizationHandle), a.State)
	if a.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(a.Description))
	}
	b.WriteString(writeTagSetXML(a.Tags))
	return b.String()
}

func handleCreateIpamInternetRegistryAssociation(w http.ResponseWriter, r *http.Request) {
	ipamID := r.FormValue("IpamId")
	if _, ok := ec2Ipams.Get(ipamID); !ok {
		ec2ErrorXML(w, "InvalidIpamId.NotFound", fmt.Sprintf("The ipam ID '%s' does not exist", ipamID), http.StatusBadRequest)
		return
	}
	rir := r.FormValue("Rir")
	if rir == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter Rir", http.StatusBadRequest)
		return
	}
	id := ec2ID("ipam-irr-assoc")
	association := EC2IpamInternetRegistryAssociation{
		OwnerId:                            ec2Owner(),
		IpamInternetRegistryAssociationId:  id,
		IpamInternetRegistryAssociationArn: ipamArn("ipam-internet-registry-association/" + id),
		IpamId:                             ipamID,
		IpamRegion:                         awsRegion(),
		Rir:                                rir,
		OrganizationHandle:                 r.FormValue("OrganizationHandle"),
		Description:                        r.FormValue("Description"),
		// Created, and never verified: verification is the registry's act, and
		// there is no registry here. Enable says so loudly.
		State: "pending-verification",
		Tags:  parseTags(r),
	}
	ec2IpamRegistryAssociations.Put(id, association)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateIpamInternetRegistryAssociationResponse %s>
  <requestId>%s</requestId>
  <ipamInternetRegistryAssociation>%s</ipamInternetRegistryAssociation>
</CreateIpamInternetRegistryAssociationResponse>`, ec2Xmlns(), generateUUID(), ipamRegistryAssociationXML(association))
}

func handleDescribeIpamInternetRegistryAssociations(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "IpamInternetRegistryAssociationId")
	var items strings.Builder
	for _, association := range ec2IpamRegistryAssociations.List() {
		if len(ids) > 0 && !ec2StrInValues(association.IpamInternetRegistryAssociationId, ids) {
			continue
		}
		fmt.Fprintf(&items, "<item>%s</item>", ipamRegistryAssociationXML(association))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIpamInternetRegistryAssociationsResponse %s>
  <requestId>%s</requestId>
  <ipamInternetRegistryAssociationSet>%s</ipamInternetRegistryAssociationSet>
</DescribeIpamInternetRegistryAssociationsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleDeleteIpamInternetRegistryAssociation(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamInternetRegistryAssociationId")
	association, ok := ec2IpamRegistryAssociations.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamInternetRegistryAssociationId.NotFound",
			fmt.Sprintf("The association ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// A deleted association takes its registrations with it: they were scoped
	// to it and answer through it.
	for _, registration := range ec2IpamRoutingRegistrations.List() {
		if registration.AssociationId == id {
			ec2IpamRoutingRegistrations.Delete(ec2IpamRoutingRegistrationKey(id, registration.Cidr))
		}
	}
	association.State = "delete-complete"
	ec2IpamRegistryAssociations.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteIpamInternetRegistryAssociationResponse %s>
  <requestId>%s</requestId>
  <ipamInternetRegistryAssociation>%s</ipamInternetRegistryAssociation>
</DeleteIpamInternetRegistryAssociationResponse>`, ec2Xmlns(), generateUUID(), ipamRegistryAssociationXML(association))
}

func handleEnableIpamInternetRegistryAssociation(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamInternetRegistryAssociationId")
	if _, ok := ec2IpamRegistryAssociations.Get(id); !ok {
		ec2ErrorXML(w, "InvalidIpamInternetRegistryAssociationId.NotFound",
			fmt.Sprintf("The association ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2ErrorXML(w, "OperationNotPermitted", ipamRegistryUnreachable, http.StatusBadRequest)
}

// The two imports read what the registry sent back. No association here has
// ever been enabled — enabling is the loud failure above — so nothing has been
// imported, and an empty set is this installation's truth rather than a
// placeholder.
func handleGetIpamInternetRegistryAssociationAsns(w http.ResponseWriter, r *http.Request) {
	ec2RequireIpamRegistryAssociation(w, r, "GetIpamInternetRegistryAssociationAsns", "ipamInternetRegistryAssociationAsnSet")
}

func handleGetIpamInternetRegistryAssociationCidrs(w http.ResponseWriter, r *http.Request) {
	ec2RequireIpamRegistryAssociation(w, r, "GetIpamInternetRegistryAssociationCidrs", "ipamInternetRegistryAssociationCidrSet")
}

func ec2RequireIpamRegistryAssociation(w http.ResponseWriter, r *http.Request, action, setName string) {
	id := r.FormValue("IpamInternetRegistryAssociationId")
	if _, ok := ec2IpamRegistryAssociations.Get(id); !ok {
		ec2ErrorXML(w, "InvalidIpamInternetRegistryAssociationId.NotFound",
			fmt.Sprintf("The association ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s>
  <requestId>%s</requestId>
  <%s></%s>
</%sResponse>`, action, ec2Xmlns(), generateUUID(), setName, setName, action)
}

// ---- routing policy registrations ----

func ipamRoutingRegistrationXML(registration EC2IpamRoutingPolicyRegistration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<cidr>%s</cidr><state>%s</state>", registration.Cidr, registration.State)
	if len(registration.Asns) > 0 {
		b.WriteString("<asnSet>")
		for _, asn := range registration.Asns {
			fmt.Fprintf(&b, "<item>%s</item>", xmlEscape(asn))
		}
		b.WriteString("</asnSet>")
	}
	fmt.Fprintf(&b, "<permitMoreSpecificAnnouncements>%t</permitMoreSpecificAnnouncements>", registration.PermitMoreSpecificAnnouncements)
	if registration.MaxLength > 0 {
		fmt.Fprintf(&b, "<maxLength>%d</maxLength>", registration.MaxLength)
	}
	if registration.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(registration.Description))
	}
	if registration.LatestDeltaId != "" {
		fmt.Fprintf(&b, "<latestDeltaId>%s</latestDeltaId>", registration.LatestDeltaId)
	}
	return b.String()
}

func handleCreateIpamRoutingPolicyRegistration(w http.ResponseWriter, r *http.Request) {
	associationID := r.FormValue("IpamInternetRegistryAssociationId")
	if _, ok := ec2IpamRegistryAssociations.Get(associationID); !ok {
		ec2ErrorXML(w, "InvalidIpamInternetRegistryAssociationId.NotFound",
			fmt.Sprintf("The association ID '%s' does not exist", associationID), http.StatusBadRequest)
		return
	}
	cidr := r.FormValue("Cidr")
	if cidr == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter Cidr", http.StatusBadRequest)
		return
	}
	registration := EC2IpamRoutingPolicyRegistration{
		AssociationId:                   associationID,
		Cidr:                            cidr,
		Asns:                            ec2ParamList(r, "Asn"),
		PermitMoreSpecificAnnouncements: r.FormValue("PermitMoreSpecificAnnouncements") == "true",
		MaxLength:                       atoiDefault(r.FormValue("MaxLength"), 0),
		Description:                     r.FormValue("Description"),
		State:                           "registered",
	}
	delta := ec2RecordRoutingRegistrationDelta(associationID, map[string]any{
		"Cidr": cidr,
		"Asns": registration.Asns,
	})
	registration.LatestDeltaId = delta.DeltaId
	ec2IpamRoutingRegistrations.Put(ec2IpamRoutingRegistrationKey(associationID, cidr), registration)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateIpamRoutingPolicyRegistrationResponse %s>
  <requestId>%s</requestId>
  <ipamRoutingPolicyRegistrationDelta>%s</ipamRoutingPolicyRegistrationDelta>
</CreateIpamRoutingPolicyRegistrationResponse>`, ec2Xmlns(), generateUUID(), ipamRoutingDeltaXML(delta))
}

// ec2RecordRoutingRegistrationDelta records the single-operation counterpart
// of the batch document: the model returns an IpamRoutingPolicyRegistrationDelta
// from Create/Modify/Delete as well, so each mutation authors a one-entry delta
// document and stores it — the deltas listing must know every delta a response
// ever named, or the returned record is a phantom nothing can read back.
func ec2RecordRoutingRegistrationDelta(associationID string, entry map[string]any) EC2IpamRoutingPolicyRegistrationDelta {
	deltaJSON, err := json.Marshal([]map[string]any{entry})
	if err != nil {
		deltaJSON = []byte("[]")
	}
	delta := EC2IpamRoutingPolicyRegistrationDelta{
		AssociationId: associationID,
		DeltaId:       ec2ID("ipam-rpr-delta"),
		DeltaJson:     string(deltaJSON),
		State:         "complete",
	}
	ec2IpamRoutingDeltas.Put(delta.DeltaId, delta)
	return delta
}

func ec2ApplyRoutingRegistrationChanges(registration *EC2IpamRoutingPolicyRegistration, r *http.Request, prefix string) {
	if asns := ec2ParamList(r, prefix+"Asn"); len(asns) > 0 {
		registration.Asns = asns
	}
	if v := r.FormValue(prefix + "PermitMoreSpecificAnnouncements"); v != "" {
		registration.PermitMoreSpecificAnnouncements = v == "true"
	}
	if v := r.FormValue(prefix + "MaxLength"); v != "" {
		registration.MaxLength = atoiDefault(v, registration.MaxLength)
	}
	if v := r.FormValue(prefix + "Description"); v != "" {
		registration.Description = v
	}
}

func handleModifyIpamRoutingPolicyRegistration(w http.ResponseWriter, r *http.Request) {
	associationID := r.FormValue("IpamInternetRegistryAssociationId")
	cidr := r.FormValue("Cidr")
	key := ec2IpamRoutingRegistrationKey(associationID, cidr)
	registration, ok := ec2IpamRoutingRegistrations.Get(key)
	if !ok {
		ec2ErrorXML(w, "InvalidParameterValue",
			fmt.Sprintf("No routing policy registration for %s under %s", cidr, associationID), http.StatusBadRequest)
		return
	}
	ec2ApplyRoutingRegistrationChanges(&registration, r, "")
	delta := ec2RecordRoutingRegistrationDelta(associationID, map[string]any{
		"Cidr": cidr,
		"Asns": registration.Asns,
	})
	registration.LatestDeltaId = delta.DeltaId
	ec2IpamRoutingRegistrations.Put(key, registration)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyIpamRoutingPolicyRegistrationResponse %s>
  <requestId>%s</requestId>
  <ipamRoutingPolicyRegistrationDelta>%s</ipamRoutingPolicyRegistrationDelta>
</ModifyIpamRoutingPolicyRegistrationResponse>`, ec2Xmlns(), generateUUID(), ipamRoutingDeltaXML(delta))
}

func ipamRoutingDeltaXML(delta EC2IpamRoutingPolicyRegistrationDelta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<deltaId>%s</deltaId><deltaJson>%s</deltaJson><state>%s</state>",
		delta.DeltaId, xmlEscape(delta.DeltaJson), delta.State)
	if delta.StateMessage != "" {
		fmt.Fprintf(&b, "<stateMessage>%s</stateMessage>", xmlEscape(delta.StateMessage))
	}
	return b.String()
}

// handleBatchModifyIpamRoutingPolicyRegistrations applies a caller-authored
// delta document. The delta is real state — it gets an identifier, the
// registrations it changes record it as their latest, and the deltas listing
// returns it — and it is applied, not just recorded: each entry of the
// document modifies the registration it names.
func handleBatchModifyIpamRoutingPolicyRegistrations(w http.ResponseWriter, r *http.Request) {
	associationID := r.FormValue("IpamInternetRegistryAssociationId")
	if _, ok := ec2IpamRegistryAssociations.Get(associationID); !ok {
		ec2ErrorXML(w, "InvalidIpamInternetRegistryAssociationId.NotFound",
			fmt.Sprintf("The association ID '%s' does not exist", associationID), http.StatusBadRequest)
		return
	}
	deltaJSON := r.FormValue("DeltaJson")
	if deltaJSON == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter DeltaJson", http.StatusBadRequest)
		return
	}
	var entries []struct {
		Cidr                            string   `json:"Cidr"`
		Asns                            []string `json:"Asns"`
		PermitMoreSpecificAnnouncements *bool    `json:"PermitMoreSpecificAnnouncements"`
		MaxLength                       *int     `json:"MaxLength"`
		Description                     *string  `json:"Description"`
	}
	if err := json.Unmarshal([]byte(deltaJSON), &entries); err != nil {
		ec2ErrorXML(w, "InvalidParameterValue",
			"DeltaJson must be a JSON array of registration modifications: "+err.Error(), http.StatusBadRequest)
		return
	}
	delta := EC2IpamRoutingPolicyRegistrationDelta{
		AssociationId: associationID,
		DeltaId:       ec2ID("ipam-rpr-delta"),
		DeltaJson:     deltaJSON,
		State:         "complete",
	}
	for _, entry := range entries {
		key := ec2IpamRoutingRegistrationKey(associationID, entry.Cidr)
		registration, ok := ec2IpamRoutingRegistrations.Get(key)
		if !ok {
			delta.State = "failed"
			delta.StateMessage = "no routing policy registration for " + entry.Cidr
			break
		}
		if len(entry.Asns) > 0 {
			registration.Asns = entry.Asns
		}
		if entry.PermitMoreSpecificAnnouncements != nil {
			registration.PermitMoreSpecificAnnouncements = *entry.PermitMoreSpecificAnnouncements
		}
		if entry.MaxLength != nil {
			registration.MaxLength = *entry.MaxLength
		}
		if entry.Description != nil {
			registration.Description = *entry.Description
		}
		registration.LatestDeltaId = delta.DeltaId
		ec2IpamRoutingRegistrations.Put(key, registration)
	}
	ec2IpamRoutingDeltas.Put(delta.DeltaId, delta)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<BatchModifyIpamRoutingPolicyRegistrationsResponse %s>
  <requestId>%s</requestId>
  <ipamRoutingPolicyRegistrationDelta>%s</ipamRoutingPolicyRegistrationDelta>
</BatchModifyIpamRoutingPolicyRegistrationsResponse>`, ec2Xmlns(), generateUUID(), ipamRoutingDeltaXML(delta))
}

func handleDeleteIpamRoutingPolicyRegistration(w http.ResponseWriter, r *http.Request) {
	associationID := r.FormValue("IpamInternetRegistryAssociationId")
	cidr := r.FormValue("Cidr")
	key := ec2IpamRoutingRegistrationKey(associationID, cidr)
	registration, ok := ec2IpamRoutingRegistrations.Get(key)
	if !ok {
		ec2ErrorXML(w, "InvalidParameterValue",
			fmt.Sprintf("No routing policy registration for %s under %s", cidr, associationID), http.StatusBadRequest)
		return
	}
	registration.State = "unregistered"
	ec2IpamRoutingRegistrations.Delete(key)
	// The registration is gone, so nothing carries this delta as latest — the
	// deltas listing is the record of the removal.
	delta := ec2RecordRoutingRegistrationDelta(associationID, map[string]any{
		"Cidr":  cidr,
		"State": registration.State,
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteIpamRoutingPolicyRegistrationResponse %s>
  <requestId>%s</requestId>
  <ipamRoutingPolicyRegistrationDelta>%s</ipamRoutingPolicyRegistrationDelta>
</DeleteIpamRoutingPolicyRegistrationResponse>`, ec2Xmlns(), generateUUID(), ipamRoutingDeltaXML(delta))
}

func handleGetIpamRoutingPolicyRegistrations(w http.ResponseWriter, r *http.Request) {
	associationID := r.FormValue("IpamInternetRegistryAssociationId")
	if _, ok := ec2IpamRegistryAssociations.Get(associationID); !ok {
		ec2ErrorXML(w, "InvalidIpamInternetRegistryAssociationId.NotFound",
			fmt.Sprintf("The association ID '%s' does not exist", associationID), http.StatusBadRequest)
		return
	}
	var items strings.Builder
	for _, registration := range ec2IpamRoutingRegistrations.List() {
		if registration.AssociationId != associationID {
			continue
		}
		fmt.Fprintf(&items, "<item>%s</item>", ipamRoutingRegistrationXML(registration))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamRoutingPolicyRegistrationsResponse %s>
  <requestId>%s</requestId>
  <ipamRoutingPolicyRegistrationSet>%s</ipamRoutingPolicyRegistrationSet>
</GetIpamRoutingPolicyRegistrationsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

// handleGetIpamRoutingPolicyRegistrationDeltas lists the batch-modification
// deltas the association has applied — real records, created by
// BatchModifyIpamRoutingPolicyRegistrations, in the shape the SDK
// deserialises.
func handleGetIpamRoutingPolicyRegistrationDeltas(w http.ResponseWriter, r *http.Request) {
	associationID := r.FormValue("IpamInternetRegistryAssociationId")
	var items strings.Builder
	for _, delta := range ec2IpamRoutingDeltas.List() {
		if associationID != "" && delta.AssociationId != associationID {
			continue
		}
		fmt.Fprintf(&items, "<item>%s</item>", ipamRoutingDeltaXML(delta))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamRoutingPolicyRegistrationDeltasResponse %s>
  <requestId>%s</requestId>
  <ipamRoutingPolicyRegistrationDeltaSet>%s</ipamRoutingPolicyRegistrationDeltaSet>
</GetIpamRoutingPolicyRegistrationDeltasResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

// ---- discovered routes and route protection ----

// handleGetIpamDiscoveredRoutes reports the routes IPAM's resource discovery
// finds. The account's route tables are this simulator's own state, so the
// routes come from them — every route of every table, with the CIDR the route
// declares.
func handleGetIpamDiscoveredRoutes(w http.ResponseWriter, r *http.Request) {
	var items strings.Builder
	for _, table := range ec2RouteTables.List() {
		for _, route := range table.Routes {
			if route.DestinationCidrBlock == "" {
				continue
			}
			fmt.Fprintf(&items, "<item><cidr>%s</cidr><resourceRegion>%s</resourceRegion><resourceOwnerId>%s</resourceOwnerId><state>active</state></item>",
				route.DestinationCidrBlock, awsRegion(), ec2Owner())
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamDiscoveredRoutesResponse %s>
  <requestId>%s</requestId>
  <ipamDiscoveredRouteSet>%s</ipamDiscoveredRouteSet>
</GetIpamDiscoveredRoutesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

// Route origin authorizations are RPKI objects published by the Regional
// Internet Registries. That repository is the same external dependency as the
// registry associations above, and the failure names it.
func handleGetIpamRouteOriginAuthorizations(w http.ResponseWriter, r *http.Request) {
	ec2ErrorXML(w, "OperationNotPermitted",
		"route origin authorizations are RPKI objects published by the Regional Internet "+
			"Registries (ARIN, RIPE, APNIC), and this simulator has no connection to their "+
			"repositories. It will not fabricate an ROA: an invented authorization would claim "+
			"a registry attested to something it never saw. The routing policy registrations "+
			"this installation holds are served by GetIpamRoutingPolicyRegistrations.",
		http.StatusBadRequest)
}

// A route protection finding compares an announced route against the ROAs
// covering it. This installation announces nothing to the internet and holds
// no ROAs, so there is nothing to find, and the empty set is the truth.
func handleGetIpamRouteProtectionFindings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamRouteProtectionFindingsResponse %s>
  <requestId>%s</requestId>
  <routeProtectionFindingSet></routeProtectionFindingSet>
</GetIpamRouteProtectionFindingsResponse>`, ec2Xmlns(), generateUUID())
}
