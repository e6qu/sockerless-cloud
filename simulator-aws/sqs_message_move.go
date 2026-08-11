package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// SQS dead-letter-queue redrive surface: ListDeadLetterSourceQueues plus the
// message-move task lifecycle (StartMessageMoveTask / ListMessageMoveTasks /
// CancelMessageMoveTask). These let an operator redrive messages that have
// landed in a dead-letter queue back to their original source queue — the
// AWS-native alternative to writing a bespoke consumer that drains the DLQ.
//
// Wire protocol matches the rest of SQS: awsJson1_0, dispatched via
// `X-Amz-Target: AmazonSQS.<Action>` (registered through the shared awsJson
// router below). The request/response member names mirror the
// com.amazonaws.sqs Smithy model exactly — notably the response of
// ListDeadLetterSourceQueues uses the lowercase `queueUrls` member.

// SQSMessageMoveTask is one DLQ-redrive task. Real SQS keys a task by an opaque
// TaskHandle (a base64 blob); the sim uses a generated UUID, treated by every
// caller as opaque. A task moves messages from SourceArn (a dead-letter queue)
// to DestinationArn (the original source queue, or an operator-specified
// destination), reporting progress via ApproximateNumberOfMessagesMoved.
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

func registerSQSMessageMove(r *sim.AWSRouter, srv *sim.Server) {
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

// sqsRedriveTargetARN returns the deadLetterTargetArn configured on the queue's
// RedrivePolicy attribute, or "" when the queue has no RedrivePolicy. Real SQS
// stores the RedrivePolicy as a JSON string under the RedrivePolicy attribute,
// e.g. {"deadLetterTargetArn":"arn:aws:sqs:...:dlq","maxReceiveCount":"3"}.
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

// handleSQSListDeadLetterSourceQueues lists the source queues whose RedrivePolicy
// targets the given dead-letter queue. The given QueueUrl identifies the DLQ; its
// ARN is matched against every queue's RedrivePolicy.deadLetterTargetArn.
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
	page, next := awsPage(sources, req.NextToken, req.MaxResults, 1000)
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

// sqsQueueByARN resolves a queue from its ARN, mirroring the way the move-task
// ops accept ARNs (SourceArn / DestinationArn) rather than queue URLs.
func sqsQueueByARN(arn string) (SQSQueue, bool) {
	name := arn
	if i := strings.LastIndex(arn, ":"); i >= 0 {
		name = arn[i+1:]
	}
	return sqsQueues.Get(name)
}

// sqsResourceNotFound emits the ResourceNotFoundException real SQS raises for the
// message-move ops when a source/destination/task can't be resolved.
func sqsResourceNotFound(w http.ResponseWriter, message string) {
	sqsErrorJSON(w, "ResourceNotFoundException", message, http.StatusNotFound)
}

// handleSQSStartMessageMoveTask starts a DLQ-redrive task: it moves every message
// currently held in the source dead-letter queue back to the destination queue
// (the operator-specified DestinationArn, or — when blank — the original source
// queue whose RedrivePolicy targets this DLQ), then settles the task to COMPLETED.
//
// The redrive is a real store mutation: messages are dequeued from the DLQ and
// enqueued onto the destination queue, not a no-op count.
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

	// Resolve the destination: an explicit DestinationArn, else the single
	// source queue whose RedrivePolicy targets this DLQ. Real SQS requires the
	// source to be a dead-letter queue with at least one configured source.
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

	// Move every message from the source DLQ to the destination queue. Amazon
	// SQS treats each redriven message as a new enqueue, so the common enqueue
	// path assigns a new message ID, enqueue timestamp, FIFO sequence number,
	// receipt state, and the destination queue's delivery delay.
	var moved []SQSMessage
	sqsQueues.Update(source.Name, func(q *SQSQueue) {
		moved = q.Messages
		q.Messages = nil
	})
	for _, m := range moved {
		sqsEnqueue(dest.Name, sqsSendEntry{
			MessageBody:            m.Body,
			MessageAttributes:      m.MessageAttributes,
			MessageGroupId:         m.MessageGroupID,
			MessageDeduplicationId: m.MessageDeduplicationID,
		})
	}

	task := SQSMessageMoveTask{
		TaskHandle:                        generateUUID(),
		SourceArn:                         source.ARN,
		Status:                            "COMPLETED",
		MaxNumberOfMessagesPerSecond:      req.MaxNumberOfMessagesPerSecond,
		ApproximateNumberOfMessagesMoved:  int64(len(moved)),
		ApproximateNumberOfMessagesToMove: int64(len(moved)),
		StartedTimestamp:                  time.Now().UnixMilli(),
	}
	// DestinationArn is only echoed back when the caller specified it (the
	// model documents a NULL field when StartMessageMoveTask omitted it).
	if req.DestinationArn != "" {
		task.DestinationArn = dest.ARN
	}
	sqsMoveTasks.Put(task.TaskHandle, task)

	sim.WriteJSON(w, http.StatusOK, map[string]any{"TaskHandle": task.TaskHandle})
}

// handleSQSListMessageMoveTasks lists the message-move tasks for a source ARN,
// most-recent first, capped at MaxResults (default 1, upper limit 10 per the
// model). A task's TaskHandle is only populated for RUNNING tasks, matching the
// real response; the sim settles tasks to COMPLETED synchronously, so the handle
// is omitted for the COMPLETED/CANCELLED entries it returns.
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

// handleSQSCancelMessageMoveTask cancels a running message-move task and returns
// the count of messages already moved. A task that has already settled (COMPLETED
// / CANCELLED / FAILED) can't be cancelled — real SQS raises ResourceNotFoundException
// because only RUNNING tasks are cancellable.
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
