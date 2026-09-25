package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Future-dated Capacity Reservations and their date-change quotes.
// https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/cr-concepts.html
// https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/capacity-reservations-modify.html#cr-delay-start-date

const (
	ec2CapacityScheduled = "scheduled"
	ec2CapacityActive    = "active"

	ec2FutureDatedMinLead      = 5 * 24 * time.Hour
	ec2FutureDatedMaxLead      = 120 * 24 * time.Hour
	ec2MinCommitment           = 14 * 24 * time.Hour
	ec2MinCommitmentUAndX      = 12 * 7 * 24 * time.Hour
	ec2MaxCumulativePushout    = 30 * 24 * time.Hour
	ec2PushoutCommitmentWindow = 14 * 24 * time.Hour
	ec2QuoteValidity           = 24 * time.Hour
	ec2QuoteStartMargin        = time.Hour
)

var ec2CapacityQuotes sim.Store[EC2CapacityReservationQuote]

// EC2CapacityReservationQuote is a date-change quote: the terms on which a
// scheduled reservation's start date moves, usable once, until it expires.
type EC2CapacityReservationQuote struct {
	QuoteId               string
	CapacityReservationId string
	CreateTime            string
	ExpirationTime        string
	InstanceCount         int
	ReservationState      string
	StartDate             string
	OriginalStartDate     string
	NewStartDate          string
	NewCommitmentDuration int64
	NewCommitmentEndDate  string
	Used                  bool
	ClientToken           string
	Tags                  []EC2Tag
}

type ec2Problem struct{ code, message string }

func ec2Problemf(code, format string, args ...any) *ec2Problem {
	return &ec2Problem{code: code, message: fmt.Sprintf(format, args...)}
}

const ec2MilliLayout = "2006-01-02T15:04:05.000Z"

func ec2FormatMilli(t time.Time) string { return t.UTC().Format(ec2MilliLayout) }

func ec2ParseInstant(raw string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, raw)
	return t, err == nil
}

type ec2FutureDated struct {
	start      string
	commitment int64
}

// ec2FutureDatedRequest reads a CreateCapacityReservation request's
// future-dated terms: a start between 5 and 120 days ahead, a commitment of at
// least 14 days (12 weeks for U and X instances), targeted matching only, and
// no automatic end inside the commitment. A request with neither StartDate nor
// CommitmentDuration is for immediate use.
func ec2FutureDatedRequest(r *http.Request, matchCriteria, endDateType string, now time.Time) (ec2FutureDated, *ec2Problem) {
	rawStart, rawCommitment := r.FormValue("StartDate"), r.FormValue("CommitmentDuration")
	if rawStart == "" && rawCommitment == "" {
		return ec2FutureDated{}, nil
	}
	if rawStart == "" || rawCommitment == "" {
		return ec2FutureDated{}, ec2Problemf("InvalidParameterCombination",
			"A future-dated Capacity Reservation requires both StartDate and CommitmentDuration.")
	}
	start, ok := ec2ParseInstant(rawStart)
	if !ok {
		return ec2FutureDated{}, ec2Problemf("InvalidParameterValue", "Value (%s) for parameter StartDate is invalid.", rawStart)
	}
	if lead := start.Sub(now); lead < ec2FutureDatedMinLead || lead > ec2FutureDatedMaxLead {
		return ec2FutureDated{}, ec2Problemf("InvalidParameterValue",
			"StartDate must be between 5 and 120 days in the future.")
	}
	seconds, err := strconv.ParseInt(rawCommitment, 10, 64)
	if err != nil || seconds <= 0 {
		return ec2FutureDated{}, ec2Problemf("InvalidParameterValue", "Value (%s) for parameter CommitmentDuration is invalid.", rawCommitment)
	}
	minimum := ec2MinCommitment
	if family := strings.SplitN(r.FormValue("InstanceType"), ".", 2)[0]; strings.HasPrefix(family, "u-") || strings.HasPrefix(family, "x") {
		minimum = ec2MinCommitmentUAndX
	}
	if time.Duration(seconds)*time.Second < minimum {
		return ec2FutureDated{}, ec2Problemf("InvalidParameterValue",
			"CommitmentDuration must be at least %d seconds for instance type %s.", int64(minimum/time.Second), r.FormValue("InstanceType"))
	}
	if matchCriteria != "targeted" {
		return ec2FutureDated{}, ec2Problemf("InvalidParameterValue",
			"A future-dated Capacity Reservation accepts only targeted InstanceMatchCriteria.")
	}
	if endDateType == "limited" {
		end, ok := ec2ParseInstant(r.FormValue("EndDate"))
		if !ok || end.Before(start.Add(time.Duration(seconds)*time.Second)) {
			return ec2FutureDated{}, ec2Problemf("InvalidParameterValue",
				"EndDate must not fall before the end of the commitment duration.")
		}
	}
	return ec2FutureDated{start: ec2FormatMilli(start), commitment: seconds}, nil
}

