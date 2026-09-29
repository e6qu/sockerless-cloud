package main

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	amqp "github.com/Azure/go-amqp"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
)

// The request-response operations a Service Bus entity's management node
// (`<entity>/$management`) answers.

func sbAMQPRPCStatus(req *amqp.Message, code int32, description string) *amqp.Message {
	var corr any
	if req.Properties != nil {
		corr = req.Properties.MessageID
	}
	return &amqp.Message{
		Properties:            &amqp.MessageProperties{CorrelationID: corr},
		ApplicationProperties: map[string]any{"status-code": code, "status-description": description},
	}
}

func sbAMQPRPCValue(req *amqp.Message, code int32, value any) *amqp.Message {
	resp := sbAMQPRPCStatus(req, code, "OK")
	resp.Value = value
	return resp
}

func sbAMQPRPCLockLost(req *amqp.Message) *amqp.Message {
	resp := sbAMQPRPCStatus(req, 410, errSBLockLost.Error())
	resp.ApplicationProperties["error-condition"] = "com.microsoft:message-lock-lost"
	return resp
}

// sbLockTokenString spells an AMQP lock token as the store's receipt.
func sbLockTokenString(id amqp.UUID) string {
	h := hex.EncodeToString(id[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func sbLockTokenUUID(token string) (amqp.UUID, bool) {
	var id amqp.UUID
	b, err := hex.DecodeString(strings.ReplaceAll(token, "-", ""))
	if err != nil || len(b) != len(id) {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

// sbLockTokenTag spells a lock token as the delivery tag Service Bus sends:
// the GUID in the .NET byte order clients read it back from.
func sbLockTokenTag(lockToken string) []byte {
	id, ok := sbLockTokenUUID(lockToken)
	if !ok {
		return []byte(lockToken)
	}
	return []byte{id[3], id[2], id[1], id[0], id[5], id[4], id[7], id[6], id[8], id[9], id[10], id[11], id[12], id[13], id[14], id[15]}
}

func sbAMQPHandleRPC(namespace, path string, req *amqp.Message) *amqp.Message {
	value, _ := req.Value.(map[string]any)
	switch op := fmt.Sprint(req.ApplicationProperties["operation"]); op {
	case "com.microsoft:renew-lock":
		tokens, _ := value["lock-tokens"].([]amqp.UUID)
		expirations := make([]time.Time, 0, len(tokens))
		for _, t := range tokens {
			until, err := sbRenewLock(namespace, path, sbLockTokenString(t))
			if err != nil {
				return sbAMQPRPCLockLost(req)
			}
			expirations = append(expirations, until.UTC())
		}
		return sbAMQPRPCValue(req, 200, map[string]any{"expirations": expirations})

	case "com.microsoft:update-disposition":
		tokens, _ := value["lock-tokens"].([]amqp.UUID)
		s := sbSettlement{}
		switch status, _ := value["disposition-status"].(string); status {
		case "completed":
			s.kind = sbComplete
		case "abandoned":
			s.kind = sbAbandon
		case "suspended":
			s.kind = sbDeadLetterIt
		case "defered":
			s.kind = sbDefer
		default:
			return sbAMQPRPCStatus(req, 400, fmt.Sprintf("The disposition status %q is not valid.", status))
		}
		s.deadLetterReason, _ = value["deadletter-reason"].(string)
		s.deadLetterErrorDescription, _ = value["deadletter-description"].(string)
		s.properties, _ = value["properties-to-modify"].(map[string]any)
		for _, t := range tokens {
			if err := sbSettle(namespace, path, sbLockTokenString(t), s); err != nil {
				return sbAMQPRPCLockLost(req)
			}
		}
		return sbAMQPRPCStatus(req, 200, "OK")

	case "com.microsoft:peek-message":
		from, _ := value["from-sequence-number"].(int64)
		count, _ := value["message-count"].(int32)
		if from < 0 || count <= 0 {
			return sbAMQPRPCStatus(req, 400, "from-sequence-number and message-count must be positive.")
		}
		return sbAMQPRPCMessages(req, sbPeek(namespace, path, uint64(from), int(count)), false)

	case "com.microsoft:receive-by-sequence-number":
		raw, _ := value["sequence-numbers"].([]int64)
		seqs := make([]uint64, 0, len(raw))
		for _, n := range raw {
			seqs = append(seqs, uint64(n))
		}
		msgs := sbReceiveDeferred(namespace, path, seqs)
		// receiver-settle-mode 1 is peek-lock; 0 hands the messages over.
		if mode, _ := value["receiver-settle-mode"].(uint32); mode == 0 {
			for _, m := range msgs {
				if err := sbSettle(namespace, path, m.Receipt, sbSettlement{kind: sbComplete}); err != nil {
					return sbAMQPRPCLockLost(req)
				}
			}
			return sbAMQPRPCMessages(req, msgs, false)
		}
		return sbAMQPRPCMessages(req, msgs, true)
	}
	return sbAMQPRPCStatus(req, 501, fmt.Sprintf("The operation %v is not supported.", req.ApplicationProperties["operation"]))
}

// sbAMQPRPCMessages answers with the messages in the shape Service Bus uses:
// {"messages": [{"message": <encoded message>, "lock-token": <uuid>}]}, or
// 204 when there are none.
func sbAMQPRPCMessages(req *amqp.Message, msgs []msgq.Message[sbPayload], locked bool) *amqp.Message {
	if len(msgs) == 0 {
		return sbAMQPRPCStatus(req, 204, "No messages")
	}
	entries := make([]any, 0, len(msgs))
	for _, m := range msgs {
		rendered := sbAMQPMessage(m, locked)
		entry := map[string]any{}
		if locked {
			if id, ok := sbLockTokenUUID(m.Receipt); ok {
				rendered.DeliveryAnnotations = amqp.Annotations{"x-opt-lock-token": id}
				entry["lock-token"] = id
			}
		}
		body, err := rendered.MarshalBinary()
		if err != nil {
			return sbAMQPRPCStatus(req, 500, "encode message: "+err.Error())
		}
		entry["message"] = body
		entries = append(entries, entry)
	}
	return sbAMQPRPCValue(req, 200, map[string]any{"messages": entries})
}
