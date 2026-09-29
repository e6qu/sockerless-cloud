package main

import (
	"fmt"
	"net/http"
	"testing"

	amqp "github.com/Azure/go-amqp"
)

// Deleting an Event Hubs namespace deletes its event hubs, and with them every
// event their partitions held: a namespace created again under the same name
// starts with empty partitions.
func TestEventHubsNamespaceDeleteDropsItsHubsPartitionLogs(t *testing.T) {
	srv := newMoveTestServer(t)
	nsPath := fmt.Sprintf("/subscriptions/%s/resourceGroups/eh-delete-rg/providers/Microsoft.EventHub/namespaces/ehdeletens", moveTestSubscription)
	const ver = "?api-version=2024-01-01"

	create := func() {
		t.Helper()
		if status, out := moveARM(t, srv, http.MethodPut, nsPath+ver, `{"location":"eastus","sku":{"name":"Standard","tier":"Standard"}}`); status != http.StatusCreated {
			t.Fatalf("create namespace: %d %s", status, out)
		}
		if status, out := moveARM(t, srv, http.MethodPut, nsPath+"/eventhubs/telemetry"+ver, `{"properties":{"partitionCount":2}}`); status != http.StatusOK {
			t.Fatalf("create event hub: %d %s", status, out)
		}
	}
	create()
	for _, partition := range []string{"0", "1"} {
		ehAMQPEnqueue("ehdeletens", "telemetry/Partitions/"+partition, &amqp.Message{Data: [][]byte{[]byte("event on " + partition)}})
		if _, ok := ehLog.Last(ehPartitionKey("ehdeletens", "telemetry", partition)); !ok {
			t.Fatalf("partition %s holds no event after the send", partition)
		}
	}

	if status, out := moveARM(t, srv, http.MethodDelete, nsPath+ver, ""); status != http.StatusNoContent {
		t.Fatalf("delete namespace: %d %s", status, out)
	}
	for _, partition := range []string{"0", "1"} {
		key := ehPartitionKey("ehdeletens", "telemetry", partition)
		if rec, ok := ehLog.Last(key); ok {
			t.Fatalf("partition %s kept event %d after its namespace was deleted", partition, rec.Seq)
		}
		if head := ehLog.Head(key); head.Next != 0 {
			t.Fatalf("partition %s kept head %+v after its namespace was deleted", partition, head)
		}
	}

	create()
	if _, _, ok := ehAMQPNextEvent("ehdeletens", "telemetry/ConsumerGroups/$Default/Partitions/0", 0); ok {
		t.Fatal("the recreated namespace's hub served an event its predecessor held")
	}
}
