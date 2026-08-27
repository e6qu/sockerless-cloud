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

// This file implements the EC2 Capacity Reservations, EC2 Fleets, and Spot
// families (instance requests, fleets, datafeed, scheduled instances, dedicated
// host reservations). Each stateful resource is backed by a real sim.Store and
// settles into the state the real EC2 service would return: a capacity
// reservation becomes "active" with an AvailableInstanceCount, an EC2 fleet
// settles "active" against its target capacity, a spot instance request settles
// "active" / "fulfilled" with a spot price, etc. The read-only price/score/
// availability/offering ops return real-shaped (small) results.

// EC2CapacityReservation reserves N instances of a type in an AZ. State settles
// "active" with AvailableInstanceCount == TotalInstanceCount until instances are
// launched into it (the sim doesn't bind launches, so available stays at total).
type EC2CapacityReservation struct {
	CapacityReservationId      string
	OwnerId                    string
	InstanceType               string
	InstancePlatform           string
	AvailabilityZone           string
	Tenancy                    string
	TotalInstanceCount         int
	AvailableInstanceCount     int
	EbsOptimized               bool
	EphemeralStorage           bool
	State                      string
	StartDate                  string
	EndDate                    string
	EndDateType                string
	InstanceMatchCriteria      string
	CreateDate                 string
	CapacityReservationFleetId string
	Tags                       []EC2Tag
}

// EC2CapacityReservationFleet aggregates capacity reservations toward a target.
type EC2CapacityReservationFleet struct {
	CapacityReservationFleetId string
	State                      string
	TotalTargetCapacity        int
	TotalFulfilledCapacity     float64
	InstanceMatchCriteria      string
	AllocationStrategy         string
	CreateTime                 string
	EndDate                    string
	Tenancy                    string
	Members                    []EC2FleetCapacityReservationMember
	Tags                       []EC2Tag
}

// EC2FleetCapacityReservationMember is one (instance-type, weight, priority)
// reservation inside a capacity reservation fleet.
type EC2FleetCapacityReservationMember struct {
	CapacityReservationId string
	AvailabilityZone      string
	InstanceType          string
	InstancePlatform      string
	TotalInstanceCount    int
	FulfilledCapacity     float64
	EbsOptimized          bool
	CreateDate            string
	Weight                float64
	Priority              int
}

// EC2Fleet requests target on-demand+spot capacity from launch templates.
type EC2Fleet struct {
	FleetId                          string
	FleetState                       string
	ActivityStatus                   string
	CreateTime                       string
	ClientToken                      string
	ExcessCapacityTerminationPolicy  string
	FulfilledCapacity                float64
	FulfilledOnDemandCapacity        float64
	TotalTargetCapacity              int
	OnDemandTargetCapacity           int
	SpotTargetCapacity               int
	DefaultTargetCapacityType        string
	TerminateInstancesWithExpiration bool
	Type                             string
	ValidFrom                        string
	ValidUntil                       string
	ReplaceUnhealthyInstances        bool
	LaunchTemplateId                 string
	LaunchTemplateName               string
	LaunchTemplateVersion            string
	InstanceType                     string
	InstanceIds                      []string
	Tags                             []EC2Tag
}

// EC2SpotInstanceRequest is a one-off spot request. It settles "active" /
// "fulfilled" with a backing instance and the prevailing spot price.
type EC2SpotInstanceRequest struct {
	SpotInstanceRequestId        string
	SpotPrice                    string
	Type                         string
	State                        string
	StatusCode                   string
	StatusMessage                string
	StatusUpdateTime             string
	InstanceId                   string
	ProductDescription           string
	LaunchedAvailabilityZone     string
	InstanceInterruptionBehavior string
	ValidFrom                    string
	ValidUntil                   string
	CreateTime                   string
	ImageId                      string
	LaunchInstanceType           string
	Tags                         []EC2Tag
}

// EC2SpotFleetRequest is a spot fleet (sfr-). It settles "active" / "fulfilled"
// against its target capacity from the request config.
type EC2SpotFleetRequest struct {
	SpotFleetRequestId           string
	SpotFleetRequestState        string
	ActivityStatus               string
	CreateTime                   string
	IamFleetRole                 string
	AllocationStrategy           string
	TargetCapacity               int
	OnDemandTargetCapacity       int
	FulfilledCapacity            float64
	SpotPrice                    string
	Type                         string
	ValidFrom                    string
	ValidUntil                   string
	TerminateInstancesWithExp    bool
	InstanceInterruptionBehavior string
	LaunchInstanceType           string
	LaunchImageId                string
	InstanceIds                  []string
	Tags                         []EC2Tag
}

// EC2SpotDatafeedSubscription is the account-wide spot data feed (singleton).
type EC2SpotDatafeedSubscription struct {
	Bucket  string
	Prefix  string
	OwnerId string
	State   string
}

// EC2ScheduledInstance is a purchased Scheduled Reserved Instance.
type EC2ScheduledInstance struct {
	ScheduledInstanceId         string
	AvailabilityZone            string
	InstanceType                string
	Platform                    string
	NetworkPlatform             string
	InstanceCount               int
	SlotDurationInHours         int
	HourlyPrice                 string
	TotalScheduledInstanceHours int
	CreateDate                  string
	TermStartDate               string
	TermEndDate                 string
	NextSlotStartTime           string
	RecurrenceFrequency         string
	RecurrenceInterval          int
}

// EC2HostReservation is a purchased Dedicated Host Reservation.
type EC2HostReservation struct {
	HostReservationId string
	OfferingId        string
	InstanceFamily    string
	HostIdSet         []string
	Count             int
	State             string
	PaymentOption     string
	HourlyPrice       string
	UpfrontPrice      string
	CurrencyCode      string
	Duration          int
	Start             string
	End               string
	Tags              []EC2Tag
}

var (
	ec2CapacityReservations      sim.Store[EC2CapacityReservation]
	ec2CapacityReservationFleets sim.Store[EC2CapacityReservationFleet]
	ec2Fleets                    sim.Store[EC2Fleet]
	ec2SpotInstanceRequests      sim.Store[EC2SpotInstanceRequest]
	ec2SpotFleetRequests         sim.Store[EC2SpotFleetRequest]
	ec2SpotDatafeed              sim.Store[EC2SpotDatafeedSubscription]
	ec2ScheduledInstances        sim.Store[EC2ScheduledInstance]
	ec2HostReservations          sim.Store[EC2HostReservation]
)

func registerEC2CapacityFleet(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2CapacityReservations = sim.MakeStore[EC2CapacityReservation](srv.DB(), "ec2_capacity_reservations")
	ec2CapacityReservationFleets = sim.MakeStore[EC2CapacityReservationFleet](srv.DB(), "ec2_capacity_reservation_fleets")
	ec2Fleets = sim.MakeStore[EC2Fleet](srv.DB(), "ec2_fleets")
	ec2SpotInstanceRequests = sim.MakeStore[EC2SpotInstanceRequest](srv.DB(), "ec2_spot_instance_requests")
	ec2SpotFleetRequests = sim.MakeStore[EC2SpotFleetRequest](srv.DB(), "ec2_spot_fleet_requests")
	ec2SpotDatafeed = sim.MakeStore[EC2SpotDatafeedSubscription](srv.DB(), "ec2_spot_datafeed")
	ec2ScheduledInstances = sim.MakeStore[EC2ScheduledInstance](srv.DB(), "ec2_scheduled_instances")
	ec2HostReservations = sim.MakeStore[EC2HostReservation](srv.DB(), "ec2_host_reservations")

	// Capacity Reservations
	r.Register("CreateCapacityReservation", handleCreateCapacityReservation)
	r.Register("DescribeCapacityReservations", handleDescribeCapacityReservations)
	r.Register("ModifyCapacityReservation", handleModifyCapacityReservation)
	r.Register("CancelCapacityReservation", handleCancelCapacityReservation)
	r.Register("GetCapacityReservationUsage", handleGetCapacityReservationUsage)
	r.Register("GetGroupsForCapacityReservation", handleGetGroupsForCapacityReservation)

	// Capacity Reservation Fleets
	r.Register("CreateCapacityReservationFleet", handleCreateCapacityReservationFleet)
	r.Register("DescribeCapacityReservationFleets", handleDescribeCapacityReservationFleets)
	r.Register("ModifyCapacityReservationFleet", handleModifyCapacityReservationFleet)
	r.Register("CancelCapacityReservationFleets", handleCancelCapacityReservationFleets)

	// EC2 Fleets
	r.Register("CreateFleet", handleCreateFleet)
	r.Register("DescribeFleets", handleDescribeFleets)
	r.Register("DescribeFleetInstances", handleDescribeFleetInstances)
	r.Register("DescribeFleetHistory", handleDescribeFleetHistory)
	r.Register("ModifyFleet", handleModifyFleet)
	r.Register("DeleteFleets", handleDeleteFleets)

	// Spot instance requests
	r.Register("RequestSpotInstances", handleRequestSpotInstances)
	r.Register("DescribeSpotInstanceRequests", handleDescribeSpotInstanceRequests)
	r.Register("CancelSpotInstanceRequests", handleCancelSpotInstanceRequests)

	// Spot fleets
	r.Register("RequestSpotFleet", handleRequestSpotFleet)
	r.Register("DescribeSpotFleetRequests", handleDescribeSpotFleetRequests)
	r.Register("DescribeSpotFleetInstances", handleDescribeSpotFleetInstances)
	r.Register("DescribeSpotFleetRequestHistory", handleDescribeSpotFleetRequestHistory)
	r.Register("ModifySpotFleetRequest", handleModifySpotFleetRequest)
	r.Register("CancelSpotFleetRequests", handleCancelSpotFleetRequests)

	// Spot datafeed
	r.Register("CreateSpotDatafeedSubscription", handleCreateSpotDatafeedSubscription)
	r.Register("DescribeSpotDatafeedSubscription", handleDescribeSpotDatafeedSubscription)
	r.Register("DeleteSpotDatafeedSubscription", handleDeleteSpotDatafeedSubscription)

	// Spot read-only
	r.Register("DescribeSpotPriceHistory", handleDescribeSpotPriceHistory)
	r.Register("GetSpotPlacementScores", handleGetSpotPlacementScores)

	// Scheduled instances
	r.Register("DescribeScheduledInstances", handleDescribeScheduledInstances)
	r.Register("DescribeScheduledInstanceAvailability", handleDescribeScheduledInstanceAvailability)
	r.Register("PurchaseScheduledInstances", handlePurchaseScheduledInstances)
	r.Register("RunScheduledInstances", handleRunScheduledInstances)

	// Dedicated host reservations
	r.Register("DescribeHostReservations", handleDescribeHostReservations)
	r.Register("DescribeHostReservationOfferings", handleDescribeHostReservationOfferings)
	r.Register("GetHostReservationPurchasePreview", handleGetHostReservationPurchasePreview)
	r.Register("PurchaseHostReservation", handlePurchaseHostReservation)
}