// ec2CapacityReservationAsOf is a reservation as it stands at now: a scheduled
// future-dated reservation is delivered, and becomes active with its requested
// instances, once its start date has passed.
func ec2CapacityReservationAsOf(cr EC2CapacityReservation, now time.Time) EC2CapacityReservation {
	if cr.State != ec2CapacityScheduled {
		return cr
	}
	if start, ok := ec2ParseInstant(cr.StartDate); ok && !now.Before(start) {
		cr.State = ec2CapacityActive
		cr.TotalInstanceCount = cr.RequestedInstanceCount
		cr.AvailableInstanceCount = cr.RequestedInstanceCount
		return cr
	}
	cr.TotalInstanceCount, cr.AvailableInstanceCount = 0, 0
	return cr
}

// ec2GetCapacityReservation is every read of a reservation: the stored record
// as it stands now.
func ec2GetCapacityReservation(id string) (EC2CapacityReservation, bool) {
	cr, ok := ec2CapacityReservations.Get(id)
	if !ok {
		return cr, false
	}
	return ec2CapacityReservationAsOf(cr, time.Now()), true
}

// ec2CommitmentEnd is when a future-dated reservation's commitment lapses: the
// commitment starts when the reservation is delivered, at its start date.
func ec2CommitmentEnd(cr EC2CapacityReservation) (time.Time, bool) {
	if cr.CommitmentDuration == 0 {
		return time.Time{}, false
	}
	start, ok := ec2ParseInstant(cr.StartDate)
	if !ok {
		return time.Time{}, false
	}
	return start.Add(time.Duration(cr.CommitmentDuration) * time.Second), true
}

func ec2CapacityCommitmentXML(cr EC2CapacityReservation) string {
	var b strings.Builder
	if end, ok := ec2CommitmentEnd(cr); ok {
		fmt.Fprintf(&b, "<commitmentInfo><committedInstanceCount>%d</committedInstanceCount><commitmentEndDate>%s</commitmentEndDate><commitmentDuration>%d</commitmentDuration></commitmentInfo>",
			cr.RequestedInstanceCount, ec2FormatMilli(end), cr.CommitmentDuration)
	}
	if cr.OriginalStartDate != "" {
		fmt.Fprintf(&b, "<originalStartDate>%s</originalStartDate>", cr.OriginalStartDate)
	}
	if cr.AdjustmentStatus != "" {
		fmt.Fprintf(&b, "<adjustmentStatus>%s</adjustmentStatus>", cr.AdjustmentStatus)
	}
	return b.String()
}

