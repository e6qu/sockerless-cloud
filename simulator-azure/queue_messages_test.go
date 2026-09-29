package main

import (
	"encoding/xml"
	"net/http"
	"testing"
	"time"
)

func queueList(t *testing.T, body []byte) []QueueMessageResponse {
	t.Helper()
	var out queueMessagesList
	if err := xml.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out.Messages
}

func TestQueueStorageMessageLifetimeAndVisibility(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, queue = "ttlqueueacct", "ttl-queue"
	put := func(query, text string) *QueueMessageResponse {
		t.Helper()
		rec := storagePlaneReq(t, srv, http.MethodPost, account, "queue", "/"+queue+"/messages"+query,
			[]byte(`<QueueMessage><MessageText>`+text+`</MessageText></QueueMessage>`), nil)
		assertStatus(t, rec, http.StatusCreated, "PutMessage "+query)
		msgs := queueList(t, rec.Body.Bytes())
		if len(msgs) != 1 || msgs[0].PopReceipt == "" {
			t.Fatalf("PutMessage response = %+v", msgs)
		}
		return &msgs[0]
	}
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "queue", "/"+queue, nil, nil), http.StatusCreated, "CreateQueue")

	forever := put("?messagettl=-1", "forever")
	if forever.ExpirationTime != "Fri, 31 Dec 9999 23:59:59 GMT" {
		t.Fatalf("messagettl=-1 expires %q", forever.ExpirationTime)
	}
	short := put("?messagettl=60", "short")
	insertion, _ := http.ParseTime(short.InsertionTime)
	expiration, _ := http.ParseTime(short.ExpirationTime)
	if expiration.Sub(insertion) != time.Minute {
		t.Fatalf("messagettl=60 expires %v after insertion", expiration.Sub(insertion))
	}
	put("?visibilitytimeout=600", "hidden")

	for _, bad := range []string{"?messagettl=0", "?messagettl=10&visibilitytimeout=10", "?visibilitytimeout=604801"} {
		rec := storagePlaneReq(t, srv, http.MethodPost, account, "queue", "/"+queue+"/messages"+bad,
			[]byte(`<QueueMessage><MessageText>x</MessageText></QueueMessage>`), nil)
		assertStatus(t, rec, http.StatusBadRequest, "PutMessage "+bad)
	}

	rec := storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue+"?comp=metadata", nil, nil)
	if got := rec.Header().Get("x-ms-approximate-messages-count"); got != "3" {
		t.Fatalf("approximate count = %s, want 3 counting the invisible message", got)
	}

	// Peek returns one message unless asked for more, and never an invisible one.
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue+"/messages?peekonly=true", nil, nil)
	if msgs := queueList(t, rec.Body.Bytes()); len(msgs) != 1 || msgs[0].MessageText != "forever" {
		t.Fatalf("peek = %+v", msgs)
	}
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue+"/messages?peekonly=true&numofmessages=32", nil, nil)
	if msgs := queueList(t, rec.Body.Bytes()); len(msgs) != 2 {
		t.Fatalf("peek 32 = %+v", msgs)
	}
	assertStatus(t, storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue+"/messages?numofmessages=33", nil, nil),
		http.StatusBadRequest, "GetMessages numofmessages=33")

	// A message past its time to live is gone.
	queueData.Update(queueKey(account, queue), func(q *QueueData) {
		for i := range q.Messages.Messages {
			if q.Messages.Messages[i].Payload.Text == "short" {
				q.Messages.Messages[i].ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
			}
		}
	})
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue+"/messages?numofmessages=32", nil, nil)
	msgs := queueList(t, rec.Body.Bytes())
	if len(msgs) != 1 || msgs[0].MessageText != "forever" || msgs[0].DequeueCount != 1 {
		t.Fatalf("get = %+v", msgs)
	}

	// The pop receipt Put Message returned lets its holder delete the
	// invisible message.
	hidden := put("?visibilitytimeout=600", "second-hidden")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "queue",
		"/"+queue+"/messages/"+hidden.MessageID+"?popreceipt="+hidden.PopReceipt, nil, nil),
		http.StatusNoContent, "DeleteMessage with the Put Message pop receipt")
}