func ec2NowMilli() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func ec2FloatStr(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// ec2ParseTagSpecs reads tags from a request, accepting both the EC2 wire forms
// the aws-sdk-go-v2 query serializer emits: RunInstances-style
// "TagSpecification.N.Tag.M.{Key,Value}" (handled by the shared parseTags) and
// the "TagSpecifications.N.Tag.M.{Key,Value}" form the Capacity Reservation /
// Fleet / Spot operations serialize (object.FlatKey("TagSpecifications")). It
// returns the union across all TagSpecification entries.
func ec2ParseTagSpecs(r *http.Request) []EC2Tag {
	if tags := parseTags(r); len(tags) > 0 {
		return tags
	}
	var tags []EC2Tag
	for i := 1; ; i++ {
		// A TagSpecification entry is present iff it carries a ResourceType or a
		// first tag key.
		base := fmt.Sprintf("TagSpecifications.%d", i)
		if r.FormValue(base+".ResourceType") == "" && r.FormValue(base+".Tag.1.Key") == "" {
			break
		}
		for j := 1; ; j++ {
			key := r.FormValue(fmt.Sprintf("%s.Tag.%d.Key", base, j))
			if key == "" {
				break
			}
			tags = append(tags, EC2Tag{Key: key, Value: r.FormValue(fmt.Sprintf("%s.Tag.%d.Value", base, j))})
		}
	}
	return tags
}

func ec2CapReservationArn(id string) string {
	return fmt.Sprintf("arn:aws:ec2:%s:%s:capacity-reservation/%s", awsRegion(), ec2Owner(), id)
}

func ec2CapReservationFleetArn(id string) string {
	return fmt.Sprintf("arn:aws:ec2:%s:%s:capacity-reservation-fleet/%s", awsRegion(), ec2Owner(), id)
}

func handleCreateCapacityReservation(w http.ResponseWriter, r *http.Request) {
	instanceType := r.FormValue("InstanceType")
	if instanceType == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter InstanceType", http.StatusBadRequest)
		return
	}
	platform := r.FormValue("InstancePlatform")
	if platform == "" {
		platform = "Linux/UNIX"
	}
	az := r.FormValue("AvailabilityZone")
	if az == "" {
		az = awsAvailabilityZone()
	}
	tenancy := r.FormValue("Tenancy")
	if tenancy == "" {
		tenancy = "default"
	}
	count := ec2AtoiOr(r.FormValue("InstanceCount"), 1)
	matchCriteria := r.FormValue("InstanceMatchCriteria")
	if matchCriteria == "" {
		matchCriteria = "open"
	}
	endDateType := r.FormValue("EndDateType")
	if endDateType == "" {
		endDateType = "unlimited"
	}
	cr := EC2CapacityReservation{
		CapacityReservationId:  ec2ID("cr"),
		OwnerId:                ec2Owner(),
		InstanceType:           instanceType,
		InstancePlatform:       platform,
		AvailabilityZone:       az,
		Tenancy:                tenancy,
		TotalInstanceCount:     count,
		AvailableInstanceCount: count,
		EbsOptimized:           r.FormValue("EbsOptimized") == "true",
		EphemeralStorage:       r.FormValue("EphemeralStorage") == "true",
		State:                  "active",
		StartDate:              ec2NowMilli(),
		EndDate:                r.FormValue("EndDate"),
		EndDateType:            endDateType,
		InstanceMatchCriteria:  matchCriteria,
		CreateDate:             ec2NowMilli(),
		Tags:                   ec2ParseTagSpecs(r),
	}
	ec2CapacityReservations.Put(cr.CapacityReservationId, cr)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateCapacityReservationResponse %s><requestId>%s</requestId><capacityReservation>%s</capacityReservation></CreateCapacityReservationResponse>`,
		ec2Xmlns(), generateUUID(), ec2CapReservationFieldsXML(cr))
}

func ec2CapReservationFieldsXML(cr EC2CapacityReservation) string {
	endDate := ""
	if cr.EndDate != "" {
		endDate = fmt.Sprintf("<endDate>%s</endDate>", cr.EndDate)
	}
	fleet := ""
	if cr.CapacityReservationFleetId != "" {
		fleet = fmt.Sprintf("<capacityReservationFleetId>%s</capacityReservationFleetId>", cr.CapacityReservationFleetId)
	}
	return fmt.Sprintf("<capacityReservationId>%s</capacityReservationId><ownerId>%s</ownerId><capacityReservationArn>%s</capacityReservationArn><instanceType>%s</instanceType><instancePlatform>%s</instancePlatform><availabilityZone>%s</availabilityZone><tenancy>%s</tenancy><totalInstanceCount>%d</totalInstanceCount><availableInstanceCount>%d</availableInstanceCount><ebsOptimized>%t</ebsOptimized><ephemeralStorage>%t</ephemeralStorage><state>%s</state><startDate>%s</startDate>%s<endDateType>%s</endDateType><instanceMatchCriteria>%s</instanceMatchCriteria><createDate>%s</createDate>%s%s",
		cr.CapacityReservationId, cr.OwnerId, ec2CapReservationArn(cr.CapacityReservationId),
		cr.InstanceType, cr.InstancePlatform, cr.AvailabilityZone, cr.Tenancy,
		cr.TotalInstanceCount, cr.AvailableInstanceCount, cr.EbsOptimized, cr.EphemeralStorage,
		cr.State, cr.StartDate, endDate, cr.EndDateType, cr.InstanceMatchCriteria, cr.CreateDate,
		fleet, writeTagSetXML(cr.Tags))
}

func handleDescribeCapacityReservations(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "CapacityReservationId")
	filters := ec2Filters(r)
	results := make([]EC2CapacityReservation, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			cr, ok := ec2CapacityReservations.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("The Capacity Reservation ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, cr)
		}
	} else {
		for _, cr := range ec2CapacityReservations.List() {
			if !ec2CapReservationMatchesFilters(cr, filters) {
				continue
			}
			results = append(results, cr)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].CapacityReservationId < results[j].CapacityReservationId })
	var items strings.Builder
	for _, cr := range results {
		items.WriteString("<item>")
		items.WriteString(ec2CapReservationFieldsXML(cr))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeCapacityReservationsResponse %s><requestId>%s</requestId><capacityReservationSet>%s</capacityReservationSet></DescribeCapacityReservationsResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func ec2CapReservationMatchesFilters(cr EC2CapacityReservation, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "instance-type":
			if !ec2StrInValues(cr.InstanceType, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(cr.State, vals) {
				return false
			}
		case "availability-zone":
			if !ec2StrInValues(cr.AvailabilityZone, vals) {
				return false
			}
		case "tenancy":
			if !ec2StrInValues(cr.Tenancy, vals) {
				return false
			}
		case "instance-platform":
			if !ec2StrInValues(cr.InstancePlatform, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, cr.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleModifyCapacityReservation(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CapacityReservationId")
	cr, ok := ec2CapacityReservations.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("The Capacity Reservation ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("InstanceCount"); v != "" {
		n := ec2AtoiOr(v, cr.TotalInstanceCount)
		cr.TotalInstanceCount = n
		cr.AvailableInstanceCount = n
	}
	if v := r.FormValue("EndDateType"); v != "" {
		cr.EndDateType = v
	}
	if v := r.FormValue("EndDate"); v != "" {
		cr.EndDate = v
	}
	ec2CapacityReservations.Put(id, cr)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyCapacityReservationResponse %s><requestId>%s</requestId><return>true</return></ModifyCapacityReservationResponse>`, ec2Xmlns(), generateUUID())
}