// ec2ModifyCapacityReservation applies a ModifyCapacityReservation request to
// cr, refusing what the reservation's state does not allow: a scheduled
// reservation changes only its dates, by quote for its start; an active one
// inside its commitment keeps its committed count and commitment end.
func ec2ModifyCapacityReservation(cr *EC2CapacityReservation, r *http.Request, now time.Time) *ec2Problem {
	count, endDate, endDateType := r.FormValue("InstanceCount"), r.FormValue("EndDate"), r.FormValue("EndDateType")
	matchCriteria, quoteID := r.FormValue("InstanceMatchCriteria"), r.FormValue("QuoteId")
	switch cr.State {
	case ec2CapacityActive:
		if quoteID != "" {
			return ec2Problemf("IncorrectState", "A date-change quote applies only to a scheduled Capacity Reservation.")
		}
	case ec2CapacityScheduled:
		if count != "" || matchCriteria != "" {
			return ec2Problemf("IncorrectState",
				"A scheduled Capacity Reservation can change only its tags, its end date and, by quote, its start date.")
		}
	default:
		return ec2Problemf("IncorrectState", "The Capacity Reservation %s is %s and cannot be modified.", cr.CapacityReservationId, cr.State)
	}
	if r.FormValue("StartDate") != "" && quoteID == "" {
		return ec2Problemf("InvalidParameterCombination",
			"Changing StartDate requires a quote from CreateCapacityReservationDateChangeQuote.")
	}
	commitmentEnd, committed := ec2CommitmentEnd(*cr)
	committed = committed && now.Before(commitmentEnd)

	if count != "" {
		n, err := strconv.Atoi(count)
		if err != nil || n < 0 {
			return ec2Problemf("InvalidParameterValue", "Value (%s) for parameter InstanceCount is invalid.", count)
		}
		if committed && n < cr.RequestedInstanceCount {
			return ec2Problemf("InvalidParameterValue",
				"InstanceCount cannot be decreased below the committed instance count of %d during the commitment duration.", cr.RequestedInstanceCount)
		}
		cr.TotalInstanceCount, cr.AvailableInstanceCount = n, n
		if cr.RequestedInstanceCount != 0 {
			cr.RequestedInstanceCount = n
		}
	}
	if matchCriteria != "" {
		if matchCriteria != "open" && matchCriteria != "targeted" {
			return ec2Problemf("InvalidParameterValue", "Value (%s) for parameter InstanceMatchCriteria is invalid.", matchCriteria)
		}
		cr.InstanceMatchCriteria = matchCriteria
	}
	if endDateType != "" {
		cr.EndDateType = endDateType
	}
	if endDate != "" {
		cr.EndDate = endDate
	}
	if cr.EndDateType == "limited" {
		end, ok := ec2ParseInstant(cr.EndDate)
		if !ok {
			return ec2Problemf("InvalidParameterCombination", "EndDate is required when EndDateType is limited.")
		}
		if committed && end.Before(commitmentEnd) {
			return ec2Problemf("InvalidParameterValue", "EndDate must not fall before the end of the commitment duration.")
		}
	}
	if quoteID != "" {
		return ec2ApplyDateChangeQuote(cr, quoteID, r.FormValue("AcceptModificationTerms") == "true", now)
	}
	return nil
}

func ec2ApplyDateChangeQuote(cr *EC2CapacityReservation, quoteID string, accepted bool, now time.Time) *ec2Problem {
	if !accepted {
		return ec2Problemf("InvalidParameterValue", "To apply a quoted modification, set AcceptModificationTerms to true.")
	}
	quote, ok := ec2CapacityQuotes.Get(quoteID)
	if !ok || quote.CapacityReservationId != cr.CapacityReservationId {
		return ec2Problemf("InvalidParameterValue", "The quote %s does not exist for Capacity Reservation %s.", quoteID, cr.CapacityReservationId)
	}
	if ec2QuoteState(quote, now) != "active" || quote.Used {
		return ec2Problemf("InvalidParameterValue", "The quote %s is no longer active.", quoteID)
	}
	if quote.StartDate != cr.StartDate {
		return ec2Problemf("InvalidParameterValue", "The quote %s was made for a start date the Capacity Reservation no longer has.", quoteID)
	}
	cr.StartDate = quote.NewStartDate
	cr.CommitmentDuration = quote.NewCommitmentDuration
	cr.AdjustmentStatus = "applied"
	quote.Used = true
	ec2CapacityQuotes.Put(quote.QuoteId, quote)
	return nil
}

func ec2QuoteState(q EC2CapacityReservationQuote, now time.Time) string {
	if expires, ok := ec2ParseInstant(q.ExpirationTime); ok && now.Before(expires) {
		return "active"
	}
	return "expired"
}

func registerEC2CapacityReservationDateChange(r *AWSQueryRouter, srv *sim.Server) {
	ec2CapacityQuotes = sim.MakeStore[EC2CapacityReservationQuote](srv.DB(), "ec2_capacity_reservation_quotes")
	r.Register("CreateCapacityReservationDateChangeQuote", handleCreateCapacityReservationDateChangeQuote)
	r.Register("DescribeCapacityReservationDateChangeQuotes", handleDescribeCapacityReservationDateChangeQuotes)
}

