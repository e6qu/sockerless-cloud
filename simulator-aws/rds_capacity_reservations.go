package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// RDSCapacityReservation is a DB capacity reservation: capacity for DB
// instances of one DB instance class and one engine in each of its
// Availability Zones. State is what Amazon RDS recorded; the state a reader
// sees also follows the reservation's StartDate and EndDate
// (rdsCapacityReservationState).
type RDSCapacityReservation struct {
	Identifier             string
	Name                   string
	ARN                    string
	DBInstanceClass        string
	Engine                 string
	LicenseModel           string `json:",omitempty"`
	CapacityPerZone        int
	AvailabilityZones      []string
	State                  string
	CreateDate             time.Time
	StartDate              time.Time `json:",omitzero"`
	EndDate                time.Time `json:",omitzero"`
	CommitmentEndDate      time.Time `json:",omitzero"`
	CommittedCapacityPerAZ int       `json:",omitempty"`
	Tags                   map[string]string
}

const (
	rdsCapacityReservationActive    = "active"
	rdsCapacityReservationScheduled = "scheduled"
	rdsCapacityReservationCancelled = "cancelled"
	rdsCapacityReservationExpired   = "expired"
)

var (
	rdsCapacityReservations sim.Store[RDSCapacityReservation]

	rdsCapacityReservationNamePattern = regexp.MustCompile(`^[a-z](?:-?[a-z0-9]+){0,15}$`)
	rdsDBInstanceClassPattern         = regexp.MustCompile(`^db\.[0-9a-z]{2,6}\.[0-9a-z.]{4,20}$`)

	rdsCapacityReservationEngines = []string{
		"aurora-mysql", "aurora-postgresql", "mysql", "postgres", "mariadb",
		"oracle-ee", "oracle-ee-cdb", "oracle-se2", "oracle-se2-cdb",
		"sqlserver-ee", "sqlserver-se", "sqlserver-web", "sqlserver-ex", "sqlserver-dev-ee", "sqlserver-dev-se",
		"db2-ae", "db2-ce", "db2-se",
	}
	rdsCapacityReservationStates = []string{"active", "expired", "cancelled", "pending", "failed", "assessing", "scheduled", "delayed", "unsupported"}

	// rdsCapacity serializes every decision that reads a reservation's
	// available capacity with the change that takes it.
	rdsCapacity      sync.Mutex
	rdsCapacityHolds = map[string]rdsCapacityHold{}
)

// rdsCapacityHold is the reserved capacity a request takes for a DB instance
// before it stores the instance: the units in Zones of Reservation.
type rdsCapacityHold struct {
	Reservation string
	Zones       []string
}

func registerRDSCapacityReservations(r *AWSQueryRouter, srv *sim.Server) {
	rdsCapacityReservations = sim.MakeStore[RDSCapacityReservation](srv.DB(), "rds_capacity_reservations")
	r.RegisterVersioned(rdsAPIVersion, "CreateDBCapacityReservation", handleRDSCreateCapacityReservation)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBCapacityReservations", handleRDSDescribeCapacityReservations)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBCapacityReservation", handleRDSModifyCapacityReservation)
	r.RegisterVersioned(rdsAPIVersion, "CancelDBCapacityReservation", handleRDSCancelCapacityReservation)
}

func rdsCapacityReservationARN(id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:db-capacity-reservation:%s", awsRegion(), awsAccountID(), id)
}

// rdsFindCapacityReservation resolves a reservation by its identifier, its
// name or its ARN.
func rdsFindCapacityReservation(ref string) (RDSCapacityReservation, bool) {
	if res, ok := rdsCapacityReservations.Get(ref); ok {
		return res, true
	}
	for _, res := range rdsCapacityReservations.List() {
		if res.Name == ref || res.ARN == ref {
			return res, true
		}
	}
	return RDSCapacityReservation{}, false
}

// rdsCapacityReservationState is the reservation's state at now: a scheduled
// reservation is active from its start date, and one past its end date is
// expired.
func rdsCapacityReservationState(res RDSCapacityReservation, now time.Time) string {
	if res.State == rdsCapacityReservationCancelled {
		return res.State
	}
	if !res.EndDate.IsZero() && !now.Before(res.EndDate) {
		return rdsCapacityReservationExpired
	}
	if res.State == rdsCapacityReservationScheduled && !now.Before(res.StartDate) {
		return rdsCapacityReservationActive
	}
	return res.State
}

// rdsInCommitment reports whether now falls in the reservation's capacity
// commitment, which holds from the time Amazon RDS schedules it until the
// commitment ends.
func rdsInCommitment(res RDSCapacityReservation, now time.Time) bool {
	return !res.CommitmentEndDate.IsZero() && now.Before(res.CommitmentEndDate)
}

// rdsInstanceReservedZones are the Availability Zones whose reserved capacity
// the instance holds a unit of in reservation: the zones its current
// reservation serves while it runs, and the zones a pending modification
// moves it to, which hold their unit from the time Amazon RDS accepted it.
func rdsInstanceReservedZones(instance RDSInstance, reservation string) []string {
	var zones []string
	if instance.DBCapacityReservationIdentifier == reservation &&
		instance.DBInstanceStatus != "stopping" && instance.DBInstanceStatus != "stopped" {
		zones = append(zones, instance.AvailabilityZone)
		if instance.MultiAZ && !rdsIsReadReplica(instance) {
			secondary := instance.SecondaryAvailabilityZone
			if secondary == "" {
				secondary = rdsStandbyAvailabilityZone(instance.AvailabilityZone)
			}
			zones = append(zones, secondary)
		}
	}
	if pending := instance.PendingModifications; pending != nil && pending.CapacityReservationChange &&
		pending.DBCapacityReservationIdentifier == reservation {
		for _, zone := range []string{instance.AvailabilityZone, pending.CapacityReservationSecondaryZone} {
			if zone != "" && !slices.Contains(zones, zone) {
				zones = append(zones, zone)
			}
		}
	}
	return zones
}