func handleCancelCapacityReservation(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CapacityReservationId")
	cr, ok := ec2CapacityReservations.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("The Capacity Reservation ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	cr.State = "cancelled"
	cr.AvailableInstanceCount = 0
	ec2CapacityReservations.Put(id, cr)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CancelCapacityReservationResponse %s><requestId>%s</requestId><return>true</return></CancelCapacityReservationResponse>`, ec2Xmlns(), generateUUID())
}

func handleGetCapacityReservationUsage(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CapacityReservationId")
	cr, ok := ec2CapacityReservations.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("The Capacity Reservation ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	used := cr.TotalInstanceCount - cr.AvailableInstanceCount
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetCapacityReservationUsageResponse %s><requestId>%s</requestId><capacityReservationId>%s</capacityReservationId><instanceType>%s</instanceType><totalInstanceCount>%d</totalInstanceCount><availableInstanceCount>%d</availableInstanceCount><state>%s</state><instanceUsageSet><item><accountId>%s</accountId><usedInstanceCount>%d</usedInstanceCount></item></instanceUsageSet></GetCapacityReservationUsageResponse>`,
		ec2Xmlns(), generateUUID(), cr.CapacityReservationId, cr.InstanceType, cr.TotalInstanceCount, cr.AvailableInstanceCount, cr.State, ec2Owner(), used)
}

func handleGetGroupsForCapacityReservation(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CapacityReservationId")
	if _, ok := ec2CapacityReservations.Get(id); !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("The Capacity Reservation ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	// The reservation is not in any resource group; return an empty set.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetGroupsForCapacityReservationResponse %s><requestId>%s</requestId><capacityReservationGroupSet/></GetGroupsForCapacityReservationResponse>`,
		ec2Xmlns(), generateUUID())
}

// ec2ParseFleetCapacityReservations reads the
// InstanceTypeSpecification.N.{InstanceType,AvailabilityZone,Weight,Priority,...}
// request params for CreateCapacityReservationFleet.
func ec2ParseFleetCapacityReservations(r *http.Request) []EC2FleetCapacityReservationMember {
	var members []EC2FleetCapacityReservationMember
	for i := 1; ; i++ {
		it := r.FormValue(fmt.Sprintf("InstanceTypeSpecification.%d.InstanceType", i))
		if it == "" {
			break
		}
		az := r.FormValue(fmt.Sprintf("InstanceTypeSpecification.%d.AvailabilityZone", i))
		if az == "" {
			az = awsAvailabilityZone()
		}
		platform := r.FormValue(fmt.Sprintf("InstanceTypeSpecification.%d.InstancePlatform", i))
		if platform == "" {
			platform = "Linux/UNIX"
		}
		weight := 1.0
		if v := r.FormValue(fmt.Sprintf("InstanceTypeSpecification.%d.Weight", i)); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				weight = f
			}
		}
		members = append(members, EC2FleetCapacityReservationMember{
			CapacityReservationId: ec2ID("cr"),
			AvailabilityZone:      az,
			InstanceType:          it,
			InstancePlatform:      platform,
			TotalInstanceCount:    0,
			FulfilledCapacity:     0,
			EbsOptimized:          r.FormValue(fmt.Sprintf("InstanceTypeSpecification.%d.EbsOptimized", i)) == "true",
			CreateDate:            ec2NowMilli(),
			Weight:                weight,
			Priority:              ec2AtoiOr(r.FormValue(fmt.Sprintf("InstanceTypeSpecification.%d.Priority", i)), 0),
		})
	}
	return members
}

func handleCreateCapacityReservationFleet(w http.ResponseWriter, r *http.Request) {
	target := ec2AtoiOr(r.FormValue("TotalTargetCapacity"), 0)
	if target <= 0 {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter TotalTargetCapacity", http.StatusBadRequest)
		return
	}
	matchCriteria := r.FormValue("InstanceMatchCriteria")
	if matchCriteria == "" {
		matchCriteria = "open"
	}
	allocStrategy := r.FormValue("AllocationStrategy")
	if allocStrategy == "" {
		allocStrategy = "prioritized"
	}
	tenancy := r.FormValue("Tenancy")
	if tenancy == "" {
		tenancy = "default"
	}
	members := ec2ParseFleetCapacityReservations(r)
	// Distribute the target capacity across the members and create the backing
	// capacity reservations, mirroring how a real fleet fulfills its target.
	fleetID := ec2ID("crf")
	var fulfilled float64
	for i := range members {
		members[i].TotalInstanceCount = target / max(len(members), 1)
		if i == 0 {
			members[i].TotalInstanceCount += target % max(len(members), 1)
		}
		members[i].FulfilledCapacity = float64(members[i].TotalInstanceCount) * members[i].Weight
		fulfilled += members[i].FulfilledCapacity
		ec2CapacityReservations.Put(members[i].CapacityReservationId, EC2CapacityReservation{
			CapacityReservationId:      members[i].CapacityReservationId,
			OwnerId:                    ec2Owner(),
			InstanceType:               members[i].InstanceType,
			InstancePlatform:           members[i].InstancePlatform,
			AvailabilityZone:           members[i].AvailabilityZone,
			Tenancy:                    tenancy,
			TotalInstanceCount:         members[i].TotalInstanceCount,
			AvailableInstanceCount:     members[i].TotalInstanceCount,
			EbsOptimized:               members[i].EbsOptimized,
			State:                      "active",
			StartDate:                  ec2NowMilli(),
			EndDateType:                "unlimited",
			InstanceMatchCriteria:      matchCriteria,
			CreateDate:                 ec2NowMilli(),
			CapacityReservationFleetId: fleetID,
		})
	}
	crf := EC2CapacityReservationFleet{
		CapacityReservationFleetId: fleetID,
		State:                      "active",
		TotalTargetCapacity:        target,
		TotalFulfilledCapacity:     fulfilled,
		InstanceMatchCriteria:      matchCriteria,
		AllocationStrategy:         allocStrategy,
		CreateTime:                 ec2NowMilli(),
		EndDate:                    r.FormValue("EndDate"),
		Tenancy:                    tenancy,
		Members:                    members,
		Tags:                       ec2ParseTagSpecs(r),
	}
	ec2CapacityReservationFleets.Put(fleetID, crf)
	var memberXML strings.Builder
	for _, m := range members {
		memberXML.WriteString(ec2FleetCapResMemberXML(m))
	}
	endDate := ""
	if crf.EndDate != "" {
		endDate = fmt.Sprintf("<endDate>%s</endDate>", crf.EndDate)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateCapacityReservationFleetResponse %s><requestId>%s</requestId><capacityReservationFleetId>%s</capacityReservationFleetId><state>%s</state><totalTargetCapacity>%d</totalTargetCapacity><totalFulfilledCapacity>%s</totalFulfilledCapacity><instanceMatchCriteria>%s</instanceMatchCriteria><allocationStrategy>%s</allocationStrategy><createTime>%s</createTime>%s<tenancy>%s</tenancy><fleetCapacityReservationSet>%s</fleetCapacityReservationSet>%s</CreateCapacityReservationFleetResponse>`,
		ec2Xmlns(), generateUUID(), crf.CapacityReservationFleetId, crf.State, crf.TotalTargetCapacity,
		ec2FloatStr(crf.TotalFulfilledCapacity), crf.InstanceMatchCriteria, crf.AllocationStrategy,
		crf.CreateTime, endDate, crf.Tenancy, memberXML.String(), writeTagSetXML(crf.Tags))
}

func ec2FleetCapResMemberXML(m EC2FleetCapacityReservationMember) string {
	return fmt.Sprintf("<item><capacityReservationId>%s</capacityReservationId><availabilityZone>%s</availabilityZone><instanceType>%s</instanceType><instancePlatform>%s</instancePlatform><totalInstanceCount>%d</totalInstanceCount><fulfilledCapacity>%s</fulfilledCapacity><ebsOptimized>%t</ebsOptimized><createDate>%s</createDate><weight>%s</weight><priority>%d</priority></item>",
		m.CapacityReservationId, m.AvailabilityZone, m.InstanceType, m.InstancePlatform,
		m.TotalInstanceCount, ec2FloatStr(m.FulfilledCapacity), m.EbsOptimized, m.CreateDate,
		ec2FloatStr(m.Weight), m.Priority)
}

func ec2CapResFleetFieldsXML(crf EC2CapacityReservationFleet) string {
	var memberXML strings.Builder
	for _, m := range crf.Members {
		memberXML.WriteString(ec2FleetCapResMemberXML(m))
	}
	endDate := ""
	if crf.EndDate != "" {
		endDate = fmt.Sprintf("<endDate>%s</endDate>", crf.EndDate)
	}
	return fmt.Sprintf("<capacityReservationFleetId>%s</capacityReservationFleetId><capacityReservationFleetArn>%s</capacityReservationFleetArn><state>%s</state><totalTargetCapacity>%d</totalTargetCapacity><totalFulfilledCapacity>%s</totalFulfilledCapacity><tenancy>%s</tenancy>%s<createTime>%s</createTime><instanceMatchCriteria>%s</instanceMatchCriteria><allocationStrategy>%s</allocationStrategy><instanceTypeSpecificationSet>%s</instanceTypeSpecificationSet>%s",
		crf.CapacityReservationFleetId, ec2CapReservationFleetArn(crf.CapacityReservationFleetId), crf.State,
		crf.TotalTargetCapacity, ec2FloatStr(crf.TotalFulfilledCapacity), crf.Tenancy, endDate, crf.CreateTime,
		crf.InstanceMatchCriteria, crf.AllocationStrategy, memberXML.String(), writeTagSetXML(crf.Tags))
}

func handleDescribeCapacityReservationFleets(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "CapacityReservationFleetId")
	results := make([]EC2CapacityReservationFleet, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			crf, ok := ec2CapacityReservationFleets.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidCapacityReservationFleetId.NotFound", fmt.Sprintf("The Capacity Reservation Fleet ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, crf)
		}
	} else {
		results = append(results, ec2CapacityReservationFleets.List()...)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].CapacityReservationFleetId < results[j].CapacityReservationFleetId
	})
	var items strings.Builder
	for _, crf := range results {
		items.WriteString("<item>")
		items.WriteString(ec2CapResFleetFieldsXML(crf))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeCapacityReservationFleetsResponse %s><requestId>%s</requestId><capacityReservationFleetSet>%s</capacityReservationFleetSet></DescribeCapacityReservationFleetsResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyCapacityReservationFleet(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CapacityReservationFleetId")
	crf, ok := ec2CapacityReservationFleets.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationFleetId.NotFound", fmt.Sprintf("The Capacity Reservation Fleet ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("TotalTargetCapacity"); v != "" {
		crf.TotalTargetCapacity = ec2AtoiOr(v, crf.TotalTargetCapacity)
	}
	if v := r.FormValue("EndDate"); v != "" {
		crf.EndDate = v
	}
	ec2CapacityReservationFleets.Put(id, crf)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyCapacityReservationFleetResponse %s><requestId>%s</requestId><return>true</return></ModifyCapacityReservationFleetResponse>`, ec2Xmlns(), generateUUID())
}