// ec2QuoteTerms prices a postponement: no additional commitment when it is
// asked for more than two weeks before the current start, one more day of
// commitment per day of postponement within those two weeks.
func ec2QuoteTerms(cr EC2CapacityReservation, newStart, now time.Time) (int64, *ec2Problem) {
	current, ok := ec2ParseInstant(cr.StartDate)
	original, originalOK := ec2ParseInstant(cr.OriginalStartDate)
	if !ok || !originalOK {
		return 0, ec2Problemf("IncorrectState", "The Capacity Reservation %s is not future-dated.", cr.CapacityReservationId)
	}
	if !newStart.After(current) {
		return 0, ec2Problemf("InvalidParameterValue", "NewStartDate must be later than the current start date.")
	}
	if newStart.Sub(original) > ec2MaxCumulativePushout {
		return 0, ec2Problemf("InvalidParameterValue",
			"The start date can be postponed at most 30 days in total from the original start date.")
	}
	if current.Sub(now) < ec2QuoteStartMargin {
		return 0, ec2Problemf("IncorrectState", "A start date less than one hour away cannot be postponed.")
	}
	commitment := cr.CommitmentDuration
	if current.Sub(now) <= ec2PushoutCommitmentWindow {
		commitment += int64(newStart.Sub(current) / time.Second)
	}
	return commitment, nil
}

