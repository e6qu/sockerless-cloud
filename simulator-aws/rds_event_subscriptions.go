package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// rdsEventCategories maps each RDS source type to its canonical set of
// event categories (as published by real RDS). Static-but-real: these
// values match the AWS-documented categories the SDK/CLI consume.
var rdsEventCategories = []struct {
	SourceType string
	Categories []string
}{
	{"db-instance", []string{"availability", "backup", "configuration change", "creation", "deletion", "failover", "failure", "maintenance", "notification", "read replica", "recovery", "restoration", "security"}},
	{"db-security-group", []string{"configuration change", "failure"}},
	{"db-parameter-group", []string{"configuration change"}},
	{"db-snapshot", []string{"creation", "deletion", "notification", "restoration"}},
	{"db-cluster", []string{"failover", "failure", "maintenance", "notification"}},
	{"db-cluster-snapshot", []string{"backup"}},
}

func handleRDSDescribeEventCategories(w http.ResponseWriter, r *http.Request) {
	wantType := r.FormValue("SourceType")
	var b strings.Builder
	b.WriteString("<EventCategoriesMapList>")
	for _, m := range rdsEventCategories {
		if wantType != "" && m.SourceType != wantType {
			continue
		}
		b.WriteString("<EventCategoriesMap>")
		fmt.Fprintf(&b, "<SourceType>%s</SourceType>", xmlEscape(m.SourceType))
		b.WriteString("<EventCategories>")
		for _, c := range m.Categories {
			fmt.Fprintf(&b, "<EventCategory>%s</EventCategory>", xmlEscape(c))
		}
		b.WriteString("</EventCategories>")
		b.WriteString("</EventCategoriesMap>")
	}
	b.WriteString("</EventCategoriesMapList>")
	rdsXMLResponse(w, "DescribeEventCategories", b.String(), sim.RequestID(r.Context()))
}

func rdsEventSubscriptionARN(name string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:es:%s", awsRegion(), awsAccountID(), name)
}

func findRDSEventSubscriptionByARN(arn string) (RDSEventSubscription, bool) {
	for _, s := range rdsEventSubscriptions.List() {
		if s.ARN == arn {
			return s, true
		}
	}
	if s, ok := rdsEventSubscriptions.Get(arn); ok {
		return s, true
	}
	return RDSEventSubscription{}, false
}

func renderRDSEventSubscription(s RDSEventSubscription) string {
	var b strings.Builder
	b.WriteString("<EventSubscription>")
	fmt.Fprintf(&b, "<CustomerAwsId>%s</CustomerAwsId>", xmlEscape(s.CustomerAwsId))
	fmt.Fprintf(&b, "<CustSubscriptionId>%s</CustSubscriptionId>", xmlEscape(s.SubscriptionName))
	fmt.Fprintf(&b, "<SnsTopicArn>%s</SnsTopicArn>", xmlEscape(s.SnsTopicArn))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(s.Status))
	fmt.Fprintf(&b, "<SubscriptionCreationTime>%s</SubscriptionCreationTime>", xmlEscape(s.SubscriptionCreateTime))
	fmt.Fprintf(&b, "<SourceType>%s</SourceType>", xmlEscape(s.SourceType))
	fmt.Fprintf(&b, "<Enabled>%t</Enabled>", s.Enabled)
	fmt.Fprintf(&b, "<EventSubscriptionArn>%s</EventSubscriptionArn>", xmlEscape(s.ARN))
	b.WriteString("<SourceIdsList>")
	for _, id := range s.SourceIds {
		fmt.Fprintf(&b, "<SourceId>%s</SourceId>", xmlEscape(id))
	}
	b.WriteString("</SourceIdsList>")
	b.WriteString("<EventCategoriesList>")
	for _, c := range s.EventCategories {
		fmt.Fprintf(&b, "<EventCategory>%s</EventCategory>", xmlEscape(c))
	}
	b.WriteString("</EventCategoriesList>")
	b.WriteString("</EventSubscription>")
	return b.String()
}

func handleRDSCreateEventSubscription(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("SubscriptionName")
	topic := r.FormValue("SnsTopicArn")
	if name == "" || topic == "" {
		rdsErrorXML(w, "MissingParameter",
			"SubscriptionName and SnsTopicArn are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsEventSubscriptions.Get(name); ok {
		rdsErrorXML(w, "SubscriptionAlreadyExistFault", "Event subscription already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	enabled := true
	if v := r.FormValue("Enabled"); v != "" {
		enabled = v == "true"
	}
	s := RDSEventSubscription{
		SubscriptionName:       name,
		CustomerAwsId:          awsAccountID(),
		SnsTopicArn:            topic,
		Status:                 "active",
		SubscriptionCreateTime: time.Now().UTC().Format(time.RFC3339),
		SourceType:             r.FormValue("SourceType"),
		Enabled:                enabled,
		SourceIds:              parseRDSIndexedStringList(r, "SourceIds.SourceId"),
		EventCategories:        parseRDSIndexedStringList(r, "EventCategories.EventCategory"),
		ARN:                    rdsEventSubscriptionARN(name),
		Tags:                   parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsEventSubscriptions.Put(name, s)
	rdsXMLResponse(w, "CreateEventSubscription", renderRDSEventSubscription(s), sim.RequestID(r.Context()))
}

func handleRDSDescribeEventSubscriptions(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("SubscriptionName")
	matched := false
	var b strings.Builder
	b.WriteString("<EventSubscriptionsList>")
	for _, s := range rdsEventSubscriptions.List() {
		if wanted != "" && s.SubscriptionName != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSEventSubscription(s))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "SubscriptionNotFoundFault",
			fmt.Sprintf("EventSubscription %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</EventSubscriptionsList>")
	rdsXMLResponse(w, "DescribeEventSubscriptions", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyEventSubscription(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("SubscriptionName")
	if _, ok := rdsEventSubscriptions.Get(name); !ok {
		rdsErrorXML(w, "SubscriptionNotFoundFault", "Event subscription not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsEventSubscriptions.Update(name, func(s *RDSEventSubscription) {
		if v := r.FormValue("SnsTopicArn"); v != "" {
			s.SnsTopicArn = v
		}
		if v := r.FormValue("SourceType"); v != "" {
			s.SourceType = v
		}
		if v := r.FormValue("Enabled"); v != "" {
			s.Enabled = v == "true"
		}
		if cats := parseRDSIndexedStringList(r, "EventCategories.EventCategory"); len(cats) > 0 {
			s.EventCategories = cats
		}
	})
	updated, _ := rdsEventSubscriptions.Get(name)
	rdsXMLResponse(w, "ModifyEventSubscription", renderRDSEventSubscription(updated), sim.RequestID(r.Context()))
}

func handleRDSDeleteEventSubscription(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("SubscriptionName")
	s, ok := rdsEventSubscriptions.Get(name)
	if !ok {
		s, ok = findRDSEventSubscriptionByARN(name)
		if !ok {
			rdsErrorXML(w, "SubscriptionNotFoundFault", "Event subscription not found", http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	rdsEventSubscriptions.Delete(s.SubscriptionName)
	s.Status = "deleting"
	rdsXMLResponse(w, "DeleteEventSubscription", renderRDSEventSubscription(s), sim.RequestID(r.Context()))
}
