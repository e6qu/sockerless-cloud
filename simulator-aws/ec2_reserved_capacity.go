package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// This file implements three EC2 commitment families: Reserved Instances
// (ri-…, purchased from a real-shaped offering catalog, settling "active"; plus
// marketplace listings, modifications, and Convertible-RI exchange quotes),
// Capacity Reservation billing / topology / splitting (operating on the
// existing ec2CapacityReservations store from ec2_capacity_fleet.go), and
// Capacity Blocks for ML (cb-…, time-bounded reservations purchased from a
// deterministic offering catalog). Every stateful resource is backed by a real
// sim.Store and settles into the state the real EC2 service returns. The
// read-only describe-offering ops return a small honest, deterministic catalog.

// EC2ReservedInstances is a purchased Reserved Instance commitment (ri-…). It
// settles "active" with the count, type, AZ/scope, and a deterministic fixed
// price drawn from the offering it was purchased from.
type EC2ReservedInstances struct {
	ReservedInstancesId string
	InstanceType        string
	AvailabilityZone    string
	Scope               string
	Start               string
	End                 string
	Duration            int64
	FixedPrice          float64
	UsagePrice          float64
	InstanceCount       int
	ProductDescription  string
	State               string
	CurrencyCode        string
	InstanceTenancy     string
	OfferingClass       string
	OfferingType        string
	RecurringAmount     float64
	Tags                []EC2Tag
}

// EC2ReservedInstancesListing offers purchased RIs for resale on the Reserved
// Instance Marketplace.
type EC2ReservedInstancesListing struct {
	ReservedInstancesListingId string
	ReservedInstancesId        string
	CreateDate                 string
	UpdateDate                 string
	Status                     string
	StatusMessage              string
	ClientToken                string
	AvailableCount             int
	SoldCount                  int
	PriceSchedules             []EC2PriceSchedule
	Tags                       []EC2Tag
}

// EC2PriceSchedule is one (term, price) point of a listing's resale schedule.
type EC2PriceSchedule struct {
	Term   int64
	Price  float64
	Active bool
}

// EC2ReservedInstancesModification records an in-place RI modification request,
// producing new RI ids per target configuration.
type EC2ReservedInstancesModification struct {
	ReservedInstancesModificationId string
	ClientToken                     string
	CreateDate                      string
	UpdateDate                      string
	EffectiveDate                   string
	Status                          string
	StatusMessage                   string
	SourceReservedInstancesIds      []string
	ResultReservedInstancesIds      []string
}

// EC2CapacityBlock is a time-bounded ML capacity reservation purchased from a
// Capacity Block offering. It is backed by a capacity reservation.
type EC2CapacityBlock struct {
	CapacityBlockId       string
	UltraserverType       string
	AvailabilityZone      string
	AvailabilityZoneId    string
	CapacityReservationId string
	StartDate             string
	EndDate               string
	CreateDate            string
	State                 string
	InstanceType          string
	InstanceCount         int
	Tags                  []EC2Tag
}

// EC2CapacityBlockExtension records a purchased Capacity Block extension.
type EC2CapacityBlockExtension struct {
	CapacityReservationId               string
	InstanceType                        string
	InstanceCount                       int
	AvailabilityZone                    string
	AvailabilityZoneId                  string
	CapacityBlockExtensionOfferingId    string
	CapacityBlockExtensionDurationHours int
	CapacityBlockExtensionStatus        string
	CapacityBlockExtensionPurchaseDate  string
	CapacityBlockExtensionStartDate     string
	CapacityBlockExtensionEndDate       string
	UpfrontFee                          string
	CurrencyCode                        string
}

// EC2CapacityReservationCancellationQuote is a persisted cancellation quote for
// a capacity reservation, readable back by its quote id.
type EC2CapacityReservationCancellationQuote struct {
	CapacityReservationCancellationQuoteId string
	CapacityReservationId                  string
	CreateTime                             string
	ExpirationTime                         string
	QuoteState                             string
	InstanceCount                          int
	ReservationState                       string
}

// EC2CapacityReservationBillingRequest records a request to transfer the unused
// capacity billing of a shared capacity reservation to another account.
type EC2CapacityReservationBillingRequest struct {
	CapacityReservationId           string
	RequestedBy                     string
	UnusedReservationBillingOwnerId string
	LastUpdateTime                  string
	Status                          string
	StatusMessage                   string
	InstanceType                    string
	AvailabilityZone                string
	Tenancy                         string
}

var (
	ec2ReservedInstances              sim.Store[EC2ReservedInstances]
	ec2ReservedInstancesListings      sim.Store[EC2ReservedInstancesListing]
	ec2ReservedInstancesModifications sim.Store[EC2ReservedInstancesModification]
	ec2CapacityBlocks                 sim.Store[EC2CapacityBlock]
	ec2CapacityBlockExtensions        sim.Store[EC2CapacityBlockExtension]
	ec2CapResBillingRequests          sim.Store[EC2CapacityReservationBillingRequest]
	ec2CapResCancellationQuotes       sim.Store[EC2CapacityReservationCancellationQuote]
)

// registerEC2ReservedCapacity registers this EC2 sub-service's ec2Query actions.
func registerEC2ReservedCapacity(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2ReservedInstances = sim.MakeStore[EC2ReservedInstances](srv.DB(), "ec2_reserved_instances")
	ec2ReservedInstancesListings = sim.MakeStore[EC2ReservedInstancesListing](srv.DB(), "ec2_reserved_instances_listings")
	ec2ReservedInstancesModifications = sim.MakeStore[EC2ReservedInstancesModification](srv.DB(), "ec2_reserved_instances_modifications")
	ec2CapacityBlocks = sim.MakeStore[EC2CapacityBlock](srv.DB(), "ec2_capacity_blocks")
	ec2CapacityBlockExtensions = sim.MakeStore[EC2CapacityBlockExtension](srv.DB(), "ec2_capacity_block_extensions")
	ec2CapResBillingRequests = sim.MakeStore[EC2CapacityReservationBillingRequest](srv.DB(), "ec2_capres_billing_requests")
	ec2CapResCancellationQuotes = sim.MakeStore[EC2CapacityReservationCancellationQuote](srv.DB(), "ec2_capres_cancellation_quotes")

	// Reserved Instances
	// Reserved Instances, Capacity Reservation billing/topology/splitting, and
	// Capacity Blocks for ML.
	for action, h := range map[string]http.HandlerFunc{
		"DescribeReservedInstances":                     handleDescribeReservedInstances,
		"DescribeReservedInstancesOfferings":            handleDescribeReservedInstancesOfferings,
		"DescribeReservedInstancesListings":             handleDescribeReservedInstancesListings,
		"DescribeReservedInstancesModifications":        handleDescribeReservedInstancesModifications,
		"PurchaseReservedInstancesOffering":             handlePurchaseReservedInstancesOffering,
		"ModifyReservedInstances":                       handleModifyReservedInstances,
		"CreateReservedInstancesListing":                handleCreateReservedInstancesListing,
		"CancelReservedInstancesListing":                handleCancelReservedInstancesListing,
		"DeleteQueuedReservedInstances":                 handleDeleteQueuedReservedInstances,
		"GetReservedInstancesExchangeQuote":             handleGetReservedInstancesExchangeQuote,
		"AcceptReservedInstancesExchangeQuote":          handleAcceptReservedInstancesExchangeQuote,
		"DescribeCapacityReservationBillingRequests":    handleDescribeCapacityReservationBillingRequests,
		"AcceptCapacityReservationBillingOwnership":     handleAcceptCapacityReservationBillingOwnership,
		"RejectCapacityReservationBillingOwnership":     handleRejectCapacityReservationBillingOwnership,
		"AssociateCapacityReservationBillingOwner":      handleAssociateCapacityReservationBillingOwner,
		"DisassociateCapacityReservationBillingOwner":   handleDisassociateCapacityReservationBillingOwner,
		"DescribeCapacityReservationTopology":           handleDescribeCapacityReservationTopology,
		"CreateCapacityReservationBySplitting":          handleCreateCapacityReservationBySplitting,
		"MoveCapacityReservationInstances":              handleMoveCapacityReservationInstances,
		"ModifyInstanceCapacityReservationAttributes":   handleModifyInstanceCapacityReservationAttributes,
		"CreateCapacityReservationCancellationQuote":    handleCreateCapacityReservationCancellationQuote,
		"DescribeCapacityReservationCancellationQuotes": handleDescribeCapacityReservationCancellationQuotes,
		"DescribeCapacityBlockOfferings":                handleDescribeCapacityBlockOfferings,
		"DescribeCapacityBlocks":                        handleDescribeCapacityBlocks,
		"DescribeCapacityBlockStatus":                   handleDescribeCapacityBlockStatus,
		"DescribeCapacityBlockExtensionOfferings":       handleDescribeCapacityBlockExtensionOfferings,
		"DescribeCapacityBlockExtensionHistory":         handleDescribeCapacityBlockExtensionHistory,
		"PurchaseCapacityBlock":                         handlePurchaseCapacityBlock,
		"PurchaseCapacityBlockExtension":                handlePurchaseCapacityBlockExtension,
	} {
		r.Register(action, h)
	}
}