func handleCreateCapacityReservationDateChangeQuote(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CapacityReservationId")
	rawStart := r.FormValue("NewStartDate")
	if id == "" || rawStart == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameters CapacityReservationId and NewStartDate", http.StatusBadRequest)
		return
	}
	cr, ok := ec2GetCapacityReservation(id)
	if !ok {
		ec2ErrorXML(w, "InvalidCapacityReservationId.NotFound", fmt.Sprintf("The Capacity Reservation ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	newStart, ok := ec2ParseInstant(rawStart)
	if !ok {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter NewStartDate is invalid.", rawStart), http.StatusBadRequest)
		return
	}
	now := time.Now()
	if cr.State != ec2CapacityScheduled {
		ec2ErrorXML(w, "IncorrectState", fmt.Sprintf("The Capacity Reservation %s is %s; only a scheduled one can be postponed.", id, cr.State), http.StatusBadRequest)
		return
	}
	commitment, problem := ec2QuoteTerms(cr, newStart, now)
	if problem != nil {
		ec2ErrorXML(w, problem.code, problem.message, http.StatusBadRequest)
		return
	}
	if r.FormValue("DryRun") == "true" {
		ec2ErrorXML(w, "DryRunOperation", "Request would have succeeded, but DryRun flag is set.", http.StatusPreconditionFailed)
		return
	}
	newStartText := ec2FormatMilli(newStart)
	token := r.FormValue("ClientToken")
	if token != "" {
		for _, earlier := range ec2CapacityQuotes.List() {
			if earlier.ClientToken != token {
				continue
			}
			if earlier.CapacityReservationId != id || earlier.NewStartDate != newStartText {
				ec2ErrorXML(w, "IdempotentParameterMismatch", "The client token was already used with different parameters.", http.StatusBadRequest)
				return
			}
			ec2WriteQuoteResponse(w, earlier, now)
			return
		}
	}
	current, _ := ec2ParseInstant(cr.StartDate)
	// A quote lasts 24 hours, and always ends at least an hour before the
	// start date it would move.
	expires := now.Add(ec2QuoteValidity)
	if latest := current.Add(-ec2QuoteStartMargin); latest.Before(expires) {
		expires = latest
	}
	quote := EC2CapacityReservationQuote{
		QuoteId:               ec2ID("crmq"),
		CapacityReservationId: id,
		CreateTime:            ec2FormatMilli(now),
		ExpirationTime:        ec2FormatMilli(expires),
		InstanceCount:         cr.RequestedInstanceCount,
		ReservationState:      cr.State,
		StartDate:             cr.StartDate,
		OriginalStartDate:     cr.OriginalStartDate,
		NewStartDate:          newStartText,
		NewCommitmentDuration: commitment,
		NewCommitmentEndDate:  ec2FormatMilli(newStart.Add(time.Duration(commitment) * time.Second)),
		ClientToken:           token,
		Tags:                  ec2ParseTagSpecs(r),
	}
	ec2CapacityQuotes.Put(quote.QuoteId, quote)
	ec2WriteQuoteResponse(w, quote, now)
}

func ec2WriteQuoteResponse(w http.ResponseWriter, quote EC2CapacityReservationQuote, now time.Time) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateCapacityReservationDateChangeQuoteResponse %s><requestId>%s</requestId><capacityReservationModificationQuote>%s</capacityReservationModificationQuote></CreateCapacityReservationDateChangeQuoteResponse>`,
		ec2Xmlns(), generateUUID(), ec2QuoteFieldsXML(quote, now))
}

func ec2QuoteFieldsXML(q EC2CapacityReservationQuote, now time.Time) string {
	return fmt.Sprintf("<capacityReservationModificationQuoteId>%s</capacityReservationModificationQuoteId><capacityReservationId>%s</capacityReservationId><createTime>%s</createTime><expirationTime>%s</expirationTime><quoteState>%s</quoteState><currentConfiguration><instanceCount>%d</instanceCount><reservationState>%s</reservationState><startDate>%s</startDate><originalStartDate>%s</originalStartDate></currentConfiguration><modificationTerms><reservationUpdate><newCommitmentEndDate>%s</newCommitmentEndDate><newStartDate>%s</newStartDate><newCommitmentDuration>%d</newCommitmentDuration></reservationUpdate></modificationTerms>%s",
		q.QuoteId, q.CapacityReservationId, q.CreateTime, q.ExpirationTime, ec2QuoteState(q, now),
		q.InstanceCount, q.ReservationState, q.StartDate, q.OriginalStartDate,
		q.NewCommitmentEndDate, q.NewStartDate, q.NewCommitmentDuration, writeTagSetXML(q.Tags))
}

func handleDescribeCapacityReservationDateChangeQuotes(w http.ResponseWriter, r *http.Request) {
	maxResults := 1000
	if raw := r.FormValue("MaxResults"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 {
			ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter MaxResults is invalid. Expecting a value between 1 and 1000.", raw), http.StatusBadRequest)
			return
		}
		maxResults = n
	}
	filters := ec2Filters(r)
	for name := range filters {
		if !strings.HasPrefix(name, "tag:") && name != "tag-key" {
			ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("The filter '%s' is invalid", name), http.StatusBadRequest)
			return
		}
	}
	if r.FormValue("DryRun") == "true" {
		ec2ErrorXML(w, "DryRunOperation", "Request would have succeeded, but DryRun flag is set.", http.StatusPreconditionFailed)
		return
	}
	var quotes []EC2CapacityReservationQuote
	if ids := ec2ParamList(r, "CapacityReservationModificationQuoteId"); len(ids) > 0 {
		for _, id := range ids {
			quote, ok := ec2CapacityQuotes.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("The quote ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			quotes = append(quotes, quote)
		}
	} else {
		quotes = ec2CapacityQuotes.List()
	}
	matched := quotes[:0]
	for _, quote := range quotes {
		keep := true
		for name, vals := range filters {
			if _, match := ec2TagFilterMatch(name, vals, quote.Tags); !match {
				keep = false
			}
		}
		if keep {
			matched = append(matched, quote)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].QuoteId < matched[j].QuoteId })
	if token := r.FormValue("NextToken"); token != "" {
		start := sort.Search(len(matched), func(i int) bool { return matched[i].QuoteId > token })
		matched = matched[start:]
	}
	next := ""
	if len(matched) > maxResults {
		matched = matched[:maxResults]
		next = fmt.Sprintf("<nextToken>%s</nextToken>", matched[len(matched)-1].QuoteId)
	}
	now := time.Now()
	var items strings.Builder
	for _, quote := range matched {
		fmt.Fprintf(&items, "<item>%s</item>", ec2QuoteFieldsXML(quote, now))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeCapacityReservationDateChangeQuotesResponse %s><requestId>%s</requestId><capacityReservationModificationQuoteSet>%s</capacityReservationModificationQuoteSet>%s</DescribeCapacityReservationDateChangeQuotesResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), next)
}

// ec2CapacityReservationPage pages reservations sorted by ID: MaxResults of 1
// to 1000, and a NextToken that resumes after the last ID returned.
func ec2CapacityReservationPage(sorted []EC2CapacityReservation, rawMax, token string) ([]EC2CapacityReservation, string, *ec2Problem) {
	maxResults := 1000
	if rawMax != "" {
		n, err := strconv.Atoi(rawMax)
		if err != nil || n < 1 || n > 1000 {
			return nil, "", ec2Problemf("InvalidParameterValue", "Value (%s) for parameter MaxResults is invalid. Expecting a value between 1 and 1000.", rawMax)
		}
		maxResults = n
	}
	if token != "" {
		sorted = sorted[sort.Search(len(sorted), func(i int) bool { return sorted[i].CapacityReservationId > token }):]
	}
	if len(sorted) <= maxResults {
		return sorted, "", nil
	}
	sorted = sorted[:maxResults]
	return sorted, fmt.Sprintf("<nextToken>%s</nextToken>", sorted[len(sorted)-1].CapacityReservationId), nil
}
