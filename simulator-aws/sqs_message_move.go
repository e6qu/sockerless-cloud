package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
)

// Amazon SQS dead-letter-queue redrive: ListDeadLetterSourceQueues and the
// message-move task lifecycle (StartMessageMoveTask, ListMessageMoveTasks,
// CancelMessageMoveTask), over awsJson1_0. Member names follow the
// com.amazonaws.sqs Smithy model, including ListDeadLetterSourceQueues' lowercase
// `queueUrls`.

// SQSMessageMoveTask is one redrive task, moving messages from SourceArn (a
// dead-letter queue) to DestinationArn. SQS issues an opaque base64 TaskHandle;
// the simulator issues a UUID, which callers treat as equally opaque.
type SQSMessageMoveTask struct {
	TaskHandle                        string
	SourceArn                         string
	DestinationArn                    string
	Status                            string // RUNNING, COMPLETED, CANCELLING, CANCELLED, FAILED
	MaxNumberOfMessagesPerSecond      *int
	ApproximateNumberOfMessagesMoved  int64
	ApproximateNumberOfMessagesToMove int64
	FailureReason                     string
	StartedTimestamp                  int64
}

var sqsMoveTasks sim.Store[SQSMessageMoveTask]

func registerSQSMessageMove(r *AWSRouter, srv *sim.Server) {
	sqsMoveTasks = sim.MakeStore[SQSMessageMoveTask](srv.DB(), "sqs_move_tasks")

	for target, h := range map[string]http.HandlerFunc{
		"AmazonSQS.ListDeadLetterSourceQueues": handleSQSListDeadLetterSourceQueues,
		"AmazonSQS.StartMessageMoveTask":       handleSQSStartMessageMoveTask,
		"AmazonSQS.ListMessageMoveTasks":       handleSQSListMessageMoveTasks,
		"AmazonSQS.CancelMessageMoveTask":      handleSQSCancelMessageMoveTask,
	} {
		r.Register(target, h)
	}
}

// sqsRedriveTargetARN returns the deadLetterTargetArn of the queue's
// RedrivePolicy, a JSON string such as
// {"deadLetterTargetArn":"arn:aws:sqs:...:dlq","maxReceiveCount":"3"}, or "".
func sqsRedriveTargetARN(q SQSQueue) string {
	raw := q.Attributes["RedrivePolicy"]
	if raw == "" {
		return ""
	}
	var policy struct {
		DeadLetterTargetArn string `json:"deadLetterTargetArn"`
	}
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return ""
	}
	return policy.DeadLetterTargetArn
}

