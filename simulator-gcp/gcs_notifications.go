package main

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Cloud Storage notification event types.
const (
	gcsEventFinalize       = "OBJECT_FINALIZE"
	gcsEventMetadataUpdate = "OBJECT_METADATA_UPDATE"
	gcsEventDelete         = "OBJECT_DELETE"
)

func gcsNotificationKey(bucket, id string) string { return bucket + "\x00" + id }

// gcsBucketNotifications returns a bucket's notification configurations in id
// order.
func gcsBucketNotifications(bucket string) []GCSNotification {
	rows := gcsNotifications.ListPrefix(gcsNotificationKey(bucket, ""))
	out := make([]GCSNotification, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Item)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i].ID)
		b, _ := strconv.Atoi(out[j].ID)
		return a < b
	})
	return out
}

// gcsNextNotificationID is one past the highest id the bucket's configurations
// hold, so an id is never handed out twice while its configuration exists.
func gcsNextNotificationID(bucket string) string {
	next := 1
	for _, n := range gcsBucketNotifications(bucket) {
		if id, err := strconv.Atoi(n.ID); err == nil && id >= next {
			next = id + 1
		}
	}
	return strconv.Itoa(next)
}

// gcsNotificationTopicPattern is the topic a notification configuration names:
// a Pub/Sub topic whose ID follows Pub/Sub's rules, as its full resource name
// or as the relative name gcloud sends.
var gcsNotificationTopicPattern = regexp.MustCompile(
	`^(?://pubsub\.googleapis\.com/)?(projects/[a-z0-9.:-]+/topics/[A-Za-z][A-Za-z0-9._~+%-]{2,254})$`)

// gcsNotificationTopicName is the Pub/Sub topic name a notification's topic
// field names, and whether the field is well formed.
func gcsNotificationTopicName(topic string) (string, bool) {
	m := gcsNotificationTopicPattern.FindStringSubmatch(topic)
	if m == nil || strings.HasPrefix(m[1][strings.LastIndex(m[1], "/")+1:], "goog") {
		return "", false
	}
	return m[1], true
}

// gcsServiceAgentEmail is Cloud Storage's service agent for the project with
// the given number.
func gcsServiceAgentEmail(projectNumber string) string {
	return "service-" + projectNumber + "@gs-project-accounts.iam.gserviceaccount.com"
}

// gcsResolveProject resolves a project ID or number through Cloud Resource
// Manager, answering Cloud Storage's refusal for a project that does not exist
// or is not active.
func gcsResolveProject(w http.ResponseWriter, ref string) (CRMProject, bool) {
	p, ok := crmResolveProject(ref)
	if !ok || p.State != "ACTIVE" {
		writeGCSJSONError(w, http.StatusBadRequest, "invalid", "Unknown project id: "+ref)
		return CRMProject{}, false
	}
	return p, true
}

// gcsAgentMayPublish reports whether the topic exists and Cloud Storage's
// service agent holds pubsub.topics.publish on it, through the topic's policy
// or its project's.
func gcsAgentMayPublish(agent, topicName string) bool {
	if _, exists := psTopics.Get(topicName); !exists {
		return false
	}
	project := strings.TrimPrefix(topicName[:strings.Index(topicName, "/topics/")], "projects/")
	topicPolicy, _ := gcpResourcePolicies.Get(topicName)
	held := gcpPermissionsHeldUnder("serviceAccount:"+agent, false,
		[]IAMPolicy{gcpProjectPolicy(project), topicPolicy}, []string{"pubsub.topics.publish"}, gcpIAMResourceNamed(topicName))
	return len(held) == 1
}

// gcsNotificationPayload is the object resource a JSON_API_V1 notification
// carries, with the links Cloud Storage writes into it.
func gcsNotificationPayload(obj GCSObject) map[string]any {
	return gcsObjectMetadata(&http.Request{Host: "www.googleapis.com", URL: &url.URL{}}, obj)
}

// gcsNotifyWrite publishes a new generation of an object. The generation it
// replaced, if any, is deleted by the same write.
func gcsNotifyWrite(obj, replaced GCSObject, existed bool) {
	if !existed {
		gcsNotify(gcsEventFinalize, obj, nil)
		return
	}
	gcsNotify(gcsEventFinalize, obj, map[string]string{"overwroteGeneration": replaced.Generation})
	gcsNotify(gcsEventDelete, replaced, map[string]string{"overwrittenByGeneration": obj.Generation})
}

// gcsNotify publishes a change to an object to the Pub/Sub topic of every
// notification configuration on its bucket that selects the event type and the
// object's name. extra carries the event's own attributes, such as
// overwroteGeneration.
func gcsNotify(eventType string, obj GCSObject, extra map[string]string) {
	configs := gcsBucketNotifications(obj.Bucket)
	if len(configs) == 0 {
		return
	}
	eventTime := obj.Updated
	if eventTime == "" {
		eventTime = gcsTimestamp()
	}
	var payload []byte
	for _, n := range configs {
		if len(n.EventTypes) > 0 && !slices.Contains(n.EventTypes, eventType) {
			continue
		}
		if !strings.HasPrefix(obj.Name, n.ObjectNamePrefix) {
			continue
		}
		attrs := map[string]string{
			"notificationConfig": "projects/_/buckets/" + obj.Bucket + "/notificationConfigs/" + n.ID,
			"eventType":          eventType,
			"payloadFormat":      n.PayloadFormat,
			"bucketId":           obj.Bucket,
			"objectId":           obj.Name,
			"objectGeneration":   obj.Generation,
			"eventTime":          eventTime,
		}
		for k, v := range extra {
			attrs[k] = v
		}
		for k, v := range n.CustomAttributes {
			attrs[k] = v
		}
		message := PSMessage{Attributes: attrs}
		if n.PayloadFormat == "JSON_API_V1" {
			if payload == nil {
				body, err := json.Marshal(gcsNotificationPayload(obj))
				if err != nil {
					log.Printf("cloud storage: notification payload for %s/%s: %v", obj.Bucket, obj.Name, err)
					return
				}
				payload = body
			}
			message.Data = base64.StdEncoding.EncodeToString(payload)
		}
		topic := strings.TrimPrefix(n.Topic, "//pubsub.googleapis.com/")
		if _, err := psPublishMessages(topic, []PSMessage{message}); err != nil {
			log.Printf("cloud storage: notification %s on bucket %s: %v", n.ID, obj.Bucket, err)
		}
	}
}