func handleCancelCapacityReservationFleets(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "CapacityReservationFleetId")
	var success, failed strings.Builder
	for _, id := range ids {
		crf, ok := ec2CapacityReservationFleets.Get(id)
		if !ok {
			fmt.Fprintf(&failed, "<item><capacityReservationFleetId>%s</capacityReservationFleetId><cancelCapacityReservationFleetError><code>InvalidCapacityReservationFleetId.NotFound</code><message>The Capacity Reservation Fleet ID %s does not exist</message></cancelCapacityReservationFleetError></item>", id, id)
			continue
		}
		prev := crf.State
		crf.State = "cancelled"
		for i := range crf.Members {
			if cr, ok := ec2CapacityReservations.Get(crf.Members[i].CapacityReservationId); ok {
				cr.State = "cancelled"
				cr.AvailableInstanceCount = 0
				ec2CapacityReservations.Put(cr.CapacityReservationId, cr)
			}
		}
		ec2CapacityReservationFleets.Put(id, crf)
		fmt.Fprintf(&success, "<item><currentFleetState>cancelled</currentFleetState><previousFleetState>%s</previousFleetState><capacityReservationFleetId>%s</capacityReservationFleetId></item>", prev, id)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CancelCapacityReservationFleetsResponse %s><requestId>%s</requestId><successfulFleetCancellationSet>%s</successfulFleetCancellationSet><failedFleetCancellationSet>%s</failedFleetCancellationSet></CancelCapacityReservationFleetsResponse>`,
		ec2Xmlns(), generateUUID(), success.String(), failed.String())
}

func handleCreateFleet(w http.ResponseWriter, r *http.Request) {
	total := ec2AtoiOr(r.FormValue("TargetCapacitySpecification.TotalTargetCapacity"), 0)
	onDemand := ec2AtoiOr(r.FormValue("TargetCapacitySpecification.OnDemandTargetCapacity"), 0)
	spot := ec2AtoiOr(r.FormValue("TargetCapacitySpecification.SpotTargetCapacity"), 0)
	defType := r.FormValue("TargetCapacitySpecification.DefaultTargetCapacityType")
	if defType == "" {
		defType = "on-demand"
	}
	fleetType := r.FormValue("Type")
	if fleetType == "" {
		fleetType = "maintain"
	}
	// Launch template config 1 — the first override drives the synthesized
	// instances (the sim launches one instance per unit of target capacity).
	ltID := r.FormValue("LaunchTemplateConfigs.1.LaunchTemplateSpecification.LaunchTemplateId")
	ltName := r.FormValue("LaunchTemplateConfigs.1.LaunchTemplateSpecification.LaunchTemplateName")
	ltVersion := r.FormValue("LaunchTemplateConfigs.1.LaunchTemplateSpecification.Version")
	instType := r.FormValue("LaunchTemplateConfigs.1.Overrides.1.InstanceType")
	if instType == "" {
		instType = "t3.micro"
	}
	fleet := EC2Fleet{
		FleetId:                          ec2ID("fleet"),
		Type:                             fleetType,
		CreateTime:                       ec2NowMilli(),
		ClientToken:                      r.FormValue("ClientToken"),
		ExcessCapacityTerminationPolicy:  r.FormValue("ExcessCapacityTerminationPolicy"),
		TotalTargetCapacity:              total,
		OnDemandTargetCapacity:           onDemand,
		SpotTargetCapacity:               spot,
		DefaultTargetCapacityType:        defType,
		TerminateInstancesWithExpiration: r.FormValue("TerminateInstancesWithExpiration") == "true",
		ReplaceUnhealthyInstances:        r.FormValue("ReplaceUnhealthyInstances") == "true",
		ValidFrom:                        r.FormValue("ValidFrom"),
		ValidUntil:                       r.FormValue("ValidUntil"),
		LaunchTemplateId:                 ltID,
		LaunchTemplateName:               ltName,
		LaunchTemplateVersion:            ltVersion,
		InstanceType:                     instType,
		Tags:                             ec2ParseTagSpecs(r),
	}
	// A "request"-type fleet is one-shot and returns instance ids; "maintain"
	// and "instant" also synthesize instances. Settle the fleet active and
	// launch real instances into the default subnet.
	for i := 0; i < total; i++ {
		id := ec2LaunchFleetInstance(instType, ltID)
		fleet.InstanceIds = append(fleet.InstanceIds, id)
	}
	fleet.FulfilledCapacity = float64(total)
	if onDemand > 0 {
		fleet.FulfilledOnDemandCapacity = float64(onDemand)
	}
	fleet.FleetState = "active"
	fleet.ActivityStatus = "fulfilled"
	ec2Fleets.Put(fleet.FleetId, fleet)

	w.Header().Set("Content-Type", "text/xml")
	// CreateFleet's response shape depends on Type: "instant" returns the
	// fleetInstanceSet; "request"/"maintain" return only the fleetId.
	if fleetType == "instant" {
		var instXML strings.Builder
		instXML.WriteString("<fleetInstanceSet>")
		instXML.WriteString(ec2CreateFleetInstanceXML(fleet))
		instXML.WriteString("</fleetInstanceSet>")
		fmt.Fprintf(w, `<CreateFleetResponse %s><requestId>%s</requestId><fleetId>%s</fleetId><errorSet/>%s</CreateFleetResponse>`,
			ec2Xmlns(), generateUUID(), fleet.FleetId, instXML.String())
		return
	}
	fmt.Fprintf(w, `<CreateFleetResponse %s><requestId>%s</requestId><fleetId>%s</fleetId></CreateFleetResponse>`,
		ec2Xmlns(), generateUUID(), fleet.FleetId)
}

// ec2LaunchFleetInstance launches one backing instance for a fleet/spot request
// into the default subnet, returning the instance id.
func ec2LaunchFleetInstance(instanceType, _ string) string {
	subnet := defaultVPCSubnetID()
	inst := EC2Instance{
		InstanceId:     ec2ID("i"),
		ImageId:        "ami-12345678",
		InstanceType:   instanceType,
		State:          "running",
		SubnetId:       subnet,
		Architecture:   "x86_64",
		RootDeviceName: "/dev/sda1",
		RootVolumeSize: 8,
		LaunchTime:     ec2NowMilli(),
	}
	if v, ok := ec2Vpcs.Get("vpc-sim"); ok {
		inst.VpcId = v.VpcId
	}
	ec2Instances.Put(inst.InstanceId, inst)
	return inst.InstanceId
}

func ec2CreateFleetInstanceXML(fleet EC2Fleet) string {
	var ids strings.Builder
	ids.WriteString("<instanceIds>")
	for _, id := range fleet.InstanceIds {
		fmt.Fprintf(&ids, "<item>%s</item>", id)
	}
	ids.WriteString("</instanceIds>")
	lifecycle := "on-demand"
	if fleet.DefaultTargetCapacityType == "spot" {
		lifecycle = "spot"
	}
	return fmt.Sprintf("<item><lifecycle>%s</lifecycle>%s<instanceType>%s</instanceType></item>",
		lifecycle, ids.String(), fleet.InstanceType)
}

func ec2FleetFieldsXML(fleet EC2Fleet) string {
	var instSet strings.Builder
	if len(fleet.InstanceIds) > 0 {
		instSet.WriteString("<fleetInstanceSet>")
		instSet.WriteString(ec2CreateFleetInstanceXML(fleet))
		instSet.WriteString("</fleetInstanceSet>")
	}
	validFrom := ""
	if fleet.ValidFrom != "" {
		validFrom = fmt.Sprintf("<validFrom>%s</validFrom>", fleet.ValidFrom)
	}
	validUntil := ""
	if fleet.ValidUntil != "" {
		validUntil = fmt.Sprintf("<validUntil>%s</validUntil>", fleet.ValidUntil)
	}
	clientToken := ""
	if fleet.ClientToken != "" {
		clientToken = fmt.Sprintf("<clientToken>%s</clientToken>", fleet.ClientToken)
	}
	return fmt.Sprintf("<activityStatus>%s</activityStatus><createTime>%s</createTime><fleetId>%s</fleetId><fleetState>%s</fleetState>%s<fulfilledCapacity>%s</fulfilledCapacity><fulfilledOnDemandCapacity>%s</fulfilledOnDemandCapacity><targetCapacitySpecification><totalTargetCapacity>%d</totalTargetCapacity><onDemandTargetCapacity>%d</onDemandTargetCapacity><spotTargetCapacity>%d</spotTargetCapacity><defaultTargetCapacityType>%s</defaultTargetCapacityType></targetCapacitySpecification><terminateInstancesWithExpiration>%t</terminateInstancesWithExpiration><type>%s</type>%s%s<replaceUnhealthyInstances>%t</replaceUnhealthyInstances>%s%s",
		fleet.ActivityStatus, fleet.CreateTime, fleet.FleetId, fleet.FleetState, clientToken,
		ec2FloatStr(fleet.FulfilledCapacity), ec2FloatStr(fleet.FulfilledOnDemandCapacity),
		fleet.TotalTargetCapacity, fleet.OnDemandTargetCapacity, fleet.SpotTargetCapacity, fleet.DefaultTargetCapacityType,
		fleet.TerminateInstancesWithExpiration, fleet.Type, validFrom, validUntil,
		fleet.ReplaceUnhealthyInstances, instSet.String(), writeTagSetXML(fleet.Tags))
}

func handleDescribeFleets(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "FleetId")
	results := make([]EC2Fleet, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			f, ok := ec2Fleets.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidFleetId.NotFound", fmt.Sprintf("The fleet ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, f)
		}
	} else {
		results = append(results, ec2Fleets.List()...)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].FleetId < results[j].FleetId })
	var items strings.Builder
	for _, f := range results {
		items.WriteString("<item>")
		items.WriteString(ec2FleetFieldsXML(f))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeFleetsResponse %s><requestId>%s</requestId><fleetSet>%s</fleetSet></DescribeFleetsResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func handleDescribeFleetInstances(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("FleetId")
	f, ok := ec2Fleets.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidFleetId.NotFound", fmt.Sprintf("The fleet ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	var items strings.Builder
	for _, iid := range f.InstanceIds {
		fmt.Fprintf(&items, "<item><instanceId>%s</instanceId><instanceType>%s</instanceType><instanceHealth>healthy</instanceHealth></item>", iid, f.InstanceType)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeFleetInstancesResponse %s><requestId>%s</requestId><fleetId>%s</fleetId><activeInstanceSet>%s</activeInstanceSet></DescribeFleetInstancesResponse>`,
		ec2Xmlns(), generateUUID(), id, items.String())
}