// sqsSourceQueuesForDLQ returns the queues whose RedrivePolicy targets the named
// dead-letter queue, sorted by URL for a deterministic page order.
func sqsSourceQueuesForDLQ(dlqARN string) []SQSQueue {
	var out []SQSQueue
	for _, q := range sqsQueues.List() {
		if sqsRedriveTargetARN(q) == dlqARN {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// handleSQSListDeadLetterSourceQueues lists the queues whose
// RedrivePolicy.deadLetterTargetArn is the ARN of the dead-letter queue QueueUrl
// names.
func handleSQSListDeadLetterSourceQueues(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl   string `json:"QueueUrl"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	dlq, ok := sqsQueues.Get(queueNameFromURL(req.QueueUrl))
	if !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	sources := sqsSourceQueuesForDLQ(dlq.ARN)
	page, next, pageOK := awsKeyPage(w, sqsBadToken, sources, func(it SQSQueue) string { return it.URL }, req.NextToken, req.MaxResults, 1000)
	if !pageOK {
		return
	}
	urls := make([]string, 0, len(page))
	for _, q := range page {
		urls = append(urls, q.URL)
	}
	out := map[string]any{"queueUrls": urls}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// sqsQueueByARN resolves a queue from an ARN, which the move-task operations take
// in place of a queue URL.
func sqsQueueByARN(arn string) (SQSQueue, bool) {
	name := arn
	if i := strings.LastIndex(arn, ":"); i >= 0 {
		name = arn[i+1:]
	}
	return sqsQueues.Get(name)
}

// sqsResourceNotFound writes the ResourceNotFoundException SQS raises when a
// move-task operation names an unknown queue or task.
func sqsResourceNotFound(w http.ResponseWriter, message string) {
	sqsErrorJSON(w, "ResourceNotFoundException", message, http.StatusNotFound)
}

// handleSQSStartMessageMoveTask moves every message in the source dead-letter
// queue to DestinationArn, or without one to the source queue whose
// RedrivePolicy targets it, and settles the task COMPLETED before returning.
// SQS moves messages asynchronously at MaxNumberOfMessagesPerSecond; the
// simulator does not.
func handleSQSStartMessageMoveTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceArn                    string `json:"SourceArn"`
		DestinationArn               string `json:"DestinationArn"`
		MaxNumberOfMessagesPerSecond *int   `json:"MaxNumberOfMessagesPerSecond"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.SourceArn == "" {
		sqsErrorJSON(w, "MissingParameter", "SourceArn is required", http.StatusBadRequest)
		return
	}
	source, ok := sqsQueueByARN(req.SourceArn)
	if !ok {
		sqsResourceNotFound(w, "The resource that you specified for the SourceArn parameter doesn't exist.")
		return
	}

	// SQS requires the source to be a dead-letter queue with at least one source queue.
	destArn := req.DestinationArn
	if destArn == "" {
		sources := sqsSourceQueuesForDLQ(source.ARN)
		if len(sources) == 0 {
			sqsErrorJSON(w, "InvalidParameterValue",
				"Source queue must be configured as a dead-letter queue of at least one source queue to start a message move task without a destination.",
				http.StatusBadRequest)
			return
		}
		destArn = sources[0].ARN
	}
	dest, ok := sqsQueueByARN(destArn)
	if !ok {
		sqsResourceNotFound(w, "The resource that you specified for the DestinationArn parameter doesn't exist.")
		return
	}

	// SQS enqueues each redriven message anew: a new message ID, enqueue timestamp,
	// FIFO sequence number and receive state, and the destination's delivery delay.
	var moved []msgq.Message[sqsPayload]
	sqsQueues.Update(source.Name, func(q *SQSQueue) {
		moved = q.Messages.Remove(func(msgq.Message[sqsPayload]) bool { return true })
	})
	for _, m := range moved {
		sqsEnqueue(dest.Name, sqsSendEntry{
			MessageBody:            m.Payload.Body,
			MessageAttributes:      m.Payload.MessageAttributes,
			MessageGroupId:         m.Group,
			MessageDeduplicationId: m.DedupID,
		})
	}

	task := SQSMessageMoveTask{
		TaskHandle:                        sim.NewUUID(),
		SourceArn:                         source.ARN,
		Status:                            "COMPLETED",
		MaxNumberOfMessagesPerSecond:      req.MaxNumberOfMessagesPerSecond,
		ApproximateNumberOfMessagesMoved:  int64(len(moved)),
		ApproximateNumberOfMessagesToMove: int64(len(moved)),
		StartedTimestamp:                  time.Now().UnixMilli(),
	}
	// The model leaves DestinationArn null when StartMessageMoveTask omitted it.
	if req.DestinationArn != "" {
		task.DestinationArn = dest.ARN
	}
	sqsMoveTasks.Put(task.TaskHandle, task)

	sim.WriteJSON(w, http.StatusOK, map[string]any{"TaskHandle": task.TaskHandle})
}

// handleSQSListMessageMoveTasks lists a source ARN's tasks newest first, up to
// MaxResults (default 1, at most 10). Only a RUNNING task carries its TaskHandle.
func handleSQSListMessageMoveTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceArn  string `json:"SourceArn"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.SourceArn == "" {
		sqsErrorJSON(w, "MissingParameter", "SourceArn is required", http.StatusBadRequest)
		return
	}
	max := req.MaxResults
	if max <= 0 {
		max = 1
	}
	if max > 10 {
		max = 10
	}

	var tasks []SQSMessageMoveTask
	for _, t := range sqsMoveTasks.List() {
		if t.SourceArn == req.SourceArn {
			tasks = append(tasks, t)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].StartedTimestamp > tasks[j].StartedTimestamp })
	if len(tasks) > max {
		tasks = tasks[:max]
	}

	results := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		entry := map[string]any{
			"Status":                           t.Status,
			"SourceArn":                        t.SourceArn,
			"ApproximateNumberOfMessagesMoved": t.ApproximateNumberOfMessagesMoved,
			"StartedTimestamp":                 t.StartedTimestamp,
		}
		if t.Status == "RUNNING" {
			entry["TaskHandle"] = t.TaskHandle
		}
		if t.DestinationArn != "" {
			entry["DestinationArn"] = t.DestinationArn
		}
		if t.MaxNumberOfMessagesPerSecond != nil {
			entry["MaxNumberOfMessagesPerSecond"] = *t.MaxNumberOfMessagesPerSecond
		}
		entry["ApproximateNumberOfMessagesToMove"] = t.ApproximateNumberOfMessagesToMove
		if t.FailureReason != "" {
			entry["FailureReason"] = t.FailureReason
		}
		results = append(results, entry)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Results": results})
}

// handleSQSCancelMessageMoveTask cancels a RUNNING task and returns the count
// already moved; SQS answers ResourceNotFoundException for a settled task.
func handleSQSCancelMessageMoveTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskHandle string `json:"TaskHandle"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.TaskHandle == "" {
		sqsErrorJSON(w, "MissingParameter", "TaskHandle is required", http.StatusBadRequest)
		return
	}
	task, ok := sqsMoveTasks.Get(req.TaskHandle)
	if !ok {
		sqsResourceNotFound(w, "The resource that you specified for the TaskHandle parameter doesn't exist.")
		return
	}
	if task.Status != "RUNNING" {
		sqsErrorJSON(w, "ResourceNotFoundException",
			fmt.Sprintf("Task with TaskHandle == %s is in %s state and cannot be cancelled.", req.TaskHandle, task.Status),
			http.StatusNotFound)
		return
	}
	task.Status = "CANCELLED"
	sqsMoveTasks.Put(task.TaskHandle, task)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ApproximateNumberOfMessagesMoved": task.ApproximateNumberOfMessagesMoved,
	})
}