// rdsReservedUnits counts the units of reservation in use in zone, leaving
// out the instance named except, whose units the caller counts itself. Hold
// rdsCapacity.
func rdsReservedUnits(reservation, zone, except string) int {
	used := 0
	for _, instance := range rdsInstances.List() {
		if instance.DBInstanceIdentifier == except {
			continue
		}
		if hold, held := rdsCapacityHolds[instance.DBInstanceIdentifier]; held && hold.Reservation == reservation {
			continue
		}
		if slices.Contains(rdsInstanceReservedZones(instance, reservation), zone) {
			used++
		}
	}
	for id, hold := range rdsCapacityHolds {
		if id != except && hold.Reservation == reservation && slices.Contains(hold.Zones, zone) {
			used++
		}
	}
	return used
}

// rdsAvailableCapacity is the capacity of res that remains available in zone,
// which Amazon RDS reports only while the reservation is active. Hold
// rdsCapacity.
func rdsAvailableCapacity(res RDSCapacityReservation, zone, except string, now time.Time) int {
	if rdsCapacityReservationState(res, now) != rdsCapacityReservationActive {
		return 0
	}
	return max(res.CapacityPerZone-rdsReservedUnits(res.Identifier, zone, except), 0)
}

func renderRDSCapacityReservation(res RDSCapacityReservation, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<DBCapacityReservationName>%s</DBCapacityReservationName>", xmlEscape(res.Name))
	fmt.Fprintf(&b, "<DBCapacityReservationIdentifier>%s</DBCapacityReservationIdentifier>", xmlEscape(res.Identifier))
	fmt.Fprintf(&b, "<DBCapacityReservationArn>%s</DBCapacityReservationArn>", xmlEscape(res.ARN))
	fmt.Fprintf(&b, "<DBInstanceClass>%s</DBInstanceClass>", xmlEscape(res.DBInstanceClass))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(res.Engine))
	if res.LicenseModel != "" {
		fmt.Fprintf(&b, "<LicenseModel>%s</LicenseModel>", xmlEscape(res.LicenseModel))
	}
	fmt.Fprintf(&b, "<ReservedCapacityPerAvailabilityZone>%d</ReservedCapacityPerAvailabilityZone>", res.CapacityPerZone)
	b.WriteString("<AvailableCapacityPerAvailabilityZoneList>")
	for _, zone := range res.AvailabilityZones {
		fmt.Fprintf(&b, "<AvailableCapacityPerAvailabilityZone><AvailabilityZone>%s</AvailabilityZone><AvailableCapacity>%d</AvailableCapacity></AvailableCapacityPerAvailabilityZone>",
			xmlEscape(zone), rdsAvailableCapacity(res, zone, "", now))
	}
	b.WriteString("</AvailableCapacityPerAvailabilityZoneList>")
	if !res.EndDate.IsZero() {
		fmt.Fprintf(&b, "<EndDate>%s</EndDate>", rdsTimestamp(res.EndDate))
	}
	fmt.Fprintf(&b, "<CreateDate>%s</CreateDate>", rdsTimestamp(res.CreateDate))
	if !res.StartDate.IsZero() {
		fmt.Fprintf(&b, "<StartDate>%s</StartDate>", rdsTimestamp(res.StartDate))
	}
	if !res.CommitmentEndDate.IsZero() {
		fmt.Fprintf(&b, "<CommitmentInfo><CommittedCapacityPerAvailabilityZone>%d</CommittedCapacityPerAvailabilityZone><CommitmentEndDate>%s</CommitmentEndDate></CommitmentInfo>",
			res.CommittedCapacityPerAZ, rdsTimestamp(res.CommitmentEndDate))
	}
	fmt.Fprintf(&b, "<State>%s</State>", rdsCapacityReservationState(res, now))
	b.WriteString(renderRDSTagList(res.Tags))
	return b.String()
}

func rdsTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// rdsRequestTime reads an optional timestamp member.
func rdsRequestTime(r *http.Request, name string) (time.Time, string) {
	value := r.FormValue(name)
	if value == "" {
		return time.Time{}, ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Sprintf("Invalid value %q for %s: it is not a valid timestamp.", value, name)
	}
	return parsed.UTC(), ""
}

// rdsCapacityReservationLicenseModel is the license model a reservation for
// engine holds when the request names requested, as the
// CreateDBCapacityReservation reference states it per engine.
func rdsCapacityReservationLicenseModel(engine, requested string) (license, code, message string) {
	var allowed []string
	defaulted := ""
	switch engine {
	case "sqlserver-ee", "sqlserver-se":
		allowed, defaulted = []string{"license-included", "bring-your-own-media"}, "license-included"
	case "sqlserver-dev-ee", "sqlserver-dev-se":
		allowed, defaulted = []string{"bring-your-own-license"}, "bring-your-own-license"
	case "oracle-ee", "oracle-ee-cdb":
		if requested == "license-included" {
			return "", "DBCapacityReservationUnsupported", "RDS for Oracle Enterprise Edition doesn't support the license-included license model."
		}
		allowed, defaulted = []string{"bring-your-own-license"}, "bring-your-own-license"
	case "oracle-se2", "oracle-se2-cdb":
		allowed = []string{"bring-your-own-license", "license-included"}
		if requested == "" {
			return "", "InvalidParameterCombination",
				fmt.Sprintf("More than one license model is orderable for %s; specify LicenseModel.", engine)
		}
	case "db2-se", "db2-ae":
		allowed, defaulted = []string{"bring-your-own-license", "marketplace-license"}, "bring-your-own-license"
	default:
		if requested != "" {
			return "", "InvalidParameterCombination", fmt.Sprintf("LicenseModel doesn't apply to a DB capacity reservation for %s.", engine)
		}
		return "", "", ""
	}
	if requested == "" {
		return defaulted, "", ""
	}
	if !slices.Contains(allowed, requested) {
		return "", "InvalidParameterValue", fmt.Sprintf("Invalid LicenseModel %q for %s. Valid values: %s.", requested, engine, strings.Join(allowed, " | "))
	}
	return requested, "", ""
}