func handleDescribeFleetHistory(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("FleetId")
	f, ok := ec2Fleets.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidFleetId.NotFound", fmt.Sprintf("The fleet ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	now := ec2NowMilli()
	var records strings.Builder
	fmt.Fprintf(&records, "<item><timestamp>%s</timestamp><eventType>fleetRequestChange</eventType><eventInformation><eventSubType>active</eventSubType><eventDescription>EC2 Fleet %s is in the active state.</eventDescription></eventInformation></item>", f.CreateTime, id)
	for _, iid := range f.InstanceIds {
		fmt.Fprintf(&records, "<item><timestamp>%s</timestamp><eventType>instanceChange</eventType><eventInformation><eventSubType>launched</eventSubType><instanceId>%s</instanceId></eventInformation></item>", now, iid)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeFleetHistoryResponse %s><requestId>%s</requestId><fleetId>%s</fleetId><startTime>%s</startTime><lastEvaluatedTime>%s</lastEvaluatedTime><historyRecordSet>%s</historyRecordSet></DescribeFleetHistoryResponse>`,
		ec2Xmlns(), generateUUID(), id, f.CreateTime, now, records.String())
}

func handleModifyFleet(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("FleetId")
	f, ok := ec2Fleets.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidFleetId.NotFound", fmt.Sprintf("The fleet ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("TargetCapacitySpecification.TotalTargetCapacity"); v != "" {
		f.TotalTargetCapacity = ec2AtoiOr(v, f.TotalTargetCapacity)
	}
	ec2Fleets.Put(id, f)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyFleetResponse %s><requestId>%s</requestId><return>true</return></ModifyFleetResponse>`, ec2Xmlns(), generateUUID())
}

func handleDeleteFleets(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "FleetId")
	terminate := r.FormValue("TerminateInstances") == "true"
	var success, failed strings.Builder
	for _, id := range ids {
		f, ok := ec2Fleets.Get(id)
		if !ok {
			fmt.Fprintf(&failed, "<item><fleetId>%s</fleetId><error><code>fleetIdDoesNotExist</code><message>The fleet ID %s does not exist</message></error></item>", id, id)
			continue
		}
		prev := f.FleetState
		newState := "deleted-running"
		if terminate {
			newState = "deleted-terminating"
			for _, iid := range f.InstanceIds {
				if inst, ok := ec2Instances.Get(iid); ok {
					inst.State = "shutting-down"
					ec2Instances.Put(iid, inst)
				}
			}
		}
		f.FleetState = newState
		ec2Fleets.Put(id, f)
		fmt.Fprintf(&success, "<item><currentFleetState>%s</currentFleetState><previousFleetState>%s</previousFleetState><fleetId>%s</fleetId></item>", newState, prev, id)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteFleetsResponse %s><requestId>%s</requestId><successfulFleetDeletionSet>%s</successfulFleetDeletionSet><unsuccessfulFleetDeletionSet>%s</unsuccessfulFleetDeletionSet></DeleteFleetsResponse>`,
		ec2Xmlns(), generateUUID(), success.String(), failed.String())
}

func handleRequestSpotInstances(w http.ResponseWriter, r *http.Request) {
	count := ec2AtoiOr(r.FormValue("InstanceCount"), 1)
	spotPrice := r.FormValue("SpotPrice")
	if spotPrice == "" {
		spotPrice = "0.0035"
	}
	reqType := r.FormValue("Type")
	if reqType == "" {
		reqType = "one-time"
	}
	interruption := r.FormValue("InstanceInterruptionBehavior")
	if interruption == "" {
		interruption = "terminate"
	}
	instanceType := r.FormValue("LaunchSpecification.InstanceType")
	if instanceType == "" {
		instanceType = "t3.micro"
	}
	imageID := r.FormValue("LaunchSpecification.ImageId")
	if imageID == "" {
		imageID = "ami-12345678"
	}
	now := ec2NowMilli()
	var items strings.Builder
	for i := 0; i < count; i++ {
		instID := ec2LaunchFleetInstance(instanceType, "")
		sir := EC2SpotInstanceRequest{
			SpotInstanceRequestId:        ec2ID("sir"),
			SpotPrice:                    spotPrice,
			Type:                         reqType,
			State:                        "active",
			StatusCode:                   "fulfilled",
			StatusMessage:                "Your spot request is fulfilled.",
			StatusUpdateTime:             now,
			InstanceId:                   instID,
			ProductDescription:           "Linux/UNIX",
			LaunchedAvailabilityZone:     awsAvailabilityZone(),
			InstanceInterruptionBehavior: interruption,
			ValidFrom:                    r.FormValue("ValidFrom"),
			ValidUntil:                   r.FormValue("ValidUntil"),
			CreateTime:                   now,
			ImageId:                      imageID,
			LaunchInstanceType:           instanceType,
			Tags:                         ec2ParseTagSpecs(r),
		}
		ec2SpotInstanceRequests.Put(sir.SpotInstanceRequestId, sir)
		items.WriteString("<item>")
		items.WriteString(ec2SpotInstanceRequestXML(sir))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RequestSpotInstancesResponse %s><requestId>%s</requestId><spotInstanceRequestSet>%s</spotInstanceRequestSet></RequestSpotInstancesResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func ec2SpotInstanceRequestXML(sir EC2SpotInstanceRequest) string {
	validFrom := ""
	if sir.ValidFrom != "" {
		validFrom = fmt.Sprintf("<validFrom>%s</validFrom>", sir.ValidFrom)
	}
	validUntil := ""
	if sir.ValidUntil != "" {
		validUntil = fmt.Sprintf("<validUntil>%s</validUntil>", sir.ValidUntil)
	}
	instance := ""
	if sir.InstanceId != "" {
		instance = fmt.Sprintf("<instanceId>%s</instanceId>", sir.InstanceId)
	}
	launchSpec := fmt.Sprintf("<launchSpecification><imageId>%s</imageId><instanceType>%s</instanceType></launchSpecification>", sir.ImageId, sir.LaunchInstanceType)
	return fmt.Sprintf("<spotInstanceRequestId>%s</spotInstanceRequestId><spotPrice>%s</spotPrice><type>%s</type><state>%s</state><status><code>%s</code><message>%s</message><updateTime>%s</updateTime></status>%s<productDescription>%s</productDescription>%s<launchedAvailabilityZone>%s</launchedAvailabilityZone><instanceInterruptionBehavior>%s</instanceInterruptionBehavior>%s%s<createTime>%s</createTime>%s",
		sir.SpotInstanceRequestId, sir.SpotPrice, sir.Type, sir.State, sir.StatusCode,
		xmlEscape(sir.StatusMessage), sir.StatusUpdateTime, launchSpec, sir.ProductDescription, instance,
		sir.LaunchedAvailabilityZone, sir.InstanceInterruptionBehavior, validFrom, validUntil,
		sir.CreateTime, writeTagSetXML(sir.Tags))
}

func handleDescribeSpotInstanceRequests(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "SpotInstanceRequestId")
	filters := ec2Filters(r)
	results := make([]EC2SpotInstanceRequest, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			sir, ok := ec2SpotInstanceRequests.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidSpotInstanceRequestID.NotFound", fmt.Sprintf("The spot instance request ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, sir)
		}
	} else {
		for _, sir := range ec2SpotInstanceRequests.List() {
			if !ec2SpotRequestMatchesFilters(sir, filters) {
				continue
			}
			results = append(results, sir)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].SpotInstanceRequestId < results[j].SpotInstanceRequestId })
	var items strings.Builder
	for _, sir := range results {
		items.WriteString("<item>")
		items.WriteString(ec2SpotInstanceRequestXML(sir))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSpotInstanceRequestsResponse %s><requestId>%s</requestId><spotInstanceRequestSet>%s</spotInstanceRequestSet></DescribeSpotInstanceRequestsResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func ec2SpotRequestMatchesFilters(sir EC2SpotInstanceRequest, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "state":
			if !ec2StrInValues(sir.State, vals) {
				return false
			}
		case "spot-instance-request-id":
			if !ec2StrInValues(sir.SpotInstanceRequestId, vals) {
				return false
			}
		case "instance-id":
			if !ec2StrInValues(sir.InstanceId, vals) {
				return false
			}
		case "launch.instance-type":
			if !ec2StrInValues(sir.LaunchInstanceType, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, sir.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleCancelSpotInstanceRequests(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "SpotInstanceRequestId")
	var items strings.Builder
	for _, id := range ids {
		sir, ok := ec2SpotInstanceRequests.Get(id)
		if !ok {
			ec2ErrorXML(w, "InvalidSpotInstanceRequestID.NotFound", fmt.Sprintf("The spot instance request ID %q does not exist", id), http.StatusBadRequest)
			return
		}
		sir.State = "cancelled"
		sir.StatusCode = "canceled-before-fulfillment"
		ec2SpotInstanceRequests.Put(id, sir)
		fmt.Fprintf(&items, "<item><spotInstanceRequestId>%s</spotInstanceRequestId><state>cancelled</state></item>", id)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CancelSpotInstanceRequestsResponse %s><requestId>%s</requestId><spotInstanceRequestSet>%s</spotInstanceRequestSet></CancelSpotInstanceRequestsResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func handleRequestSpotFleet(w http.ResponseWriter, r *http.Request) {
	target := ec2AtoiOr(r.FormValue("SpotFleetRequestConfig.TargetCapacity"), 0)
	onDemand := ec2AtoiOr(r.FormValue("SpotFleetRequestConfig.OnDemandTargetCapacity"), 0)
	role := r.FormValue("SpotFleetRequestConfig.IamFleetRole")
	allocStrategy := r.FormValue("SpotFleetRequestConfig.AllocationStrategy")
	if allocStrategy == "" {
		allocStrategy = "lowestPrice"
	}
	fleetType := r.FormValue("SpotFleetRequestConfig.Type")
	if fleetType == "" {
		fleetType = "maintain"
	}
	spotPrice := r.FormValue("SpotFleetRequestConfig.SpotPrice")
	interruption := r.FormValue("SpotFleetRequestConfig.InstanceInterruptionBehavior")
	if interruption == "" {
		interruption = "terminate"
	}
	// First launch spec drives the synthesized instances.
	instType := r.FormValue("SpotFleetRequestConfig.LaunchSpecifications.1.InstanceType")
	if instType == "" {
		instType = "t3.micro"
	}
	imageID := r.FormValue("SpotFleetRequestConfig.LaunchSpecifications.1.ImageId")
	if imageID == "" {
		imageID = "ami-12345678"
	}
	sfr := EC2SpotFleetRequest{
		SpotFleetRequestId:           ec2ID("sfr"),
		SpotFleetRequestState:        "active",
		ActivityStatus:               "fulfilled",
		CreateTime:                   ec2NowMilli(),
		IamFleetRole:                 role,
		AllocationStrategy:           allocStrategy,
		TargetCapacity:               target,
		OnDemandTargetCapacity:       onDemand,
		FulfilledCapacity:            float64(target),
		SpotPrice:                    spotPrice,
		Type:                         fleetType,
		ValidFrom:                    r.FormValue("SpotFleetRequestConfig.ValidFrom"),
		ValidUntil:                   r.FormValue("SpotFleetRequestConfig.ValidUntil"),
		TerminateInstancesWithExp:    r.FormValue("SpotFleetRequestConfig.TerminateInstancesWithExpiration") == "true",
		InstanceInterruptionBehavior: interruption,
		LaunchInstanceType:           instType,
		LaunchImageId:                imageID,
	}
	for i := 0; i < target; i++ {
		sfr.InstanceIds = append(sfr.InstanceIds, ec2LaunchFleetInstance(instType, ""))
	}
	ec2SpotFleetRequests.Put(sfr.SpotFleetRequestId, sfr)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RequestSpotFleetResponse %s><requestId>%s</requestId><spotFleetRequestId>%s</spotFleetRequestId></RequestSpotFleetResponse>`,
		ec2Xmlns(), generateUUID(), sfr.SpotFleetRequestId)
}

