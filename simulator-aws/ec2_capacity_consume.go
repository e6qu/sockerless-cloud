package main

import (
	"fmt"
	"net/http"
	"sync"
)

// A launch runs in a Capacity Reservation with matching instance type,
// platform, Availability Zone and tenancy that is active and has capacity,
// which reduces the reservation's available capacity by one per instance.
// An instance holds its slot until it is terminated.

var ec2CapacityMu sync.Mutex

func ec2CapacityReservationUsed(crID string) int {
	n := 0
	for _, inst := range ec2Instances.List() {
		if inst.CapacityReservationId == crID && inst.State != "terminated" {
			n++
		}
	}
	return n
}

func ec2CapacityReservationAvailable(cr EC2CapacityReservation) int {
	return max(0, cr.AvailableInstanceCount-ec2CapacityReservationUsed(cr.CapacityReservationId))
}

func ec2CapacityReservationMatches(cr EC2CapacityReservation, instanceType, az, tenancy string) bool {
	crTenancy := cr.Tenancy
	if crTenancy == "" {
		crTenancy = "default"
	}
	if tenancy == "" {
		tenancy = "default"
	}
	return cr.State == "active" && cr.InstanceType == instanceType && cr.InstancePlatform == "Linux/UNIX" &&
		cr.AvailabilityZone == az && crTenancy == tenancy
}

type ec2CapacityTargeting struct {
	Preference  string
	TargetId    string
	TargetGroup string
}

// Without a CapacityReservationSpecification the preference is open.
func ec2CapacityTargetingFrom(r *http.Request) ec2CapacityTargeting {
	t := ec2CapacityTargeting{
		Preference:  r.FormValue("CapacityReservationSpecification.CapacityReservationPreference"),
		TargetId:    r.FormValue("CapacityReservationSpecification.CapacityReservationTarget.CapacityReservationId"),
		TargetGroup: r.FormValue("CapacityReservationSpecification.CapacityReservationTarget.CapacityReservationResourceGroupArn"),
	}
	if t.Preference == "" && t.TargetId == "" && t.TargetGroup == "" {
		t.Preference = "open"
	}
	return t
}

func ec2CapacityTargetingProblem(t ec2CapacityTargeting) *ec2Problem {
	switch t.Preference {
	case "", "open", "none", "capacity-reservations-only":
	default:
		return ec2Problemf("InvalidParameterValue", "Value (%s) for parameter CapacityReservationPreference is invalid.", t.Preference)
	}
	if (t.TargetId != "" || t.TargetGroup != "") && t.Preference != "" && t.Preference != "capacity-reservations-only" {
		return ec2Problemf("InvalidParameterCombination", "You can only specify CapacityReservationPreference and CapacityReservationTarget if the CapacityReservationPreference is capacity-reservations-only.")
	}
	if t.TargetId != "" {
		if _, ok := ec2GetCapacityReservation(t.TargetId); !ok {
			return ec2Problemf("InvalidCapacityReservationId.NotFound", "The Capacity Reservation ID '%s' does not exist", t.TargetId)
		}
	}
	return nil
}

// ec2ClaimCapacity picks the reservation one instance launches into, or none
// for On-Demand capacity. Hold ec2CapacityMu from the claim until the
// instance carrying it is stored.
func ec2ClaimCapacity(t ec2CapacityTargeting, instanceType, az, tenancy string) (string, *ec2Problem) {
	if t.TargetId != "" {
		cr, _ := ec2GetCapacityReservation(t.TargetId)
		if !ec2CapacityReservationMatches(cr, instanceType, az, tenancy) || ec2CapacityReservationAvailable(cr) == 0 {
			return "", ec2Problemf("ReservationCapacityExceeded", "The targeted Capacity Reservation %s does not have enough available instance capacity to fulfill your request.", t.TargetId)
		}
		return cr.CapacityReservationId, nil
	}
	if t.Preference == "none" {
		return "", nil
	}
	// GetGroupsForCapacityReservation places no reservation in a group, so a
	// group target holds no capacity.
	if t.TargetGroup != "" {
		if t.Preference == "capacity-reservations-only" {
			return "", ec2Problemf("ReservationCapacityExceeded", "No Capacity Reservation in group %s has available instance capacity for this launch.", t.TargetGroup)
		}
		return "", nil
	}
	for _, cr := range ec2CapacityReservations.List() {
		if cr.InstanceMatchCriteria == "open" && ec2CapacityReservationMatches(cr, instanceType, az, tenancy) && ec2CapacityReservationAvailable(cr) > 0 {
			return cr.CapacityReservationId, nil
		}
	}
	if t.Preference == "capacity-reservations-only" {
		return "", ec2Problemf("ReservationCapacityExceeded", "No open Capacity Reservation with matching attributes has available instance capacity for this launch.")
	}
	return "", nil
}

func ec2InstanceCapacityReservationXML(inst EC2Instance) string {
	if inst.CapacityReservationPreference == "" && inst.CapacityReservationTargetId == "" && inst.CapacityReservationTargetGroup == "" {
		return ""
	}
	out := ""
	if inst.CapacityReservationId != "" {
		out = fmt.Sprintf("<capacityReservationId>%s</capacityReservationId>", inst.CapacityReservationId)
	}
	if inst.CapacityReservationTargetId != "" {
		return out + fmt.Sprintf("<capacityReservationSpecification><capacityReservationTarget><capacityReservationId>%s</capacityReservationId></capacityReservationTarget></capacityReservationSpecification>", inst.CapacityReservationTargetId)
	}
	if inst.CapacityReservationTargetGroup != "" {
		return out + fmt.Sprintf("<capacityReservationSpecification><capacityReservationTarget><capacityReservationResourceGroupArn>%s</capacityReservationResourceGroupArn></capacityReservationTarget></capacityReservationSpecification>", xmlEscape(inst.CapacityReservationTargetGroup))
	}
	return out + fmt.Sprintf("<capacityReservationSpecification><capacityReservationPreference>%s</capacityReservationPreference></capacityReservationSpecification>", inst.CapacityReservationPreference)
}