// rdsFutureDatedClassProblem answers why a future-dated reservation cannot
// reserve class at capacity per Availability Zone, or "" when it can.
func rdsFutureDatedClassProblem(class string, capacity int) string {
	family, rest := class[len("db.")], class[len("db.")+1:]
	if !strings.ContainsRune("cmrtx", rune(family)) {
		return fmt.Sprintf("A future-dated reservation reserves only a db.c, db.m, db.r, db.t or db.x DB instance class, not %s.", class)
	}
	if family == 'm' || family == 'r' {
		digits := len(rest) - len(strings.TrimLeft(rest, "0123456789"))
		generation, err := strconv.Atoi(rest[:digits])
		if err != nil || generation < 7 {
			return fmt.Sprintf("A future-dated reservation of a db.m or db.r DB instance class needs generation 7 or later, not %s.", class)
		}
	}
	// A DB instance class runs on the Amazon EC2 instance type of the same
	// name, whose vCPUs the vendored Price List states.
	facts, ok := ec2LookupInstanceType(strings.TrimPrefix(class, "db."))
	if !ok {
		return fmt.Sprintf("Amazon RDS doesn't know the vCPUs of DB instance class %s.", class)
	}
	if facts.VCpus*capacity < 32 {
		return fmt.Sprintf("A future-dated reservation must reserve at least 32 vCPUs in each Availability Zone; %d %s DB instances have %d.",
			capacity, class, facts.VCpus*capacity)
	}
	return ""
}