func ec2SpotFleetConfigXML(sfr EC2SpotFleetRequest) string {
	validFrom := ""
	if sfr.ValidFrom != "" {
		validFrom = fmt.Sprintf("<validFrom>%s</validFrom>", sfr.ValidFrom)
	}
	validUntil := ""
	if sfr.ValidUntil != "" {
		validUntil = fmt.Sprintf("<validUntil>%s</validUntil>", sfr.ValidUntil)
	}
	spotPrice := ""
	if sfr.SpotPrice != "" {
		spotPrice = fmt.Sprintf("<spotPrice>%s</spotPrice>", sfr.SpotPrice)
	}
	launchSpecs := fmt.Sprintf("<launchSpecifications><item><imageId>%s</imageId><instanceType>%s</instanceType></item></launchSpecifications>", sfr.LaunchImageId, sfr.LaunchInstanceType)
	return fmt.Sprintf("<allocationStrategy>%s</allocationStrategy><fulfilledCapacity>%s</fulfilledCapacity><iamFleetRole>%s</iamFleetRole>%s%s<targetCapacity>%d</targetCapacity><onDemandTargetCapacity>%d</onDemandTargetCapacity><terminateInstancesWithExpiration>%t</terminateInstancesWithExpiration><type>%s</type>%s%s<instanceInterruptionBehavior>%s</instanceInterruptionBehavior>",
		sfr.AllocationStrategy, ec2FloatStr(sfr.FulfilledCapacity), sfr.IamFleetRole, spotPrice, launchSpecs,
		sfr.TargetCapacity, sfr.OnDemandTargetCapacity, sfr.TerminateInstancesWithExp, sfr.Type,
		validFrom, validUntil, sfr.InstanceInterruptionBehavior)
}