// ec2WriteResponse writes an ec2Query XML response with the standard envelope.
func ec2WriteResponse(w http.ResponseWriter, action, body string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s><requestId>%s</requestId>%s</%sResponse>`,
		action, ec2Xmlns(), generateUUID(), body, action)
}

// ec2WriteReturnTrue writes the standard <return>true</return> ec2Query reply.
func ec2WriteReturnTrue(w http.ResponseWriter, action string) {
	ec2WriteResponse(w, action, "<return>true</return>")
}

// ec2RIOffering describes one entry of the deterministic Reserved Instance
// offering catalog. Offering ids are derived from the instance type + term so a
// purchase can recover the offering's terms.
type ec2RIOffering struct {
	OfferingId         string
	InstanceType       string
	AvailabilityZone   string
	Scope              string
	Duration           int64
	FixedPrice         float64
	UsagePrice         float64
	RecurringAmount    float64
	ProductDescription string
	OfferingClass      string
	OfferingType       string
	Marketplace        bool
}

// ec2RICatalog returns the deterministic Reserved Instance offering catalog: a
// 1-year and a 3-year offering for each of a few common instance types, in both
// Region and Availability Zone scope.
func ec2RICatalog() []ec2RIOffering {
	var out []ec2RIOffering
	types := []struct {
		it    string
		price float64
	}{
		{"t3.micro", 50.0},
		{"t3.medium", 200.0},
		{"m5.large", 600.0},
		{"c5.xlarge", 1100.0},
	}
	terms := []struct {
		dur   int64
		mult  float64
		class string
	}{
		{31536000, 1.0, "standard"},
		{94608000, 2.6, "standard"},
	}
	for _, t := range types {
		for _, term := range terms {
			fixed := t.price * term.mult
			out = append(out, ec2RIOffering{
				OfferingId:         ec2RIOfferingID(t.it, term.dur, "Region"),
				InstanceType:       t.it,
				Scope:              "Region",
				Duration:           term.dur,
				FixedPrice:         fixed,
				UsagePrice:         0,
				RecurringAmount:    0,
				ProductDescription: "Linux/UNIX",
				OfferingClass:      term.class,
				OfferingType:       "All Upfront",
			})
			out = append(out, ec2RIOffering{
				OfferingId:         ec2RIOfferingID(t.it, term.dur, "Availability Zone"),
				InstanceType:       t.it,
				AvailabilityZone:   awsAvailabilityZone(),
				Scope:              "Availability Zone",
				Duration:           term.dur,
				FixedPrice:         fixed,
				UsagePrice:         0,
				RecurringAmount:    0,
				ProductDescription: "Linux/UNIX",
				OfferingClass:      term.class,
				OfferingType:       "All Upfront",
			})
		}
	}
	return out
}

// ec2RIOfferingID derives a deterministic, recoverable offering id.
func ec2RIOfferingID(instanceType string, duration int64, scope string) string {
	scopeTag := "r"
	if scope == "Availability Zone" {
		scopeTag = "z"
	}
	return fmt.Sprintf("%s-%d-%s-aaaaaaaaaaaaaaaaa", instanceType, duration, scopeTag)
}

// ec2FindRIOffering looks an offering up by id in the catalog.
func ec2FindRIOffering(id string) (ec2RIOffering, bool) {
	for _, o := range ec2RICatalog() {
		if o.OfferingId == id {
			return o, true
		}
	}
	return ec2RIOffering{}, false
}

func ec2RIOfferingFieldsXML(o ec2RIOffering) string {
	az := ""
	if o.AvailabilityZone != "" {
		az = fmt.Sprintf("<availabilityZone>%s</availabilityZone>", o.AvailabilityZone)
	}
	return fmt.Sprintf("<reservedInstancesOfferingId>%s</reservedInstancesOfferingId><instanceType>%s</instanceType>%s<duration>%d</duration><fixedPrice>%s</fixedPrice><usagePrice>%s</usagePrice><productDescription>%s</productDescription><instanceTenancy>default</instanceTenancy><currencyCode>USD</currencyCode><offeringType>%s</offeringType><recurringCharges>%s</recurringCharges><marketplace>%t</marketplace><offeringClass>%s</offeringClass><scope>%s</scope><pricingDetailsSet/>",
		o.OfferingId, o.InstanceType, az, o.Duration, ec2FloatStr(o.FixedPrice), ec2FloatStr(o.UsagePrice),
		o.ProductDescription, o.OfferingType, ec2RecurringChargesXML(o.RecurringAmount), o.Marketplace, o.OfferingClass, o.Scope)
}

// ec2RecurringChargesXML renders a recurringCharges list (empty if amount==0).
func ec2RecurringChargesXML(amount float64) string {
	if amount == 0 {
		return ""
	}
	return fmt.Sprintf("<item><frequency>Hourly</frequency><amount>%s</amount></item>", ec2FloatStr(amount))
}

func ec2RIFieldsXML(ri EC2ReservedInstances) string {
	az := ""
	if ri.AvailabilityZone != "" {
		az = fmt.Sprintf("<availabilityZone>%s</availabilityZone>", ri.AvailabilityZone)
	}
	end := ""
	if ri.End != "" {
		end = fmt.Sprintf("<end>%s</end>", ri.End)
	}
	return fmt.Sprintf("<reservedInstancesId>%s</reservedInstancesId><instanceType>%s</instanceType>%s<start>%s</start>%s<duration>%d</duration><fixedPrice>%s</fixedPrice><usagePrice>%s</usagePrice><instanceCount>%d</instanceCount><productDescription>%s</productDescription><state>%s</state><currencyCode>%s</currencyCode><instanceTenancy>%s</instanceTenancy><offeringClass>%s</offeringClass><offeringType>%s</offeringType><recurringCharges>%s</recurringCharges><scope>%s</scope>%s",
		ri.ReservedInstancesId, ri.InstanceType, az, ri.Start, end, ri.Duration,
		ec2FloatStr(ri.FixedPrice), ec2FloatStr(ri.UsagePrice), ri.InstanceCount,
		ri.ProductDescription, ri.State, ri.CurrencyCode, ri.InstanceTenancy,
		ri.OfferingClass, ri.OfferingType, ec2RecurringChargesXML(ri.RecurringAmount),
		ri.Scope, writeTagSetXML(ri.Tags))
}

func handleDescribeReservedInstancesOfferings(w http.ResponseWriter, r *http.Request) {
	wantType := r.FormValue("InstanceType")
	wantOffering := r.FormValue("OfferingClass")
	offerings := ec2RICatalog()
	var items strings.Builder
	for _, o := range offerings {
		if wantType != "" && o.InstanceType != wantType {
			continue
		}
		if wantOffering != "" && o.OfferingClass != wantOffering {
			continue
		}
		items.WriteString("<item>")
		items.WriteString(ec2RIOfferingFieldsXML(o))
		items.WriteString("</item>")
	}
	ec2WriteResponse(w, "DescribeReservedInstancesOfferings",
		fmt.Sprintf("<reservedInstancesOfferingsSet>%s</reservedInstancesOfferingsSet>", items.String()))
}

func handlePurchaseReservedInstancesOffering(w http.ResponseWriter, r *http.Request) {
	offeringID := r.FormValue("ReservedInstancesOfferingId")
	offering, ok := ec2FindRIOffering(offeringID)
	if !ok {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("The Reserved Instances offering ID %q does not exist", offeringID), http.StatusBadRequest)
		return
	}
	count := ec2AtoiOr(r.FormValue("InstanceCount"), 1)
	now := time.Now().UTC()
	ri := EC2ReservedInstances{
		ReservedInstancesId: ec2ID("ri"),
		InstanceType:        offering.InstanceType,
		AvailabilityZone:    offering.AvailabilityZone,
		Scope:               offering.Scope,
		Start:               now.Format("2006-01-02T15:04:05.000Z"),
		End:                 now.Add(time.Duration(offering.Duration) * time.Second).Format("2006-01-02T15:04:05.000Z"),
		Duration:            offering.Duration,
		FixedPrice:          offering.FixedPrice,
		UsagePrice:          offering.UsagePrice,
		InstanceCount:       count,
		ProductDescription:  offering.ProductDescription,
		State:               "active",
		CurrencyCode:        "USD",
		InstanceTenancy:     "default",
		OfferingClass:       offering.OfferingClass,
		OfferingType:        offering.OfferingType,
		RecurringAmount:     offering.RecurringAmount,
	}
	ec2ReservedInstances.Put(ri.ReservedInstancesId, ri)
	ec2WriteResponse(w, "PurchaseReservedInstancesOffering",
		fmt.Sprintf("<reservedInstancesId>%s</reservedInstancesId>", ri.ReservedInstancesId))
}

func handleDescribeReservedInstances(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ReservedInstancesId")
	filters := ec2Filters(r)
	results := make([]EC2ReservedInstances, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			ri, ok := ec2ReservedInstances.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidReservedInstancesId.NotFound", fmt.Sprintf("The Reserved Instance ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, ri)
		}
	} else {
		for _, ri := range ec2ReservedInstances.List() {
			if !ec2RIMatchesFilters(ri, filters) {
				continue
			}
			results = append(results, ri)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ReservedInstancesId < results[j].ReservedInstancesId })
	var items strings.Builder
	for _, ri := range results {
		items.WriteString("<item>")
		items.WriteString(ec2RIFieldsXML(ri))
		items.WriteString("</item>")
	}
	ec2WriteResponse(w, "DescribeReservedInstances",
		fmt.Sprintf("<reservedInstancesSet>%s</reservedInstancesSet>", items.String()))
}

func ec2RIMatchesFilters(ri EC2ReservedInstances, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "instance-type":
			if !ec2StrInValues(ri.InstanceType, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(ri.State, vals) {
				return false
			}
		case "availability-zone":
			if !ec2StrInValues(ri.AvailabilityZone, vals) {
				return false
			}
		case "scope":
			if !ec2StrInValues(ri.Scope, vals) {
				return false
			}
		case "product-description":
			if !ec2StrInValues(ri.ProductDescription, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, ri.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleModifyReservedInstances(w http.ResponseWriter, r *http.Request) {
	srcIDs := ec2ParamList(r, "ReservedInstancesId")
	if len(srcIDs) == 0 {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ReservedInstancesId", http.StatusBadRequest)
		return
	}
	now := ec2NowMilli()
	mod := EC2ReservedInstancesModification{
		ReservedInstancesModificationId: ec2ID("rimod"),
		ClientToken:                     r.FormValue("ClientToken"),
		CreateDate:                      now,
		UpdateDate:                      now,
		EffectiveDate:                   now,
		Status:                          "processing",
		SourceReservedInstancesIds:      srcIDs,
	}
	// Each target configuration yields a new Reserved Instance derived from the
	// first source RI's terms.
	var src EC2ReservedInstances
	if ri, ok := ec2ReservedInstances.Get(srcIDs[0]); ok {
		src = ri
	}
	for i := 1; ; i++ {
		it := r.FormValue(fmt.Sprintf("ReservedInstancesConfigurationSetItemType.%d.InstanceType", i))
		az := r.FormValue(fmt.Sprintf("ReservedInstancesConfigurationSetItemType.%d.AvailabilityZone", i))
		cnt := r.FormValue(fmt.Sprintf("ReservedInstancesConfigurationSetItemType.%d.InstanceCount", i))
		if it == "" && az == "" && cnt == "" {
			break
		}
		newRI := src
		newRI.ReservedInstancesId = ec2ID("ri")
		if it != "" {
			newRI.InstanceType = it
		}
		if az != "" {
			newRI.AvailabilityZone = az
		}
		if cnt != "" {
			newRI.InstanceCount = ec2AtoiOr(cnt, src.InstanceCount)
		}
		newRI.State = "active"
		ec2ReservedInstances.Put(newRI.ReservedInstancesId, newRI)
		mod.ResultReservedInstancesIds = append(mod.ResultReservedInstancesIds, newRI.ReservedInstancesId)
	}
	ec2ReservedInstancesModifications.Put(mod.ReservedInstancesModificationId, mod)
	ec2WriteResponse(w, "ModifyReservedInstances",
		fmt.Sprintf("<reservedInstancesModificationId>%s</reservedInstancesModificationId>", mod.ReservedInstancesModificationId))
}

func ec2RIModFieldsXML(mod EC2ReservedInstancesModification) string {
	var srcSet strings.Builder
	for _, id := range mod.SourceReservedInstancesIds {
		fmt.Fprintf(&srcSet, "<item><reservedInstancesId>%s</reservedInstancesId></item>", id)
	}
	var resSet strings.Builder
	for _, id := range mod.ResultReservedInstancesIds {
		fmt.Fprintf(&resSet, "<item><reservedInstancesId>%s</reservedInstancesId></item>", id)
	}
	clientToken := ""
	if mod.ClientToken != "" {
		clientToken = fmt.Sprintf("<clientToken>%s</clientToken>", mod.ClientToken)
	}
	return fmt.Sprintf("<reservedInstancesModificationId>%s</reservedInstancesModificationId><reservedInstancesSet>%s</reservedInstancesSet><modificationResultSet>%s</modificationResultSet><createDate>%s</createDate><updateDate>%s</updateDate><effectiveDate>%s</effectiveDate><status>%s</status>%s",
		mod.ReservedInstancesModificationId, srcSet.String(), resSet.String(),
		mod.CreateDate, mod.UpdateDate, mod.EffectiveDate, mod.Status, clientToken)
}

func handleDescribeReservedInstancesModifications(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ReservedInstancesModificationId")
	results := make([]EC2ReservedInstancesModification, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			mod, ok := ec2ReservedInstancesModifications.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("The Reserved Instances modification ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, mod)
		}
	} else {
		results = append(results, ec2ReservedInstancesModifications.List()...)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].ReservedInstancesModificationId < results[j].ReservedInstancesModificationId
	})
	var items strings.Builder
	for _, mod := range results {
		items.WriteString("<item>")
		items.WriteString(ec2RIModFieldsXML(mod))
		items.WriteString("</item>")
	}
	ec2WriteResponse(w, "DescribeReservedInstancesModifications",
		fmt.Sprintf("<reservedInstancesModificationsSet>%s</reservedInstancesModificationsSet>", items.String()))
}

func ec2RIListingFieldsXML(l EC2ReservedInstancesListing) string {
	var counts strings.Builder
	fmt.Fprintf(&counts, "<item><state>available</state><instanceCount>%d</instanceCount></item>", l.AvailableCount)
	fmt.Fprintf(&counts, "<item><state>sold</state><instanceCount>%d</instanceCount></item>", l.SoldCount)
	var schedules strings.Builder
	for _, ps := range l.PriceSchedules {
		fmt.Fprintf(&schedules, "<item><term>%d</term><price>%s</price><currencyCode>USD</currencyCode><active>%t</active></item>",
			ps.Term, ec2FloatStr(ps.Price), ps.Active)
	}
	clientToken := ""
	if l.ClientToken != "" {
		clientToken = fmt.Sprintf("<clientToken>%s</clientToken>", l.ClientToken)
	}
	statusMsg := ""
	if l.StatusMessage != "" {
		statusMsg = fmt.Sprintf("<statusMessage>%s</statusMessage>", l.StatusMessage)
	}
	return fmt.Sprintf("<reservedInstancesListingId>%s</reservedInstancesListingId><reservedInstancesId>%s</reservedInstancesId><createDate>%s</createDate><updateDate>%s</updateDate><status>%s</status>%s<instanceCounts>%s</instanceCounts><priceSchedules>%s</priceSchedules>%s%s",
		l.ReservedInstancesListingId, l.ReservedInstancesId, l.CreateDate, l.UpdateDate, l.Status,
		statusMsg, counts.String(), schedules.String(), clientToken, writeTagSetXML(l.Tags))
}

func handleCreateReservedInstancesListing(w http.ResponseWriter, r *http.Request) {
	riID := r.FormValue("ReservedInstancesId")
	ri, ok := ec2ReservedInstances.Get(riID)
	if !ok {
		ec2ErrorXML(w, "InvalidReservedInstancesId.NotFound", fmt.Sprintf("The Reserved Instance ID %q does not exist", riID), http.StatusBadRequest)
		return
	}
	count := ec2AtoiOr(r.FormValue("InstanceCount"), ri.InstanceCount)
	var schedules []EC2PriceSchedule
	for i := 1; ; i++ {
		term := r.FormValue(fmt.Sprintf("PriceSchedules.%d.Term", i))
		price := r.FormValue(fmt.Sprintf("PriceSchedules.%d.Price", i))
		if term == "" && price == "" {
			break
		}
		t, _ := strconv.ParseInt(term, 10, 64)
		p, _ := strconv.ParseFloat(price, 64)
		schedules = append(schedules, EC2PriceSchedule{Term: t, Price: p, Active: i == 1})
	}
	now := ec2NowMilli()
	l := EC2ReservedInstancesListing{
		ReservedInstancesListingId: ec2ID("ril"),
		ReservedInstancesId:        riID,
		CreateDate:                 now,
		UpdateDate:                 now,
		Status:                     "active",
		StatusMessage:              "ACTIVE",
		ClientToken:                r.FormValue("ClientToken"),
		AvailableCount:             count,
		SoldCount:                  0,
		PriceSchedules:             schedules,
	}
	ec2ReservedInstancesListings.Put(l.ReservedInstancesListingId, l)
	ec2WriteResponse(w, "CreateReservedInstancesListing",
		fmt.Sprintf("<reservedInstancesListingsSet><item>%s</item></reservedInstancesListingsSet>", ec2RIListingFieldsXML(l)))
}

func handleDescribeReservedInstancesListings(w http.ResponseWriter, r *http.Request) {
	listingID := r.FormValue("ReservedInstancesListingId")
	riID := r.FormValue("ReservedInstancesId")
	results := make([]EC2ReservedInstancesListing, 0)
	if listingID != "" {
		l, ok := ec2ReservedInstancesListings.Get(listingID)
		if !ok {
			ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("The Reserved Instances listing ID %q does not exist", listingID), http.StatusBadRequest)
			return
		}
		results = append(results, l)
	} else {
		for _, l := range ec2ReservedInstancesListings.List() {
			if riID != "" && l.ReservedInstancesId != riID {
				continue
			}
			results = append(results, l)
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].ReservedInstancesListingId < results[j].ReservedInstancesListingId
	})
	var items strings.Builder
	for _, l := range results {
		items.WriteString("<item>")
		items.WriteString(ec2RIListingFieldsXML(l))
		items.WriteString("</item>")
	}
	ec2WriteResponse(w, "DescribeReservedInstancesListings",
		fmt.Sprintf("<reservedInstancesListingsSet>%s</reservedInstancesListingsSet>", items.String()))
}

func handleCancelReservedInstancesListing(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ReservedInstancesListingId")
	l, ok := ec2ReservedInstancesListings.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("The Reserved Instances listing ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	l.Status = "cancelled"
	l.StatusMessage = "CANCELLED"
	l.UpdateDate = ec2NowMilli()
	ec2ReservedInstancesListings.Put(id, l)
	ec2WriteResponse(w, "CancelReservedInstancesListing",
		fmt.Sprintf("<reservedInstancesListingsSet><item>%s</item></reservedInstancesListingsSet>", ec2RIListingFieldsXML(l)))
}

func handleDeleteQueuedReservedInstances(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ReservedInstancesId")
	var success, failed strings.Builder
	for _, id := range ids {
		ri, ok := ec2ReservedInstances.Get(id)
		// Only RIs in the "queued" state can be deleted.
		if !ok || ri.State != "queued" {
			fmt.Fprintf(&failed, "<item><reservedInstancesId>%s</reservedInstancesId><error><code>InvalidState</code><message>The Reserved Instance %s is not in the queued state</message></error></item>", id, id)
			continue
		}
		ec2ReservedInstances.Delete(id)
		fmt.Fprintf(&success, "<item><reservedInstancesId>%s</reservedInstancesId></item>", id)
	}
	ec2WriteResponse(w, "DeleteQueuedReservedInstances",
		fmt.Sprintf("<successfulQueuedPurchaseDeletionSet>%s</successfulQueuedPurchaseDeletionSet><failedQueuedPurchaseDeletionSet>%s</failedQueuedPurchaseDeletionSet>", success.String(), failed.String()))
}

func handleGetReservedInstancesExchangeQuote(w http.ResponseWriter, r *http.Request) {
	riIDs := ec2ParamList(r, "ReservedInstanceId")
	if len(riIDs) == 0 {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ReservedInstanceId", http.StatusBadRequest)
		return
	}
	var rollupTotal float64
	var riValueSet strings.Builder
	for _, id := range riIDs {
		ri, ok := ec2ReservedInstances.Get(id)
		if !ok {
			ec2ErrorXML(w, "InvalidReservedInstancesId.NotFound", fmt.Sprintf("The Reserved Instance ID %q does not exist", id), http.StatusBadRequest)
			return
		}
		val := ri.FixedPrice
		rollupTotal += val
		fmt.Fprintf(&riValueSet, "<item><reservedInstanceId>%s</reservedInstanceId><reservationValue><remainingTotalValue>%s</remainingTotalValue><remainingUpfrontValue>%s</remainingUpfrontValue><hourlyPrice>0.000</hourlyPrice></reservationValue></item>",
			id, ec2FloatStr(val), ec2FloatStr(val))
	}
	// Target configurations form the exchange's target value rollup.
	var targetValueSet strings.Builder
	var targetTotal float64
	for i := 1; ; i++ {
		offeringID := r.FormValue(fmt.Sprintf("TargetConfiguration.%d.OfferingId", i))
		if offeringID == "" {
			break
		}
		cnt := ec2AtoiOr(r.FormValue(fmt.Sprintf("TargetConfiguration.%d.InstanceCount", i)), 1)
		var val float64
		if o, ok := ec2FindRIOffering(offeringID); ok {
			val = o.FixedPrice * float64(cnt)
		}
		targetTotal += val
		fmt.Fprintf(&targetValueSet, "<item><targetConfiguration><offeringId>%s</offeringId><instanceCount>%d</instanceCount></targetConfiguration><reservationValue><remainingTotalValue>%s</remainingTotalValue><remainingUpfrontValue>%s</remainingUpfrontValue><hourlyPrice>0.000</hourlyPrice></reservationValue></item>",
			offeringID, cnt, ec2FloatStr(val), ec2FloatStr(val))
	}
	paymentDue := targetTotal - rollupTotal
	if paymentDue < 0 {
		paymentDue = 0
	}
	expireAt := time.Now().UTC().AddDate(1, 0, 0).Format("2006-01-02T15:04:05.000Z")
	body := fmt.Sprintf("<reservedInstanceValueSet>%s</reservedInstanceValueSet><reservedInstanceValueRollup><remainingTotalValue>%s</remainingTotalValue><remainingUpfrontValue>%s</remainingUpfrontValue><hourlyPrice>0.000</hourlyPrice></reservedInstanceValueRollup><targetConfigurationValueSet>%s</targetConfigurationValueSet><targetConfigurationValueRollup><remainingTotalValue>%s</remainingTotalValue><remainingUpfrontValue>%s</remainingUpfrontValue><hourlyPrice>0.000</hourlyPrice></targetConfigurationValueRollup><isValidExchange>true</isValidExchange><outputReservedInstancesWillExpireAt>%s</outputReservedInstancesWillExpireAt><paymentDue>%s</paymentDue><currencyCode>USD</currencyCode>",
		riValueSet.String(), ec2FloatStr(rollupTotal), ec2FloatStr(rollupTotal),
		targetValueSet.String(), ec2FloatStr(targetTotal), ec2FloatStr(targetTotal),
		expireAt, ec2FloatStr(paymentDue))
	ec2WriteResponse(w, "GetReservedInstancesExchangeQuote", body)
}

func handleAcceptReservedInstancesExchangeQuote(w http.ResponseWriter, r *http.Request) {
	riIDs := ec2ParamList(r, "ReservedInstanceId")
	if len(riIDs) == 0 {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ReservedInstanceId", http.StatusBadRequest)
		return
	}
	// Settle the exchange: the source convertible RIs become retired and a new RI
	// is created for each target configuration.
	for _, id := range riIDs {
		if ri, ok := ec2ReservedInstances.Get(id); ok {
			ri.State = "retired"
			ec2ReservedInstances.Put(id, ri)
		}
	}
	for i := 1; ; i++ {
		offeringID := r.FormValue(fmt.Sprintf("TargetConfiguration.%d.OfferingId", i))
		if offeringID == "" {
			break
		}
		if o, ok := ec2FindRIOffering(offeringID); ok {
			cnt := ec2AtoiOr(r.FormValue(fmt.Sprintf("TargetConfiguration.%d.InstanceCount", i)), 1)
			now := time.Now().UTC()
			newRI := EC2ReservedInstances{
				ReservedInstancesId: ec2ID("ri"),
				InstanceType:        o.InstanceType,
				AvailabilityZone:    o.AvailabilityZone,
				Scope:               o.Scope,
				Start:               now.Format("2006-01-02T15:04:05.000Z"),
				End:                 now.Add(time.Duration(o.Duration) * time.Second).Format("2006-01-02T15:04:05.000Z"),
				Duration:            o.Duration,
				FixedPrice:          o.FixedPrice,
				InstanceCount:       cnt,
				ProductDescription:  o.ProductDescription,
				State:               "active",
				CurrencyCode:        "USD",
				InstanceTenancy:     "default",
				OfferingClass:       "convertible",
				OfferingType:        o.OfferingType,
			}
			ec2ReservedInstances.Put(newRI.ReservedInstancesId, newRI)
		}
	}
	ec2WriteResponse(w, "AcceptReservedInstancesExchangeQuote",
		fmt.Sprintf("<exchangeId>%s</exchangeId>", ec2ID("riex")))
}

func ec2CapResBillingRequestFieldsXML(br EC2CapacityReservationBillingRequest) string {
	statusMsg := ""
	if br.StatusMessage != "" {
		statusMsg = fmt.Sprintf("<statusMessage>%s</statusMessage>", br.StatusMessage)
	}
	return fmt.Sprintf("<capacityReservationId>%s</capacityReservationId><requestedBy>%s</requestedBy><unusedReservationBillingOwnerId>%s</unusedReservationBillingOwnerId><lastUpdateTime>%s</lastUpdateTime><status>%s</status>%s<capacityReservationInfo><instanceType>%s</instanceType><availabilityZone>%s</availabilityZone><tenancy>%s</tenancy></capacityReservationInfo>",
		br.CapacityReservationId, br.RequestedBy, br.UnusedReservationBillingOwnerId,
		br.LastUpdateTime, br.Status, statusMsg, br.InstanceType, br.AvailabilityZone, br.Tenancy)
}

func handleAssociateCapacityReservationBillingOwner(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CapacityReservationId")
	cr, ok := ec2CapacityReservations.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("The Capacity Reservation ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	owner := r.FormValue("UnusedReservationBillingOwnerId")
	if owner == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter UnusedReservationBillingOwnerId", http.StatusBadRequest)
		return
	}
	br := EC2CapacityReservationBillingRequest{
		CapacityReservationId:           id,
		RequestedBy:                     ec2Owner(),
		UnusedReservationBillingOwnerId: owner,
		LastUpdateTime:                  ec2NowMilli(),
		Status:                          "pending",
		InstanceType:                    cr.InstanceType,
		AvailabilityZone:                cr.AvailabilityZone,
		Tenancy:                         cr.Tenancy,
	}
	ec2CapResBillingRequests.Put(id, br)
	ec2WriteReturnTrue(w, "AssociateCapacityReservationBillingOwner")
}

func handleDisassociateCapacityReservationBillingOwner(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CapacityReservationId")
	if _, ok := ec2CapacityReservations.Get(id); !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("The Capacity Reservation ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	if br, ok := ec2CapResBillingRequests.Get(id); ok {
		br.Status = "revoked"
		br.LastUpdateTime = ec2NowMilli()
		ec2CapResBillingRequests.Put(id, br)
	}
	ec2WriteReturnTrue(w, "DisassociateCapacityReservationBillingOwner")
}

func handleAcceptCapacityReservationBillingOwnership(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CapacityReservationId")
	br, ok := ec2CapResBillingRequests.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("No billing request found for Capacity Reservation %q", id), http.StatusBadRequest)
		return
	}
	br.Status = "accepted"
	br.LastUpdateTime = ec2NowMilli()
	ec2CapResBillingRequests.Put(id, br)
	ec2WriteReturnTrue(w, "AcceptCapacityReservationBillingOwnership")
}

func handleRejectCapacityReservationBillingOwnership(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CapacityReservationId")
	br, ok := ec2CapResBillingRequests.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("No billing request found for Capacity Reservation %q", id), http.StatusBadRequest)
		return
	}
	br.Status = "rejected"
	br.LastUpdateTime = ec2NowMilli()
	ec2CapResBillingRequests.Put(id, br)
	ec2WriteReturnTrue(w, "RejectCapacityReservationBillingOwnership")
}

func handleDescribeCapacityReservationBillingRequests(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "CapacityReservationId")
	role := r.FormValue("Role")
	results := make([]EC2CapacityReservationBillingRequest, 0)
	for _, br := range ec2CapResBillingRequests.List() {
		if len(ids) > 0 && !ec2StrInValues(br.CapacityReservationId, ids) {
			continue
		}
		// odcr_owner sees requests it initiated; unused_reservation_billing_owner
		// sees requests targeted at it. The sim's single account owns both ends, so
		// either role surfaces the request.
		_ = role
		results = append(results, br)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].CapacityReservationId < results[j].CapacityReservationId
	})
	var items strings.Builder
	for _, br := range results {
		items.WriteString("<item>")
		items.WriteString(ec2CapResBillingRequestFieldsXML(br))
		items.WriteString("</item>")
	}
	ec2WriteResponse(w, "DescribeCapacityReservationBillingRequests",
		fmt.Sprintf("<capacityReservationBillingRequestSet>%s</capacityReservationBillingRequestSet>", items.String()))
}

func handleDescribeCapacityReservationTopology(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "CapacityReservationId")
	results := make([]EC2CapacityReservation, 0)
	for _, cr := range ec2CapacityReservations.List() {
		if len(ids) > 0 && !ec2StrInValues(cr.CapacityReservationId, ids) {
			continue
		}
		results = append(results, cr)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].CapacityReservationId < results[j].CapacityReservationId
	})
	var items strings.Builder
	for _, cr := range results {
		nodes := ""
		if cr.State == "active" || cr.State == "pending" {
			nodes = "<networkNodeSet><item>nn-0123456789abcdef0</item><item>nn-fedcba9876543210f</item></networkNodeSet>"
		}
		fmt.Fprintf(&items, "<item><capacityReservationId>%s</capacityReservationId><instanceType>%s</instanceType><availabilityZone>%s</availabilityZone><state>%s</state>%s</item>",
			cr.CapacityReservationId, cr.InstanceType, cr.AvailabilityZone, cr.State, nodes)
	}
	ec2WriteResponse(w, "DescribeCapacityReservationTopology",
		fmt.Sprintf("<capacityReservationSet>%s</capacityReservationSet>", items.String()))
}

func handleCreateCapacityReservationBySplitting(w http.ResponseWriter, r *http.Request) {
	srcID := r.FormValue("SourceCapacityReservationId")
	src, ok := ec2CapacityReservations.Get(srcID)
	if !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("The Capacity Reservation ID %q does not exist", srcID), http.StatusBadRequest)
		return
	}
	count := ec2AtoiOr(r.FormValue("InstanceCount"), 0)
	if count <= 0 || count > src.AvailableInstanceCount {
		ec2ErrorXML(w, "InvalidParameterValue", "InstanceCount must be a positive value no greater than the source reservation's available instance count", http.StatusBadRequest)
		return
	}
	// Reduce the source and create a destination reservation holding the split.
	src.TotalInstanceCount -= count
	src.AvailableInstanceCount -= count
	ec2CapacityReservations.Put(srcID, src)

	dest := src
	dest.CapacityReservationId = ec2ID("cr")
	dest.TotalInstanceCount = count
	dest.AvailableInstanceCount = count
	dest.CreateDate = ec2NowMilli()
	dest.StartDate = ec2NowMilli()
	dest.Tags = ec2ParseTagSpecs(r)
	ec2CapacityReservations.Put(dest.CapacityReservationId, dest)

	ec2WriteResponse(w, "CreateCapacityReservationBySplitting",
		fmt.Sprintf("<sourceCapacityReservation>%s</sourceCapacityReservation><destinationCapacityReservation>%s</destinationCapacityReservation><instanceCount>%d</instanceCount>",
			ec2CapReservationFieldsXML(src), ec2CapReservationFieldsXML(dest), count))
}

func handleMoveCapacityReservationInstances(w http.ResponseWriter, r *http.Request) {
	srcID := r.FormValue("SourceCapacityReservationId")
	destID := r.FormValue("DestinationCapacityReservationId")
	src, ok := ec2CapacityReservations.Get(srcID)
	if !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("The Capacity Reservation ID %q does not exist", srcID), http.StatusBadRequest)
		return
	}
	dest, ok := ec2CapacityReservations.Get(destID)
	if !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("The Capacity Reservation ID %q does not exist", destID), http.StatusBadRequest)
		return
	}
	count := ec2AtoiOr(r.FormValue("InstanceCount"), 0)
	if count <= 0 || count > src.AvailableInstanceCount {
		ec2ErrorXML(w, "InvalidParameterValue", "InstanceCount must be a positive value no greater than the source reservation's available instance count", http.StatusBadRequest)
		return
	}
	src.TotalInstanceCount -= count
	src.AvailableInstanceCount -= count
	dest.TotalInstanceCount += count
	dest.AvailableInstanceCount += count
	ec2CapacityReservations.Put(srcID, src)
	ec2CapacityReservations.Put(destID, dest)
	ec2WriteResponse(w, "MoveCapacityReservationInstances",
		fmt.Sprintf("<sourceCapacityReservation>%s</sourceCapacityReservation><destinationCapacityReservation>%s</destinationCapacityReservation><instanceCount>%d</instanceCount>",
			ec2CapReservationFieldsXML(src), ec2CapReservationFieldsXML(dest), count))
}

func handleModifyInstanceCapacityReservationAttributes(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("InstanceId") == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter InstanceId", http.StatusBadRequest)
		return
	}
	ec2WriteReturnTrue(w, "ModifyInstanceCapacityReservationAttributes")
}

func handleCreateCapacityReservationCancellationQuote(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CapacityReservationId")
	cr, ok := ec2CapacityReservations.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("The Capacity Reservation ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	q := EC2CapacityReservationCancellationQuote{
		CapacityReservationCancellationQuoteId: ec2ID("crcq"),
		CapacityReservationId:                  cr.CapacityReservationId,
		CreateTime:                             now.Format("2006-01-02T15:04:05.000Z"),
		ExpirationTime:                         now.Add(24 * time.Hour).Format("2006-01-02T15:04:05.000Z"),
		QuoteState:                             "active",
		InstanceCount:                          cr.TotalInstanceCount,
		ReservationState:                       cr.State,
	}
	ec2CapResCancellationQuotes.Put(q.CapacityReservationCancellationQuoteId, q)
	body := fmt.Sprintf("<capacityReservationCancellationQuote>%s</capacityReservationCancellationQuote>",
		ec2CapResCancellationQuoteXML(q))
	ec2WriteResponse(w, "CreateCapacityReservationCancellationQuote", body)
}

func ec2CapResCancellationQuoteXML(q EC2CapacityReservationCancellationQuote) string {
	terms := fmt.Sprintf("<item><cancellationType>none</cancellationType><reservationState>%s</reservationState><committedInstanceCount>%d</committedInstanceCount></item>",
		q.ReservationState, q.InstanceCount)
	return fmt.Sprintf("<capacityReservationCancellationQuoteId>%s</capacityReservationCancellationQuoteId><capacityReservationId>%s</capacityReservationId><createTime>%s</createTime><expirationTime>%s</expirationTime><quoteState>%s</quoteState><currentConfiguration><instanceCount>%d</instanceCount><reservationState>%s</reservationState></currentConfiguration><cancellationTermSet>%s</cancellationTermSet><tagSet/>",
		q.CapacityReservationCancellationQuoteId, q.CapacityReservationId, q.CreateTime, q.ExpirationTime,
		q.QuoteState, q.InstanceCount, q.ReservationState, terms)
}

func handleDescribeCapacityReservationCancellationQuotes(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "CapacityReservationCancellationQuoteId")
	results := make([]EC2CapacityReservationCancellationQuote, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			q, ok := ec2CapResCancellationQuotes.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("The cancellation quote ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, q)
		}
	} else {
		results = append(results, ec2CapResCancellationQuotes.List()...)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].CapacityReservationCancellationQuoteId < results[j].CapacityReservationCancellationQuoteId
	})
	var items strings.Builder
	for _, q := range results {
		items.WriteString("<item>")
		items.WriteString(ec2CapResCancellationQuoteXML(q))
		items.WriteString("</item>")
	}
	ec2WriteResponse(w, "DescribeCapacityReservationCancellationQuotes",
		fmt.Sprintf("<capacityReservationCancellationQuoteSet>%s</capacityReservationCancellationQuoteSet>", items.String()))
}

// ec2CapacityBlockOffering describes one entry of the deterministic Capacity
// Block offering catalog.
type ec2CapacityBlockOffering struct {
	OfferingId       string
	InstanceType     string
	AvailabilityZone string
	InstanceCount    int
	StartDate        string
	EndDate          string
	DurationHours    int
	UpfrontFee       string
}

// ec2CapacityBlockCatalog returns the deterministic Capacity Block offering
// catalog for the requested instance count / duration / start window.
func ec2CapacityBlockCatalog(instanceType string, instanceCount, durationHours int, start time.Time) []ec2CapacityBlockOffering {
	if instanceType == "" {
		instanceType = "p5.48xlarge"
	}
	if instanceCount <= 0 {
		instanceCount = 1
	}
	if durationHours <= 0 {
		durationHours = 24
	}
	az := awsAvailabilityZone()
	var out []ec2CapacityBlockOffering
	// Offer two start dates within the requested window.
	for d := 0; d < 2; d++ {
		s := start.AddDate(0, 0, d)
		e := s.Add(time.Duration(durationHours) * time.Hour)
		fee := float64(instanceCount) * float64(durationHours) * 30.0
		out = append(out, ec2CapacityBlockOffering{
			OfferingId:       fmt.Sprintf("cbo-%s-%dh-%dn-%d-aaaaaaaaaaa", instanceType, durationHours, instanceCount, d),
			InstanceType:     instanceType,
			AvailabilityZone: az,
			InstanceCount:    instanceCount,
			StartDate:        s.Format("2006-01-02T15:04:05.000Z"),
			EndDate:          e.Format("2006-01-02T15:04:05.000Z"),
			DurationHours:    durationHours,
			UpfrontFee:       strconv.FormatFloat(fee, 'f', 2, 64),
		})
	}
	return out
}

func ec2FindCapacityBlockOffering(id string) (ec2CapacityBlockOffering, bool) {
	// The offering id encodes the instance type, duration, count, and index;
	// reparse it back into the catalog terms. Format:
	// cbo-<type>-<dur>h-<count>n-<idx>-aaaaaaaaaaa.
	parts := strings.SplitN(id, "-", 2)
	if len(parts) != 2 || parts[0] != "cbo" {
		return ec2CapacityBlockOffering{}, false
	}
	rest := strings.TrimSuffix(parts[1], "-aaaaaaaaaaa")
	segs := strings.Split(rest, "-")
	if len(segs) < 4 {
		return ec2CapacityBlockOffering{}, false
	}
	idx := segs[len(segs)-1]
	countStr := strings.TrimSuffix(segs[len(segs)-2], "n")
	durStr := strings.TrimSuffix(segs[len(segs)-3], "h")
	instanceType := strings.Join(segs[:len(segs)-3], "-")
	dur, _ := strconv.Atoi(durStr)
	count, _ := strconv.Atoi(countStr)
	for _, o := range ec2CapacityBlockCatalog(instanceType, count, dur, time.Now().UTC()) {
		if strings.HasSuffix(o.OfferingId, "-"+idx+"-aaaaaaaaaaa") {
			return o, true
		}
	}
	return ec2CapacityBlockOffering{}, false
}

func handleDescribeCapacityBlockOfferings(w http.ResponseWriter, r *http.Request) {
	instanceType := r.FormValue("InstanceType")
	instanceCount := ec2AtoiOr(r.FormValue("InstanceCount"), 1)
	durationHours := ec2AtoiOr(r.FormValue("CapacityDurationHours"), 24)
	start := time.Now().UTC().AddDate(0, 0, 1)
	if v := r.FormValue("StartDateRange"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			start = t.UTC()
		}
	}
	offerings := ec2CapacityBlockCatalog(instanceType, instanceCount, durationHours, start)
	var items strings.Builder
	for _, o := range offerings {
		fmt.Fprintf(&items, "<item><capacityBlockOfferingId>%s</capacityBlockOfferingId><instanceType>%s</instanceType><availabilityZone>%s</availabilityZone><instanceCount>%d</instanceCount><startDate>%s</startDate><endDate>%s</endDate><capacityBlockDurationHours>%d</capacityBlockDurationHours><upfrontFee>%s</upfrontFee><currencyCode>USD</currencyCode><tenancy>default</tenancy></item>",
			o.OfferingId, o.InstanceType, o.AvailabilityZone, o.InstanceCount, o.StartDate, o.EndDate, o.DurationHours, o.UpfrontFee)
	}
	ec2WriteResponse(w, "DescribeCapacityBlockOfferings",
		fmt.Sprintf("<capacityBlockOfferingSet>%s</capacityBlockOfferingSet>", items.String()))
}

func ec2CapacityBlockFieldsXML(cb EC2CapacityBlock) string {
	var crIDs strings.Builder
	if cb.CapacityReservationId != "" {
		fmt.Fprintf(&crIDs, "<item>%s</item>", cb.CapacityReservationId)
	}
	return fmt.Sprintf("<capacityBlockId>%s</capacityBlockId><availabilityZone>%s</availabilityZone><capacityReservationIdSet>%s</capacityReservationIdSet><startDate>%s</startDate><endDate>%s</endDate><createDate>%s</createDate><state>%s</state>%s",
		cb.CapacityBlockId, cb.AvailabilityZone, crIDs.String(), cb.StartDate, cb.EndDate, cb.CreateDate, cb.State, writeTagSetXML(cb.Tags))
}

func handlePurchaseCapacityBlock(w http.ResponseWriter, r *http.Request) {
	offeringID := r.FormValue("CapacityBlockOfferingId")
	offering, ok := ec2FindCapacityBlockOffering(offeringID)
	if !ok {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("The Capacity Block offering ID %q does not exist", offeringID), http.StatusBadRequest)
		return
	}
	platform := r.FormValue("InstancePlatform")
	if platform == "" {
		platform = "Linux/UNIX"
	}
	tags := ec2ParseTagSpecs(r)
	now := ec2NowMilli()
	// A Capacity Block is backed by a capacity reservation.
	cr := EC2CapacityReservation{
		CapacityReservationId:  ec2ID("cr"),
		OwnerId:                ec2Owner(),
		InstanceType:           offering.InstanceType,
		InstancePlatform:       platform,
		AvailabilityZone:       offering.AvailabilityZone,
		Tenancy:                "default",
		TotalInstanceCount:     offering.InstanceCount,
		AvailableInstanceCount: offering.InstanceCount,
		State:                  "payment-pending",
		StartDate:              offering.StartDate,
		EndDate:                offering.EndDate,
		EndDateType:            "limited",
		InstanceMatchCriteria:  "targeted",
		CreateDate:             now,
		Tags:                   tags,
	}
	ec2CapacityReservations.Put(cr.CapacityReservationId, cr)
	cb := EC2CapacityBlock{
		CapacityBlockId:       ec2ID("cb"),
		AvailabilityZone:      offering.AvailabilityZone,
		CapacityReservationId: cr.CapacityReservationId,
		StartDate:             offering.StartDate,
		EndDate:               offering.EndDate,
		CreateDate:            now,
		State:                 "payment-pending",
		InstanceType:          offering.InstanceType,
		InstanceCount:         offering.InstanceCount,
		Tags:                  tags,
	}
	ec2CapacityBlocks.Put(cb.CapacityBlockId, cb)
	ec2WriteResponse(w, "PurchaseCapacityBlock",
		fmt.Sprintf("<capacityReservation>%s</capacityReservation><capacityBlockSet><item>%s</item></capacityBlockSet>",
			ec2CapReservationFieldsXML(cr), ec2CapacityBlockFieldsXML(cb)))
}

func handleDescribeCapacityBlocks(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "CapacityBlockId")
	results := make([]EC2CapacityBlock, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			cb, ok := ec2CapacityBlocks.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("The Capacity Block ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, cb)
		}
	} else {
		results = append(results, ec2CapacityBlocks.List()...)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].CapacityBlockId < results[j].CapacityBlockId })
	var items strings.Builder
	for _, cb := range results {
		items.WriteString("<item>")
		items.WriteString(ec2CapacityBlockFieldsXML(cb))
		items.WriteString("</item>")
	}
	ec2WriteResponse(w, "DescribeCapacityBlocks",
		fmt.Sprintf("<capacityBlockSet>%s</capacityBlockSet>", items.String()))
}

func handleDescribeCapacityBlockStatus(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "CapacityBlockId")
	results := make([]EC2CapacityBlock, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			cb, ok := ec2CapacityBlocks.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("The Capacity Block ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, cb)
		}
	} else {
		results = append(results, ec2CapacityBlocks.List()...)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].CapacityBlockId < results[j].CapacityBlockId })
	var items strings.Builder
	for _, cb := range results {
		crStatus := ""
		if cb.CapacityReservationId != "" {
			crStatus = fmt.Sprintf("<capacityReservationStatusSet><item><capacityReservationId>%s</capacityReservationId><totalCapacity>%d</totalCapacity><totalAvailableCapacity>%d</totalAvailableCapacity><totalUnavailableCapacity>0</totalUnavailableCapacity></item></capacityReservationStatusSet>",
				cb.CapacityReservationId, cb.InstanceCount, cb.InstanceCount)
		}
		fmt.Fprintf(&items, "<item><capacityBlockId>%s</capacityBlockId><interconnectStatus>ok</interconnectStatus><totalCapacity>%d</totalCapacity><totalAvailableCapacity>%d</totalAvailableCapacity><totalUnavailableCapacity>0</totalUnavailableCapacity>%s</item>",
			cb.CapacityBlockId, cb.InstanceCount, cb.InstanceCount, crStatus)
	}
	ec2WriteResponse(w, "DescribeCapacityBlockStatus",
		fmt.Sprintf("<capacityBlockStatusSet>%s</capacityBlockStatusSet>", items.String()))
}

func handleDescribeCapacityBlockExtensionOfferings(w http.ResponseWriter, r *http.Request) {
	crID := r.FormValue("CapacityReservationId")
	durationHours := ec2AtoiOr(r.FormValue("CapacityBlockExtensionDurationHours"), 24)
	instanceType := "p5.48xlarge"
	instanceCount := 1
	az := awsAvailabilityZone()
	extStart := time.Now().UTC().AddDate(0, 0, 1)
	if cb, ok := ec2CapacityBlockByReservation(crID); ok {
		instanceType = cb.InstanceType
		instanceCount = cb.InstanceCount
		az = cb.AvailabilityZone
		if t, err := time.Parse("2006-01-02T15:04:05.000Z", cb.EndDate); err == nil {
			extStart = t
		}
	}
	var items strings.Builder
	for d := 0; d < 2; d++ {
		dur := durationHours * (d + 1)
		extEnd := extStart.Add(time.Duration(dur) * time.Hour)
		fee := float64(instanceCount) * float64(dur) * 30.0
		fmt.Fprintf(&items, "<item><capacityBlockExtensionOfferingId>cbeo-%dh-%d-aaaaaaaaaaa</capacityBlockExtensionOfferingId><instanceType>%s</instanceType><instanceCount>%d</instanceCount><availabilityZone>%s</availabilityZone><capacityBlockExtensionStartDate>%s</capacityBlockExtensionStartDate><capacityBlockExtensionEndDate>%s</capacityBlockExtensionEndDate><capacityBlockExtensionDurationHours>%d</capacityBlockExtensionDurationHours><upfrontFee>%s</upfrontFee><currencyCode>USD</currencyCode><tenancy>default</tenancy></item>",
			dur, d, instanceType, instanceCount, az,
			extStart.Format("2006-01-02T15:04:05.000Z"), extEnd.Format("2006-01-02T15:04:05.000Z"),
			dur, strconv.FormatFloat(fee, 'f', 2, 64))
	}
	ec2WriteResponse(w, "DescribeCapacityBlockExtensionOfferings",
		fmt.Sprintf("<capacityBlockExtensionOfferingSet>%s</capacityBlockExtensionOfferingSet>", items.String()))
}

func ec2CapacityBlockByReservation(crID string) (EC2CapacityBlock, bool) {
	for _, cb := range ec2CapacityBlocks.List() {
		if cb.CapacityReservationId == crID {
			return cb, true
		}
	}
	return EC2CapacityBlock{}, false
}

func ec2CapacityBlockExtensionFieldsXML(ext EC2CapacityBlockExtension) string {
	return fmt.Sprintf("<capacityReservationId>%s</capacityReservationId><instanceType>%s</instanceType><instanceCount>%d</instanceCount><availabilityZone>%s</availabilityZone><capacityBlockExtensionOfferingId>%s</capacityBlockExtensionOfferingId><capacityBlockExtensionDurationHours>%d</capacityBlockExtensionDurationHours><capacityBlockExtensionStatus>%s</capacityBlockExtensionStatus><capacityBlockExtensionPurchaseDate>%s</capacityBlockExtensionPurchaseDate><capacityBlockExtensionStartDate>%s</capacityBlockExtensionStartDate><capacityBlockExtensionEndDate>%s</capacityBlockExtensionEndDate><upfrontFee>%s</upfrontFee><currencyCode>%s</currencyCode>",
		ext.CapacityReservationId, ext.InstanceType, ext.InstanceCount, ext.AvailabilityZone,
		ext.CapacityBlockExtensionOfferingId, ext.CapacityBlockExtensionDurationHours,
		ext.CapacityBlockExtensionStatus, ext.CapacityBlockExtensionPurchaseDate,
		ext.CapacityBlockExtensionStartDate, ext.CapacityBlockExtensionEndDate,
		ext.UpfrontFee, ext.CurrencyCode)
}

func handlePurchaseCapacityBlockExtension(w http.ResponseWriter, r *http.Request) {
	crID := r.FormValue("CapacityReservationId")
	cb, ok := ec2CapacityBlockByReservation(crID)
	if !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("No Capacity Block found for Capacity Reservation %q", crID), http.StatusBadRequest)
		return
	}
	offeringID := r.FormValue("CapacityBlockExtensionOfferingId")
	durationHours := 24
	if rest := strings.TrimPrefix(offeringID, "cbeo-"); rest != offeringID {
		if h := strings.SplitN(rest, "h-", 2); len(h) == 2 {
			durationHours = ec2AtoiOr(h[0], 24)
		}
	}
	now := time.Now().UTC()
	extStart := now
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", cb.EndDate); err == nil {
		extStart = t
	}
	extEnd := extStart.Add(time.Duration(durationHours) * time.Hour)
	fee := float64(cb.InstanceCount) * float64(durationHours) * 30.0
	ext := EC2CapacityBlockExtension{
		CapacityReservationId:               crID,
		InstanceType:                        cb.InstanceType,
		InstanceCount:                       cb.InstanceCount,
		AvailabilityZone:                    cb.AvailabilityZone,
		CapacityBlockExtensionOfferingId:    offeringID,
		CapacityBlockExtensionDurationHours: durationHours,
		CapacityBlockExtensionStatus:        "payment-succeeded",
		CapacityBlockExtensionPurchaseDate:  now.Format("2006-01-02T15:04:05.000Z"),
		CapacityBlockExtensionStartDate:     extStart.Format("2006-01-02T15:04:05.000Z"),
		CapacityBlockExtensionEndDate:       extEnd.Format("2006-01-02T15:04:05.000Z"),
		UpfrontFee:                          strconv.FormatFloat(fee, 'f', 2, 64),
		CurrencyCode:                        "USD",
	}
	ec2CapacityBlockExtensions.Put(crID, ext)
	// Extend the underlying capacity block + reservation end date.
	cb.EndDate = ext.CapacityBlockExtensionEndDate
	ec2CapacityBlocks.Put(cb.CapacityBlockId, cb)
	if cr, ok := ec2CapacityReservations.Get(crID); ok {
		cr.EndDate = ext.CapacityBlockExtensionEndDate
		ec2CapacityReservations.Put(crID, cr)
	}
	ec2WriteResponse(w, "PurchaseCapacityBlockExtension",
		fmt.Sprintf("<capacityBlockExtensionSet><item>%s</item></capacityBlockExtensionSet>", ec2CapacityBlockExtensionFieldsXML(ext)))
}

func handleDescribeCapacityBlockExtensionHistory(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "CapacityReservationId")
	results := make([]EC2CapacityBlockExtension, 0)
	for _, ext := range ec2CapacityBlockExtensions.List() {
		if len(ids) > 0 && !ec2StrInValues(ext.CapacityReservationId, ids) {
			continue
		}
		results = append(results, ext)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].CapacityReservationId < results[j].CapacityReservationId
	})
	var items strings.Builder
	for _, ext := range results {
		items.WriteString("<item>")
		items.WriteString(ec2CapacityBlockExtensionFieldsXML(ext))
		items.WriteString("</item>")
	}
	ec2WriteResponse(w, "DescribeCapacityBlockExtensionHistory",
		fmt.Sprintf("<capacityBlockExtensionSet>%s</capacityBlockExtensionSet>", items.String()))
}