func handleRDSCreateCapacityReservation(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	fail := func(code, message string) { rdsErrorXML(w, code, message, rdsCapacityErrorStatus(code), requestID) }
	now := time.Now().UTC()
	for _, member := range []string{"DBCapacityReservationName", "DBInstanceClass", "Engine", "CapacityPerAvailabilityZone"} {
		if r.FormValue(member) == "" {
			fail("MissingParameter", fmt.Sprintf("The parameter %s must be provided and must not be blank.", member))
			return
		}
	}
	name, class, engine := r.FormValue("DBCapacityReservationName"), r.FormValue("DBInstanceClass"), r.FormValue("Engine")
	if len(name) > 63 || !rdsCapacityReservationNamePattern.MatchString(name) || strings.HasPrefix(name, "dbcr-") {
		fail("InvalidParameterValue", fmt.Sprintf("Invalid DBCapacityReservationName %q: it must hold 1 to 63 lowercase letters, digits or hyphens, "+
			"start with a letter, not end with a hyphen or hold two consecutive hyphens, and not begin with dbcr-.", name))
		return
	}
	if !rdsDBInstanceClassPattern.MatchString(class) {
		fail("InvalidParameterValue", fmt.Sprintf("Invalid DB instance class %q.", class))
		return
	}
	if !slices.Contains(rdsCapacityReservationEngines, engine) {
		fail("InvalidParameterValue", fmt.Sprintf("Invalid engine %q for a DB capacity reservation.", engine))
		return
	}
	capacity, err := strconv.Atoi(r.FormValue("CapacityPerAvailabilityZone"))
	if err != nil || capacity < 1 {
		fail("InvalidParameterValue", "CapacityPerAvailabilityZone must be 1 or more.")
		return
	}
	zones := parseRDSIndexedStringList(r, "AvailabilityZones.AvailabilityZone")
	if len(zones) == 0 {
		fail("InvalidParameterValue", "Specify one or more Availability Zones.")
		return
	}
	for i, zone := range zones {
		if slices.Contains(zones[:i], zone) {
			fail("InvalidParameterValue", fmt.Sprintf("Availability Zone %s appears more than once.", zone))
			return
		}
		if !slices.Contains(rdsRegionAvailabilityZones(), zone) {
			fail("InvalidParameterValue", fmt.Sprintf("Availability Zone %s isn't in Region %s.", zone, awsRegion()))
			return
		}
	}
	license, code, message := rdsCapacityReservationLicenseModel(engine, r.FormValue("LicenseModel"))
	if code != "" {
		fail(code, message)
		return
	}
	start, problem := rdsRequestTime(r, "StartDate")
	if problem != "" {
		fail("InvalidParameterValue", problem)
		return
	}
	end, problem := rdsRequestTime(r, "EndDate")
	if problem != "" {
		fail("InvalidParameterValue", problem)
		return
	}
	if !end.IsZero() && !end.After(now) {
		fail("InvalidParameterValue", "EndDate must be a date and time in the future.")
		return
	}
	res := RDSCapacityReservation{
		Identifier:        "dbcr-" + strings.ReplaceAll(sim.NewUUID(), "-", ""),
		Name:              name,
		DBInstanceClass:   class,
		Engine:            engine,
		LicenseModel:      license,
		CapacityPerZone:   capacity,
		AvailabilityZones: zones,
		State:             rdsCapacityReservationActive,
		CreateDate:        now,
		StartDate:         start,
		EndDate:           end,
		Tags:              parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	res.ARN = rdsCapacityReservationARN(res.Identifier)
	durationValue := r.FormValue("CommitmentDuration")
	if start.IsZero() != (durationValue == "") {
		fail("InvalidParameterCombination", "Specify StartDate and CommitmentDuration together to create a future-dated reservation.")
		return
	}
	if !start.IsZero() {
		if code, message := rdsFutureDated(&res, durationValue, now); code != "" {
			fail(code, message)
			return
		}
	}
	rdsCapacity.Lock()
	defer rdsCapacity.Unlock()
	for _, existing := range rdsCapacityReservations.List() {
		if existing.Name == name {
			fail("DBCapacityReservationAlreadyExists", fmt.Sprintf("DB capacity reservation %s already exists.", name))
			return
		}
	}
	if rdsDBInstancesUsed(awsRegion(), now)+capacity*len(zones) > rdsDBInstancesQuota {
		fail("InstanceQuotaExceeded", rdsInstanceQuotaExceededMessage)
		return
	}
	rdsCapacityReservations.Put(res.Identifier, res)
	rdsXMLResponse(w, "CreateDBCapacityReservation",
		"<DBCapacityReservation>"+renderRDSCapacityReservation(res, now)+"</DBCapacityReservation>", requestID)
}

// rdsFutureDated checks the constraints of a future-dated reservation and
// records its commitment. Amazon RDS assesses the request and schedules the
// reservation for its start date.
func rdsFutureDated(res *RDSCapacityReservation, durationValue string, now time.Time) (code, message string) {
	if res.StartDate.Before(now.Add(5*24*time.Hour)) || res.StartDate.After(now.Add(120*24*time.Hour)) {
		return "InvalidParameterValue", "StartDate must be from 5 to 120 days in the future."
	}
	duration, err := strconv.ParseInt(durationValue, 10, 64)
	minimum := int64(1209600)
	if strings.HasPrefix(res.DBInstanceClass, "db.x") {
		minimum = 7257600
	}
	if err != nil || duration < minimum || duration >= 315576000 {
		return "InvalidParameterValue", fmt.Sprintf("CommitmentDuration must be at least %d seconds and less than 315576000 for %s.", minimum, res.DBInstanceClass)
	}
	if problem := rdsFutureDatedClassProblem(res.DBInstanceClass, res.CapacityPerZone); problem != "" {
		return "DBCapacityReservationUnsupported", problem
	}
	res.CommitmentEndDate = res.StartDate.Add(time.Duration(duration) * time.Second)
	if !res.EndDate.IsZero() && res.EndDate.Before(res.CommitmentEndDate) {
		return "InvalidParameterValue", "EndDate must be at or after StartDate plus CommitmentDuration."
	}
	res.CommittedCapacityPerAZ = res.CapacityPerZone
	res.State = rdsCapacityReservationScheduled
	return "", ""
}

func rdsCapacityErrorStatus(code string) int {
	if code == "DBCapacityReservationNotFound" {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// rdsCapacityReservationFilters reads DescribeDBCapacityReservations' filters
// by name, refusing the names and values Amazon RDS refuses.
func rdsCapacityReservationFilters(r *http.Request) (map[string][]string, string) {
	filters := map[string][]string{}
	for n := 1; ; n++ {
		prefix := fmt.Sprintf("Filters.Filter.%d", n)
		name := r.FormValue(prefix + ".Name")
		if name == "" {
			break
		}
		switch name {
		case "db-capacity-reservation-name", "state", "engine", "db-instance-class", "availability-zone":
		default:
			return nil, fmt.Sprintf("Unrecognized filter name: %s.", name)
		}
		values := parseRDSIndexedStringList(r, prefix+".Values.Value")
		if len(values) == 0 {
			return nil, fmt.Sprintf("Filter %s specifies no values.", name)
		}
		for _, value := range values {
			if name == "state" && (value == rdsCapacityReservationExpired || !slices.Contains(rdsCapacityReservationStates, value)) {
				return nil, fmt.Sprintf("Invalid value %q for filter state.", value)
			}
		}
		filters[name] = append(filters[name], values...)
	}
	return filters, ""
}

func rdsCapacityReservationMatches(res RDSCapacityReservation, filters map[string][]string, now time.Time) bool {
	for name, values := range filters {
		var matched bool
		switch name {
		case "db-capacity-reservation-name":
			matched = slices.Contains(values, res.Name)
		case "state":
			matched = slices.Contains(values, rdsCapacityReservationState(res, now))
		case "engine":
			matched = slices.Contains(values, res.Engine)
		case "db-instance-class":
			matched = slices.Contains(values, res.DBInstanceClass)
		case "availability-zone":
			matched = slices.ContainsFunc(values, func(zone string) bool { return slices.Contains(res.AvailabilityZones, zone) })
		}
		if !matched {
			return false
		}
	}
	return true
}

// rdsCapacityReservationMarker is the opaque position a Marker names: the
// creation time and identifier of the first reservation of the next page.
func rdsCapacityReservationMarker(res RDSCapacityReservation) string {
	return base64.RawURLEncoding.EncodeToString([]byte(res.CreateDate.Format(time.RFC3339Nano) + "|" + res.Identifier))
}

func handleRDSDescribeCapacityReservations(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	now := time.Now().UTC()
	maxRecords := 100
	if value := r.FormValue("MaxRecords"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 20 || n > 100 {
			rdsErrorXML(w, "InvalidParameterValue", "MaxRecords must be between 20 and 100.", http.StatusBadRequest, requestID)
			return
		}
		maxRecords = n
	}
	filters, problem := rdsCapacityReservationFilters(r)
	if problem != "" {
		rdsErrorXML(w, "InvalidParameterValue", problem, http.StatusBadRequest, requestID)
		return
	}
	var reservations []RDSCapacityReservation
	if ref := r.FormValue("DBCapacityReservationIdentifier"); ref != "" {
		res, ok := rdsFindCapacityReservation(ref)
		if !ok {
			rdsErrorXML(w, "DBCapacityReservationNotFound", fmt.Sprintf("DB capacity reservation %s not found.", ref), http.StatusNotFound, requestID)
			return
		}
		reservations = []RDSCapacityReservation{res}
	} else {
		reservations = rdsCapacityReservations.List()
	}
	sort.Slice(reservations, func(i, j int) bool {
		if !reservations[i].CreateDate.Equal(reservations[j].CreateDate) {
			return reservations[i].CreateDate.Before(reservations[j].CreateDate)
		}
		return reservations[i].Identifier < reservations[j].Identifier
	})
	from := 0
	if marker := r.FormValue("Marker"); marker != "" {
		position, err := base64.RawURLEncoding.DecodeString(marker)
		created, id, found := strings.Cut(string(position), "|")
		createdAt, parseErr := time.Parse(time.RFC3339Nano, created)
		if err != nil || !found || parseErr != nil {
			rdsErrorXML(w, "InvalidParameterValue", "The Marker is not valid.", http.StatusBadRequest, requestID)
			return
		}
		from = sort.Search(len(reservations), func(i int) bool {
			res := reservations[i]
			return res.CreateDate.After(createdAt) || res.CreateDate.Equal(createdAt) && res.Identifier >= id
		})
	}
	rdsCapacity.Lock()
	defer rdsCapacity.Unlock()
	var b strings.Builder
	b.WriteString("<DBCapacityReservations>")
	count, next := 0, ""
	for _, res := range reservations[from:] {
		if !rdsCapacityReservationMatches(res, filters, now) {
			continue
		}
		if count == maxRecords {
			next = rdsCapacityReservationMarker(res)
			break
		}
		b.WriteString("<member>" + renderRDSCapacityReservation(res, now) + "</member>")
		count++
	}
	b.WriteString("</DBCapacityReservations>")
	if next != "" {
		fmt.Fprintf(&b, "<Marker>%s</Marker>", next)
	}
	rdsXMLResponse(w, "DescribeDBCapacityReservations", b.String(), requestID)
}

func handleRDSModifyCapacityReservation(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	fail := func(code, message string) { rdsErrorXML(w, code, message, rdsCapacityErrorStatus(code), requestID) }
	now := time.Now().UTC()
	capacityValue, endDateType := r.FormValue("CapacityPerAvailabilityZone"), r.FormValue("EndDateType")
	end, problem := rdsRequestTime(r, "EndDate")
	if problem != "" {
		fail("InvalidParameterValue", problem)
		return
	}
	if capacityValue == "" && endDateType == "" && end.IsZero() {
		fail("InvalidParameterCombination", "Specify CapacityPerAvailabilityZone, EndDateType or EndDate.")
		return
	}
	switch {
	case endDateType == "" && !end.IsZero():
		fail("InvalidParameterCombination", "Specify EndDateType along with EndDate.")
		return
	case endDateType == "limited" && end.IsZero():
		fail("InvalidParameterCombination", "EndDateType limited requires EndDate.")
		return
	case endDateType == "unlimited" && !end.IsZero():
		fail("InvalidParameterCombination", "Don't specify EndDate when EndDateType is unlimited.")
		return
	case endDateType != "" && endDateType != "limited" && endDateType != "unlimited":
		fail("InvalidParameterValue", fmt.Sprintf("Invalid EndDateType %q. Valid values: limited | unlimited.", endDateType))
		return
	}
	capacity := 0
	if capacityValue != "" {
		var err error
		capacity, err = strconv.Atoi(capacityValue)
		if err != nil || capacity < 1 {
			fail("InvalidParameterValue", "CapacityPerAvailabilityZone must be 1 or more.")
			return
		}
	}
	rdsCapacity.Lock()
	defer rdsCapacity.Unlock()
	ref := r.FormValue("DBCapacityReservationIdentifier")
	res, ok := rdsFindCapacityReservation(ref)
	if !ok {
		fail("DBCapacityReservationNotFound", fmt.Sprintf("DB capacity reservation %s not found.", ref))
		return
	}
	if state := rdsCapacityReservationState(res, now); state != rdsCapacityReservationActive {
		fail("InvalidDBCapacityReservationState", fmt.Sprintf("DB capacity reservation %s is %s; only an active reservation is modifiable.", res.Identifier, state))
		return
	}
	committed := rdsInCommitment(res, now)
	if !end.IsZero() {
		if !end.After(now) {
			fail("InvalidParameterValue", "EndDate must be a date and time in the future.")
			return
		}
		if committed && end.Before(res.CommitmentEndDate) {
			fail("InvalidParameterValue", fmt.Sprintf("During the capacity commitment, EndDate must be at or after %s.", rdsTimestamp(res.CommitmentEndDate)))
			return
		}
	}
	if endDateType == "unlimited" {
		res.EndDate = time.Time{}
	} else if !end.IsZero() {
		res.EndDate = end
	}
	if capacity != 0 {
		for _, zone := range res.AvailabilityZones {
			if used := rdsReservedUnits(res.Identifier, zone, ""); capacity < used {
				fail("InvalidParameterValue", fmt.Sprintf("CapacityPerAvailabilityZone can't be less than the %d DB instances that use the reservation in %s.", used, zone))
				return
			}
		}
		if committed && capacity < res.CommittedCapacityPerAZ {
			fail("InvalidParameterValue", fmt.Sprintf("During the capacity commitment, CapacityPerAvailabilityZone can't be less than %d.", res.CommittedCapacityPerAZ))
			return
		}
		if increase := capacity - res.CapacityPerZone; increase > 0 &&
			rdsDBInstancesUsed(awsRegion(), now)+increase*len(res.AvailabilityZones) > rdsDBInstancesQuota {
			fail("InstanceQuotaExceeded", rdsInstanceQuotaExceededMessage)
			return
		}
		res.CapacityPerZone = capacity
	}
	rdsCapacityReservations.Put(res.Identifier, res)
	rdsXMLResponse(w, "ModifyDBCapacityReservation",
		"<DBCapacityReservation>"+renderRDSCapacityReservation(res, now)+"</DBCapacityReservation>", requestID)
}

func handleRDSCancelCapacityReservation(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	fail := func(code, message string) { rdsErrorXML(w, code, message, rdsCapacityErrorStatus(code), requestID) }
	now := time.Now().UTC()
	rdsCapacity.Lock()
	defer rdsCapacity.Unlock()
	ref := r.FormValue("DBCapacityReservationIdentifier")
	res, ok := rdsFindCapacityReservation(ref)
	if !ok {
		fail("DBCapacityReservationNotFound", fmt.Sprintf("DB capacity reservation %s not found.", ref))
		return
	}
	switch state := rdsCapacityReservationState(res, now); {
	case state != rdsCapacityReservationActive && state != "assessing" && state != "delayed":
		fail("InvalidDBCapacityReservationState", fmt.Sprintf("DB capacity reservation %s is %s; only an active, assessing or delayed reservation can be cancelled.", res.Identifier, state))
		return
	case state == rdsCapacityReservationActive && rdsInCommitment(res, now):
		fail("InvalidDBCapacityReservationState", fmt.Sprintf("DB capacity reservation %s is in its capacity commitment until %s.", res.Identifier, rdsTimestamp(res.CommitmentEndDate)))
		return
	}
	res.State = rdsCapacityReservationCancelled
	rdsCapacityReservations.Put(res.Identifier, res)
	rdsXMLResponse(w, "CancelDBCapacityReservation",
		"<DBCapacityReservation>"+renderRDSCapacityReservation(res, now)+"</DBCapacityReservation>", requestID)
}

// rdsInstanceCapacityReservation is the reservation the instance uses while it
// is in effect, which DBInstance reports.
func rdsInstanceCapacityReservation(instance RDSInstance) string {
	if instance.DBCapacityReservationIdentifier == "" {
		return ""
	}
	res, ok := rdsCapacityReservations.Get(instance.DBCapacityReservationIdentifier)
	if !ok {
		return ""
	}
	switch rdsCapacityReservationState(res, time.Now().UTC()) {
	case rdsCapacityReservationCancelled, rdsCapacityReservationExpired:
		return ""
	}
	return res.Identifier
}

// rdsCheckLaunchReservation checks the reservation a launch, restore or start
// of instance names against the constraints its reference states. requestedZone
// is the Availability Zone the request names, or "".
func rdsCheckLaunchReservation(res RDSCapacityReservation, instance RDSInstance, licenseModel, requestedZone string, now time.Time) (code, message string) {
	if state := rdsCapacityReservationState(res, now); state != rdsCapacityReservationActive {
		return "InvalidDBCapacityReservationState", fmt.Sprintf("DB capacity reservation %s is %s; a DB instance uses only an active reservation.", res.Identifier, state)
	}
	if res.DBInstanceClass != instance.DBInstanceClass || res.Engine != instance.Engine {
		return "InvalidParameterCombination", fmt.Sprintf("DB capacity reservation %s reserves %s %s, not %s %s.",
			res.Identifier, res.Engine, res.DBInstanceClass, instance.Engine, instance.DBInstanceClass)
	}
	if res.LicenseModel != "" && licenseModel != "" && licenseModel != res.LicenseModel {
		return "InvalidParameterCombination", fmt.Sprintf("DB capacity reservation %s reserves license model %s, not %s.", res.Identifier, res.LicenseModel, licenseModel)
	}
	if requestedZone != "" && !slices.Contains(res.AvailabilityZones, requestedZone) {
		return "InvalidParameterCombination", fmt.Sprintf("DB capacity reservation %s doesn't cover Availability Zone %s.", res.Identifier, requestedZone)
	}
	if instance.MultiAZ && !rdsIsReadReplica(instance) && len(res.AvailabilityZones) < 2 {
		return "InvalidParameterCombination", fmt.Sprintf("A Multi-AZ DB instance needs a reservation that covers two or more Availability Zones; %s covers one.", res.Identifier)
	}
	return "", ""
}

// rdsTakeLaunchCapacity places a launching instance in the reservation the
// request names: it checks the reservation, picks the Availability Zones with
// capacity the instance runs in, and holds their units until release, which
// the caller calls once it stored the instance or gave up. A request that
// names no reservation takes nothing.
func rdsTakeLaunchCapacity(r *http.Request, instance *RDSInstance, licenseModel string) (release func(), code, message string) {
	ref := r.FormValue("DBCapacityReservationIdentifier")
	if ref == "" {
		return func() {}, "", ""
	}
	if licenseModel == "" && strings.HasPrefix(instance.Engine, "db2") {
		licenseModel = "bring-your-own-license"
	}
	now := time.Now().UTC()
	rdsCapacity.Lock()
	defer rdsCapacity.Unlock()
	res, ok := rdsFindCapacityReservation(ref)
	if !ok {
		return nil, "DBCapacityReservationNotFound", fmt.Sprintf("DB capacity reservation %s not found.", ref)
	}
	requestedZone := r.FormValue("AvailabilityZone")
	if code, message := rdsCheckLaunchReservation(res, *instance, licenseModel, requestedZone, now); code != "" {
		return nil, code, message
	}
	candidates := res.AvailabilityZones
	if requestedZone != "" {
		candidates = []string{requestedZone}
	}
	var zones []string
	want := 1
	if instance.MultiAZ && !rdsIsReadReplica(*instance) {
		want = 2
		candidates = res.AvailabilityZones
	}
	for _, zone := range candidates {
		if len(zones) < want && rdsAvailableCapacity(res, zone, instance.DBInstanceIdentifier, now) > 0 {
			zones = append(zones, zone)
		}
	}
	if len(zones) < want {
		return nil, "InsufficientDBInstanceCapacity", fmt.Sprintf("DB capacity reservation %s has no available capacity for the DB instance.", res.Identifier)
	}
	instance.AvailabilityZone = zones[0]
	if want == 2 {
		instance.SecondaryAvailabilityZone = zones[1]
	}
	instance.DBCapacityReservationIdentifier = res.Identifier
	return rdsHoldCapacity(instance.DBInstanceIdentifier, rdsCapacityHold{Reservation: res.Identifier, Zones: zones}), "", ""
}

// rdsHoldCapacity records hold for the instance id. Hold rdsCapacity.
func rdsHoldCapacity(id string, hold rdsCapacityHold) func() {
	rdsCapacityHolds[id] = hold
	return func() {
		rdsCapacity.Lock()
		defer rdsCapacity.Unlock()
		delete(rdsCapacityHolds, id)
	}
}

// rdsDBInstancesQuota is the DBInstances quota of an account in a Region.
const rdsDBInstancesQuota = 40

// rdsInstanceQuotaClaims maps each DB instance a request is provisioning, and
// the store does not hold yet, to its Region. Hold rdsCapacity.
var rdsInstanceQuotaClaims = map[string]string{}

// rdsDBInstancesUsed counts what the DBInstances quota of region holds at
// now: the account's DB instances there, the ones requests are provisioning
// there, and the unused capacity of its active reservations, which the
// simulator's own Region holds. Hold rdsCapacity.
func rdsDBInstancesUsed(region string, now time.Time) int {
	used := len(rdsInstances.Filter(func(instance RDSInstance) bool { return instance.region() == region }))
	for id, claimed := range rdsInstanceQuotaClaims {
		if _, stored := rdsInstances.Get(id); !stored && claimed == region {
			used++
		}
	}
	if region != awsRegion() {
		return used
	}
	for _, res := range rdsCapacityReservations.List() {
		for _, zone := range res.AvailabilityZones {
			used += rdsAvailableCapacity(res, zone, "", now)
		}
	}
	return used
}

func rdsDBInstancesUsage(region string) int {
	rdsCapacity.Lock()
	defer rdsCapacity.Unlock()
	return rdsDBInstancesUsed(region, time.Now().UTC())
}

const rdsInstanceQuotaExceededMessage = "The request would result in the user exceeding the allowed number of DB instances."

// rdsClaimInstanceQuota takes one DB instance of region's quota for the
// instance id a request provisions. The caller releases the claim once it
// stored the instance or gave up.
func rdsClaimInstanceQuota(region, id string) (release func(), code, message string) {
	rdsCapacity.Lock()
	defer rdsCapacity.Unlock()
	if rdsDBInstancesUsed(region, time.Now().UTC())+1 > rdsDBInstancesQuota {
		return nil, "InstanceQuotaExceeded", rdsInstanceQuotaExceededMessage
	}
	rdsInstanceQuotaClaims[id] = region
	return func() {
		rdsCapacity.Lock()
		defer rdsCapacity.Unlock()
		delete(rdsInstanceQuotaClaims, id)
	}, "", ""
}

// rdsTakeStartCapacity places a stopped instance a StartDBInstance starts: in
// the reservation the request names, or in the one it kept when it stopped. A
// kept reservation that is cancelled, expired or gone lets it start on
// unreserved capacity.
func rdsTakeStartCapacity(r *http.Request, instance *RDSInstance) (release func(), code, message string) {
	ref := r.FormValue("DBCapacityReservationIdentifier")
	named := ref != ""
	if !named {
		ref = instance.DBCapacityReservationIdentifier
	}
	if ref == "" {
		return func() {}, "", ""
	}
	now := time.Now().UTC()
	rdsCapacity.Lock()
	defer rdsCapacity.Unlock()
	res, ok := rdsFindCapacityReservation(ref)
	if !named {
		if state := rdsCapacityReservationState(res, now); !ok || state == rdsCapacityReservationCancelled || state == rdsCapacityReservationExpired {
			instance.DBCapacityReservationIdentifier = ""
			return func() {}, "", ""
		}
	}
	if !ok {
		return nil, "DBCapacityReservationNotFound", fmt.Sprintf("DB capacity reservation %s not found.", ref)
	}
	if code, message := rdsCheckLaunchReservation(res, *instance, "", "", now); code != "" {
		return nil, code, message
	}
	zones := []string{instance.AvailabilityZone}
	if instance.MultiAZ && !rdsIsReadReplica(*instance) && instance.SecondaryAvailabilityZone != "" {
		zones = append(zones, instance.SecondaryAvailabilityZone)
	}
	for _, zone := range zones {
		if !slices.Contains(res.AvailabilityZones, zone) {
			return nil, "InvalidParameterCombination", fmt.Sprintf("DB capacity reservation %s doesn't cover Availability Zone %s of the DB instance.", res.Identifier, zone)
		}
		if rdsAvailableCapacity(res, zone, instance.DBInstanceIdentifier, now) == 0 {
			return nil, "InsufficientDBInstanceCapacity", fmt.Sprintf("DB capacity reservation %s has no available capacity in %s.", res.Identifier, zone)
		}
	}
	instance.DBCapacityReservationIdentifier = res.Identifier
	return rdsHoldCapacity(instance.DBInstanceIdentifier, rdsCapacityHold{Reservation: res.Identifier, Zones: zones}), "", ""
}

// rdsRequestedCapacityReservationChange reads ModifyDBInstance's
// DBCapacityReservationIdentifier into changes. A request that moves the
// instance to other hardware — a new DB instance class or Multi-AZ setting —
// moves it onto the reservation it names, or onto unreserved capacity when it
// names none. The units the move takes are held from the time Amazon RDS
// accepts it, until release.
func rdsRequestedCapacityReservationChange(r *http.Request, instance RDSInstance, changes *RDSPendingModifications, applyNow bool) (release func(), code, message string) {
	pending := RDSPendingModifications{}
	if instance.PendingModifications != nil {
		pending = *instance.PendingModifications
	}
	classAfter := instance.DBInstanceClass
	for _, class := range []string{pending.DBInstanceClass, changes.DBInstanceClass} {
		if class != "" {
			classAfter = class
		}
	}
	multiAfter := instance.MultiAZ
	for _, multi := range []*bool{pending.MultiAZ, changes.MultiAZ} {
		if multi != nil {
			multiAfter = *multi
		}
	}
	moves := changes.DBInstanceClass != "" && changes.DBInstanceClass != instance.DBInstanceClass ||
		changes.MultiAZ != nil && *changes.MultiAZ != instance.MultiAZ
	ref := r.FormValue("DBCapacityReservationIdentifier")
	if ref == "" {
		if moves && (instance.DBCapacityReservationIdentifier != "" || pending.CapacityReservationChange) {
			changes.CapacityReservationChange = true
		}
		return func() {}, "", ""
	}
	now := time.Now().UTC()
	rdsCapacity.Lock()
	defer rdsCapacity.Unlock()
	res, ok := rdsFindCapacityReservation(ref)
	if !ok {
		return nil, "DBCapacityReservationNotFound", fmt.Sprintf("DB capacity reservation %s not found.", ref)
	}
	if !moves {
		if res.Identifier == instance.DBCapacityReservationIdentifier {
			return func() {}, "", ""
		}
		return nil, "InvalidParameterCombination", "A request that changes the DB capacity reservation of a DB instance must also set a new DBInstanceClass, MultiAZ, LicenseModel or Engine."
	}
	if changes.DBSubnetGroupName != "" && changes.DBSubnetGroupName != instance.DBSubnetGroupName {
		return nil, "InvalidParameterCombination", "DBCapacityReservationIdentifier can't be specified together with a change to DBSubnetGroupName."
	}
	after := instance
	after.DBInstanceClass, after.MultiAZ = classAfter, multiAfter
	if code, message := rdsCheckLaunchReservation(res, after, "", "", now); code != "" {
		return nil, code, message
	}
	if !applyNow && !res.EndDate.IsZero() && res.EndDate.Before(now.Add(7*24*time.Hour)) {
		return nil, "InvalidParameterCombination", fmt.Sprintf("DB capacity reservation %s expires within one week; enable ApplyImmediately or choose a reservation with a later end date.", res.Identifier)
	}
	primary := instance.AvailabilityZone
	if !slices.Contains(res.AvailabilityZones, primary) {
		return nil, "InvalidParameterCombination", fmt.Sprintf("DB capacity reservation %s doesn't cover Availability Zone %s of the DB instance.", res.Identifier, primary)
	}
	zones := []string{primary}
	secondary := ""
	if multiAfter && !rdsIsReadReplica(instance) {
		if instance.MultiAZ {
			secondary = instance.SecondaryAvailabilityZone
			if secondary == "" {
				secondary = rdsStandbyAvailabilityZone(primary)
			}
			if !slices.Contains(res.AvailabilityZones, secondary) {
				return nil, "InvalidParameterCombination", fmt.Sprintf("DB capacity reservation %s doesn't cover Availability Zone %s of the DB instance's standby.", res.Identifier, secondary)
			}
		} else {
			for _, zone := range res.AvailabilityZones {
				if secondary == "" && zone != primary && rdsAvailableCapacity(res, zone, instance.DBInstanceIdentifier, now) > 0 {
					secondary = zone
				}
			}
			if secondary == "" {
				return nil, "InsufficientDBInstanceCapacity", fmt.Sprintf("DB capacity reservation %s has no available capacity for the standby.", res.Identifier)
			}
		}
		zones = append(zones, secondary)
	}
	for _, zone := range zones {
		if rdsAvailableCapacity(res, zone, instance.DBInstanceIdentifier, now) == 0 {
			return nil, "InsufficientDBInstanceCapacity", fmt.Sprintf("DB capacity reservation %s has no available capacity in %s.", res.Identifier, zone)
		}
	}
	changes.CapacityReservationChange = true
	changes.DBCapacityReservationIdentifier = res.Identifier
	changes.CapacityReservationSecondaryZone = secondary
	return rdsHoldCapacity(instance.DBInstanceIdentifier, rdsCapacityHold{Reservation: res.Identifier, Zones: zones}), "", ""
}

// rdsApplyCapacityReservationChange moves the instance onto the reservation
// an applied modification names, or off reserved capacity.
func rdsApplyCapacityReservationChange(instance *RDSInstance, changes RDSPendingModifications) {
	if !changes.CapacityReservationChange {
		return
	}
	instance.DBCapacityReservationIdentifier = changes.DBCapacityReservationIdentifier
	if changes.CapacityReservationSecondaryZone != "" {
		instance.SecondaryAvailabilityZone = changes.CapacityReservationSecondaryZone
	}
}

func findRDSCapacityReservationByARN(arn string) (RDSCapacityReservation, bool) {
	if !strings.Contains(arn, ":db-capacity-reservation:") {
		return RDSCapacityReservation{}, false
	}
	for _, res := range rdsCapacityReservations.List() {
		if res.ARN == arn {
			return res, true
		}
	}
	return RDSCapacityReservation{}, false
}