func handleDescribeSpotFleetRequests(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "SpotFleetRequestId")
	results := make([]EC2SpotFleetRequest, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			sfr, ok := ec2SpotFleetRequests.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidSpotFleetRequestId.NotFound", fmt.Sprintf("The spot fleet request ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, sfr)
		}
	} else {
		results = append(results, ec2SpotFleetRequests.List()...)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].SpotFleetRequestId < results[j].SpotFleetRequestId })
	var items strings.Builder
	for _, sfr := range results {
		fmt.Fprintf(&items, "<item><activityStatus>%s</activityStatus><createTime>%s</createTime><spotFleetRequestConfig>%s</spotFleetRequestConfig><spotFleetRequestId>%s</spotFleetRequestId><spotFleetRequestState>%s</spotFleetRequestState>%s</item>",
			sfr.ActivityStatus, sfr.CreateTime, ec2SpotFleetConfigXML(sfr), sfr.SpotFleetRequestId, sfr.SpotFleetRequestState, writeTagSetXML(sfr.Tags))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSpotFleetRequestsResponse %s><requestId>%s</requestId><spotFleetRequestConfigSet>%s</spotFleetRequestConfigSet></DescribeSpotFleetRequestsResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func handleDescribeSpotFleetInstances(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("SpotFleetRequestId")
	sfr, ok := ec2SpotFleetRequests.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidSpotFleetRequestId.NotFound", fmt.Sprintf("The spot fleet request ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	var items strings.Builder
	for _, iid := range sfr.InstanceIds {
		fmt.Fprintf(&items, "<item><instanceId>%s</instanceId><instanceType>%s</instanceType><instanceHealth>healthy</instanceHealth></item>", iid, sfr.LaunchInstanceType)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSpotFleetInstancesResponse %s><requestId>%s</requestId><spotFleetRequestId>%s</spotFleetRequestId><activeInstanceSet>%s</activeInstanceSet></DescribeSpotFleetInstancesResponse>`,
		ec2Xmlns(), generateUUID(), id, items.String())
}

func handleDescribeSpotFleetRequestHistory(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("SpotFleetRequestId")
	sfr, ok := ec2SpotFleetRequests.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidSpotFleetRequestId.NotFound", fmt.Sprintf("The spot fleet request ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	now := ec2NowMilli()
	var records strings.Builder
	fmt.Fprintf(&records, "<item><timestamp>%s</timestamp><eventType>fleetRequestChange</eventType><eventInformation><eventSubType>active</eventSubType><eventDescription>Spot Fleet %s is in the active state.</eventDescription></eventInformation></item>", sfr.CreateTime, id)
	for _, iid := range sfr.InstanceIds {
		fmt.Fprintf(&records, "<item><timestamp>%s</timestamp><eventType>instanceChange</eventType><eventInformation><eventSubType>launched</eventSubType><instanceId>%s</instanceId></eventInformation></item>", now, iid)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSpotFleetRequestHistoryResponse %s><requestId>%s</requestId><spotFleetRequestId>%s</spotFleetRequestId><startTime>%s</startTime><lastEvaluatedTime>%s</lastEvaluatedTime><historyRecordSet>%s</historyRecordSet></DescribeSpotFleetRequestHistoryResponse>`,
		ec2Xmlns(), generateUUID(), id, sfr.CreateTime, now, records.String())
}

func handleModifySpotFleetRequest(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("SpotFleetRequestId")
	sfr, ok := ec2SpotFleetRequests.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidSpotFleetRequestId.NotFound", fmt.Sprintf("The spot fleet request ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("TargetCapacity"); v != "" {
		sfr.TargetCapacity = ec2AtoiOr(v, sfr.TargetCapacity)
	}
	if v := r.FormValue("OnDemandTargetCapacity"); v != "" {
		sfr.OnDemandTargetCapacity = ec2AtoiOr(v, sfr.OnDemandTargetCapacity)
	}
	ec2SpotFleetRequests.Put(id, sfr)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifySpotFleetRequestResponse %s><requestId>%s</requestId><return>true</return></ModifySpotFleetRequestResponse>`, ec2Xmlns(), generateUUID())
}

func handleCancelSpotFleetRequests(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "SpotFleetRequestId")
	terminate := r.FormValue("TerminateInstances") == "true"
	var success, failed strings.Builder
	for _, id := range ids {
		sfr, ok := ec2SpotFleetRequests.Get(id)
		if !ok {
			fmt.Fprintf(&failed, "<item><spotFleetRequestId>%s</spotFleetRequestId><error><code>InvalidSpotFleetRequestId.NotFound</code><message>The spot fleet request ID %s does not exist</message></error></item>", id, id)
			continue
		}
		prev := sfr.SpotFleetRequestState
		newState := "cancelled_running"
		if terminate {
			newState = "cancelled_terminating"
			for _, iid := range sfr.InstanceIds {
				if inst, ok := ec2Instances.Get(iid); ok {
					inst.State = "shutting-down"
					ec2Instances.Put(iid, inst)
				}
			}
		}
		sfr.SpotFleetRequestState = newState
		ec2SpotFleetRequests.Put(id, sfr)
		fmt.Fprintf(&success, "<item><currentSpotFleetRequestState>%s</currentSpotFleetRequestState><previousSpotFleetRequestState>%s</previousSpotFleetRequestState><spotFleetRequestId>%s</spotFleetRequestId></item>", newState, prev, id)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CancelSpotFleetRequestsResponse %s><requestId>%s</requestId><successfulFleetRequestSet>%s</successfulFleetRequestSet><unsuccessfulFleetRequestSet>%s</unsuccessfulFleetRequestSet></CancelSpotFleetRequestsResponse>`,
		ec2Xmlns(), generateUUID(), success.String(), failed.String())
}

func handleCreateSpotDatafeedSubscription(w http.ResponseWriter, r *http.Request) {
	bucket := r.FormValue("Bucket")
	if bucket == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter Bucket", http.StatusBadRequest)
		return
	}
	sub := EC2SpotDatafeedSubscription{
		Bucket:  bucket,
		Prefix:  r.FormValue("Prefix"),
		OwnerId: ec2Owner(),
		State:   "Active",
	}
	ec2SpotDatafeed.Put("default", sub)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateSpotDatafeedSubscriptionResponse %s><requestId>%s</requestId><spotDatafeedSubscription>%s</spotDatafeedSubscription></CreateSpotDatafeedSubscriptionResponse>`,
		ec2Xmlns(), generateUUID(), ec2SpotDatafeedXML(sub))
}

func ec2SpotDatafeedXML(sub EC2SpotDatafeedSubscription) string {
	prefix := ""
	if sub.Prefix != "" {
		prefix = fmt.Sprintf("<prefix>%s</prefix>", xmlEscape(sub.Prefix))
	}
	return fmt.Sprintf("<ownerId>%s</ownerId><bucket>%s</bucket>%s<state>%s</state>", sub.OwnerId, xmlEscape(sub.Bucket), prefix, sub.State)
}

func handleDescribeSpotDatafeedSubscription(w http.ResponseWriter, r *http.Request) {
	sub, ok := ec2SpotDatafeed.Get("default")
	if !ok {
		ec2ErrorXML(w, "InvalidSpotDatafeed.NotFound", "There is no data feed for the account.", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSpotDatafeedSubscriptionResponse %s><requestId>%s</requestId><spotDatafeedSubscription>%s</spotDatafeedSubscription></DescribeSpotDatafeedSubscriptionResponse>`,
		ec2Xmlns(), generateUUID(), ec2SpotDatafeedXML(sub))
}

func handleDeleteSpotDatafeedSubscription(w http.ResponseWriter, r *http.Request) {
	ec2SpotDatafeed.Delete("default")
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteSpotDatafeedSubscriptionResponse %s><requestId>%s</requestId><return>true</return></DeleteSpotDatafeedSubscriptionResponse>`, ec2Xmlns(), generateUUID())
}

func handleDescribeSpotPriceHistory(w http.ResponseWriter, r *http.Request) {
	instanceTypes := ec2ParamList(r, "InstanceType")
	if len(instanceTypes) == 0 {
		instanceTypes = []string{"t3.micro", "t3.small"}
	}
	products := ec2ParamList(r, "ProductDescription")
	if len(products) == 0 {
		products = []string{"Linux/UNIX"}
	}
	az := r.FormValue("AvailabilityZone")
	if az == "" {
		az = awsAvailabilityZone()
	}
	now := ec2NowMilli()
	var items strings.Builder
	prices := map[string]string{"t3.micro": "0.0035", "t3.small": "0.0069", "t3.medium": "0.0139"}
	for _, it := range instanceTypes {
		price := prices[it]
		if price == "" {
			price = "0.0100"
		}
		for _, pd := range products {
			fmt.Fprintf(&items, "<item><instanceType>%s</instanceType><productDescription>%s</productDescription><spotPrice>%s</spotPrice><timestamp>%s</timestamp><availabilityZone>%s</availabilityZone></item>",
				it, pd, price, now, az)
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSpotPriceHistoryResponse %s><requestId>%s</requestId><spotPriceHistorySet>%s</spotPriceHistorySet></DescribeSpotPriceHistoryResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func handleGetSpotPlacementScores(w http.ResponseWriter, r *http.Request) {
	regions := ec2ParamList(r, "RegionNames")
	if len(regions) == 0 {
		regions = []string{awsRegion()}
	}
	var items strings.Builder
	for _, region := range regions {
		fmt.Fprintf(&items, "<item><region>%s</region><score>9</score></item>", region)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetSpotPlacementScoresResponse %s><requestId>%s</requestId><spotPlacementScoreSet>%s</spotPlacementScoreSet></GetSpotPlacementScoresResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func handleDescribeScheduledInstanceAvailability(w http.ResponseWriter, r *http.Request) {
	az := awsAvailabilityZone()
	token := generateUUID()
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeScheduledInstanceAvailabilityResponse %s><requestId>%s</requestId><scheduledInstanceAvailabilitySet><item><availabilityZone>%s</availabilityZone><availableInstanceCount>20</availableInstanceCount><firstSlotStartTime>%s</firstSlotStartTime><hourlyPrice>0.095</hourlyPrice><instanceType>c4.large</instanceType><maxTermDurationInDays>366</maxTermDurationInDays><minTermDurationInDays>31</minTermDurationInDays><networkPlatform>EC2-VPC</networkPlatform><platform>Linux/UNIX</platform><purchaseToken>%s</purchaseToken><recurrence><frequency>Weekly</frequency><interval>1</interval><occurrenceDaySet><item>1</item></occurrenceDaySet><occurrenceRelativeToEnd>false</occurrenceRelativeToEnd></recurrence><slotDurationInHours>23</slotDurationInHours><totalScheduledInstanceHours>1219</totalScheduledInstanceHours></item></scheduledInstanceAvailabilitySet></DescribeScheduledInstanceAvailabilityResponse>`,
		ec2Xmlns(), generateUUID(), az, ec2NowMilli(), token)
}

func handlePurchaseScheduledInstances(w http.ResponseWriter, r *http.Request) {
	count := ec2AtoiOr(r.FormValue("PurchaseRequest.1.InstanceCount"), 1)
	now := ec2NowMilli()
	si := EC2ScheduledInstance{
		ScheduledInstanceId:         "sci-" + generateUUID()[:17],
		AvailabilityZone:            awsAvailabilityZone(),
		InstanceType:                "c4.large",
		Platform:                    "Linux/UNIX",
		NetworkPlatform:             "EC2-VPC",
		InstanceCount:               count,
		SlotDurationInHours:         23,
		HourlyPrice:                 "0.095",
		TotalScheduledInstanceHours: 1219,
		CreateDate:                  now,
		TermStartDate:               now,
		NextSlotStartTime:           now,
		RecurrenceFrequency:         "Weekly",
		RecurrenceInterval:          1,
	}
	ec2ScheduledInstances.Put(si.ScheduledInstanceId, si)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<PurchaseScheduledInstancesResponse %s><requestId>%s</requestId><scheduledInstanceSet><item>%s</item></scheduledInstanceSet></PurchaseScheduledInstancesResponse>`,
		ec2Xmlns(), generateUUID(), ec2ScheduledInstanceXML(si))
}

func ec2ScheduledInstanceXML(si EC2ScheduledInstance) string {
	return fmt.Sprintf("<scheduledInstanceId>%s</scheduledInstanceId><availabilityZone>%s</availabilityZone><instanceType>%s</instanceType><platform>%s</platform><networkPlatform>%s</networkPlatform><instanceCount>%d</instanceCount><slotDurationInHours>%d</slotDurationInHours><hourlyPrice>%s</hourlyPrice><totalScheduledInstanceHours>%d</totalScheduledInstanceHours><createDate>%s</createDate><termStartDate>%s</termStartDate><nextSlotStartTime>%s</nextSlotStartTime><recurrence><frequency>%s</frequency><interval>%d</interval></recurrence>",
		si.ScheduledInstanceId, si.AvailabilityZone, si.InstanceType, si.Platform, si.NetworkPlatform,
		si.InstanceCount, si.SlotDurationInHours, si.HourlyPrice, si.TotalScheduledInstanceHours,
		si.CreateDate, si.TermStartDate, si.NextSlotStartTime, si.RecurrenceFrequency, si.RecurrenceInterval)
}

func handleDescribeScheduledInstances(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ScheduledInstanceId")
	results := make([]EC2ScheduledInstance, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			si, ok := ec2ScheduledInstances.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidScheduledInstance", fmt.Sprintf("The scheduled instance ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, si)
		}
	} else {
		results = append(results, ec2ScheduledInstances.List()...)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ScheduledInstanceId < results[j].ScheduledInstanceId })
	var items strings.Builder
	for _, si := range results {
		items.WriteString("<item>")
		items.WriteString(ec2ScheduledInstanceXML(si))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeScheduledInstancesResponse %s><requestId>%s</requestId><scheduledInstanceSet>%s</scheduledInstanceSet></DescribeScheduledInstancesResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func handleRunScheduledInstances(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ScheduledInstanceId")
	si, ok := ec2ScheduledInstances.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidScheduledInstance", fmt.Sprintf("The scheduled instance ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	count := ec2AtoiOr(r.FormValue("InstanceCount"), si.InstanceCount)
	instType := r.FormValue("LaunchSpecification.InstanceType")
	if instType == "" {
		instType = si.InstanceType
	}
	var items strings.Builder
	for i := 0; i < count; i++ {
		iid := ec2LaunchFleetInstance(instType, "")
		fmt.Fprintf(&items, "<item>%s</item>", iid)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RunScheduledInstancesResponse %s><requestId>%s</requestId><instanceIdSet>%s</instanceIdSet></RunScheduledInstancesResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

// ec2HostOfferings is the catalog of Dedicated Host Reservation offerings the
// sim advertises (a small real-shaped set).
var ec2HostOfferings = []EC2HostReservation{
	{OfferingId: "hro-0123456789abcdef0", InstanceFamily: "m4", PaymentOption: "NoUpfront", HourlyPrice: "1.499", UpfrontPrice: "0.0", CurrencyCode: "USD", Duration: 31536000},
	{OfferingId: "hro-fedcba9876543210f", InstanceFamily: "c5", PaymentOption: "AllUpfront", HourlyPrice: "0.0", UpfrontPrice: "12000.0", CurrencyCode: "USD", Duration: 94608000},
}

func ec2HostOfferingByID(id string) (EC2HostReservation, bool) {
	for _, o := range ec2HostOfferings {
		if o.OfferingId == id {
			return o, true
		}
	}
	return EC2HostReservation{}, false
}

func handleDescribeHostReservationOfferings(w http.ResponseWriter, r *http.Request) {
	var items strings.Builder
	for _, o := range ec2HostOfferings {
		fmt.Fprintf(&items, "<item><offeringId>%s</offeringId><instanceFamily>%s</instanceFamily><paymentOption>%s</paymentOption><hourlyPrice>%s</hourlyPrice><upfrontPrice>%s</upfrontPrice><currencyCode>%s</currencyCode><duration>%d</duration></item>",
			o.OfferingId, o.InstanceFamily, o.PaymentOption, o.HourlyPrice, o.UpfrontPrice, o.CurrencyCode, o.Duration)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeHostReservationOfferingsResponse %s><requestId>%s</requestId><offeringSet>%s</offeringSet></DescribeHostReservationOfferingsResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func handleGetHostReservationPurchasePreview(w http.ResponseWriter, r *http.Request) {
	offeringID := r.FormValue("OfferingId")
	o, ok := ec2HostOfferingByID(offeringID)
	if !ok {
		ec2ErrorXML(w, "InvalidHostReservationOfferingId.NotFound", fmt.Sprintf("The offering ID %q does not exist", offeringID), http.StatusBadRequest)
		return
	}
	hostIDs := ec2ParamList(r, "HostIdSet")
	var hostSet strings.Builder
	for _, h := range hostIDs {
		fmt.Fprintf(&hostSet, "<item>%s</item>", h)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetHostReservationPurchasePreviewResponse %s><requestId>%s</requestId><currencyCode>%s</currencyCode><totalHourlyPrice>%s</totalHourlyPrice><totalUpfrontPrice>%s</totalUpfrontPrice><purchase><item><instanceFamily>%s</instanceFamily><paymentOption>%s</paymentOption><hourlyPrice>%s</hourlyPrice><upfrontPrice>%s</upfrontPrice><currencyCode>%s</currencyCode><duration>%d</duration><hostIdSet>%s</hostIdSet></item></purchase></GetHostReservationPurchasePreviewResponse>`,
		ec2Xmlns(), generateUUID(), o.CurrencyCode, o.HourlyPrice, o.UpfrontPrice,
		o.InstanceFamily, o.PaymentOption, o.HourlyPrice, o.UpfrontPrice, o.CurrencyCode, o.Duration, hostSet.String())
}

func handlePurchaseHostReservation(w http.ResponseWriter, r *http.Request) {
	offeringID := r.FormValue("OfferingId")
	o, ok := ec2HostOfferingByID(offeringID)
	if !ok {
		ec2ErrorXML(w, "InvalidHostReservationOfferingId.NotFound", fmt.Sprintf("The offering ID %q does not exist", offeringID), http.StatusBadRequest)
		return
	}
	hostIDs := ec2ParamList(r, "HostIdSet")
	now := ec2NowMilli()
	hr := EC2HostReservation{
		HostReservationId: "hr-" + generateUUID()[:17],
		OfferingId:        offeringID,
		InstanceFamily:    o.InstanceFamily,
		HostIdSet:         hostIDs,
		Count:             max(len(hostIDs), 1),
		State:             "active",
		PaymentOption:     o.PaymentOption,
		HourlyPrice:       o.HourlyPrice,
		UpfrontPrice:      o.UpfrontPrice,
		CurrencyCode:      o.CurrencyCode,
		Duration:          o.Duration,
		Start:             now,
		Tags:              ec2ParseTagSpecs(r),
	}
	ec2HostReservations.Put(hr.HostReservationId, hr)
	var hostSet strings.Builder
	for _, h := range hostIDs {
		fmt.Fprintf(&hostSet, "<item>%s</item>", h)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<PurchaseHostReservationResponse %s><requestId>%s</requestId><clientToken>%s</clientToken><currencyCode>%s</currencyCode><totalHourlyPrice>%s</totalHourlyPrice><totalUpfrontPrice>%s</totalUpfrontPrice><purchase><item><hostReservationId>%s</hostReservationId><instanceFamily>%s</instanceFamily><paymentOption>%s</paymentOption><hourlyPrice>%s</hourlyPrice><upfrontPrice>%s</upfrontPrice><currencyCode>%s</currencyCode><duration>%d</duration><hostIdSet>%s</hostIdSet></item></purchase></PurchaseHostReservationResponse>`,
		ec2Xmlns(), generateUUID(), r.FormValue("ClientToken"), o.CurrencyCode, o.HourlyPrice, o.UpfrontPrice,
		hr.HostReservationId, o.InstanceFamily, o.PaymentOption, o.HourlyPrice, o.UpfrontPrice, o.CurrencyCode, o.Duration, hostSet.String())
}

func handleDescribeHostReservations(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "HostReservationIdSet")
	results := make([]EC2HostReservation, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			hr, ok := ec2HostReservations.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidHostReservationId.NotFound", fmt.Sprintf("The host reservation ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, hr)
		}
	} else {
		results = append(results, ec2HostReservations.List()...)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].HostReservationId < results[j].HostReservationId })
	var items strings.Builder
	for _, hr := range results {
		var hostSet strings.Builder
		for _, h := range hr.HostIdSet {
			fmt.Fprintf(&hostSet, "<item>%s</item>", h)
		}
		fmt.Fprintf(&items, "<item><hostReservationId>%s</hostReservationId><offeringId>%s</offeringId><instanceFamily>%s</instanceFamily><hostIdSet>%s</hostIdSet><count>%d</count><state>%s</state><paymentOption>%s</paymentOption><hourlyPrice>%s</hourlyPrice><upfrontPrice>%s</upfrontPrice><currencyCode>%s</currencyCode><duration>%d</duration><start>%s</start>%s</item>",
			hr.HostReservationId, hr.OfferingId, hr.InstanceFamily, hostSet.String(), hr.Count, hr.State,
			hr.PaymentOption, hr.HourlyPrice, hr.UpfrontPrice, hr.CurrencyCode, hr.Duration, hr.Start, writeTagSetXML(hr.Tags))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeHostReservationsResponse %s><requestId>%s</requestId><hostReservationSet>%s</hostReservationSet></DescribeHostReservationsResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}
