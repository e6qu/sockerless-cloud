package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestActivityLogFilterEventChannels(t *testing.T) {
	event := ActivityLogEvent{EventTimestamp: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)}
	for raw, want := range map[string]bool{
		"eventTimestamp ge '2026-10-01T00:00:00Z' and eventChannels eq 'Admin, Operation'": true,
		"eventTimestamp ge '2026-10-01T00:00:00Z' and eventChannels eq 'Admin'":            false,
	} {
		filter, err := parseActivityLogFilter(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := filter.matches(event); got != want {
			t.Errorf("%q matched an Operation-channel event: %v, want %v", raw, got, want)
		}
	}
}

func TestActivityLogOperationOf(t *testing.T) {
	const sub = "/subscriptions/s"
	cases := []struct {
		method, path, action, resourceID, group, display string
	}{
		{"PUT", sub + "/resourceGroups/rg", "Microsoft.Resources/subscriptions/resourceGroups/write", sub + "/resourceGroups/rg", "rg", "Create or Update Resource Group"},
		{"DELETE", sub + "/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/a", "Microsoft.Storage/storageAccounts/delete", sub + "/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/a", "rg", "Delete Storage Account"},
		{"POST", sub + "/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/a/listKeys", "Microsoft.Storage/storageAccounts/listKeys/action", sub + "/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/a", "rg", "List Keys Storage Account"},
		{"PUT", sub + "/resourceGroups/rg/providers/Microsoft.ServiceBus/namespaces/ns/queues/q", "Microsoft.ServiceBus/namespaces/queues/write", sub + "/resourceGroups/rg/providers/Microsoft.ServiceBus/namespaces/ns/queues/q", "rg", "Create or Update Queue"},
		{"PUT", sub + "/resourceGroups/rg/providers/Microsoft.Network/publicIPAddresses/ip", "Microsoft.Network/publicIPAddresses/write", sub + "/resourceGroups/rg/providers/Microsoft.Network/publicIPAddresses/ip", "rg", "Create or Update Public IP Address"},
		{"PUT", sub + "/providers/Microsoft.Authorization/roleAssignments/r", "Microsoft.Authorization/roleAssignments/write", sub + "/providers/Microsoft.Authorization/roleAssignments/r", "", "Create or Update Role Assignment"},
	}
	for _, c := range cases {
		op, ok := activityLogOperationOf(httptest.NewRequest(c.method, c.path, nil))
		if !ok {
			t.Fatalf("%s %s is not recorded", c.method, c.path)
		}
		if op.action != c.action || op.resourceID != c.resourceID || op.resourceGroup != c.group {
			t.Errorf("%s %s maps to %+v", c.method, c.path, op)
		}
		if got := activityLogDisplay(op); got != c.display {
			t.Errorf("%s %s displays as %q, want %q", c.method, c.path, got, c.display)
		}
	}
	for _, skipped := range []struct{ method, path string }{
		{"GET", sub + "/resourceGroups/rg"},
		{"POST", sub + "/providers/Microsoft.Storage/checkNameAvailability"},
		{"PUT", "/v1.0/applications"},
	} {
		if _, ok := activityLogOperationOf(httptest.NewRequest(skipped.method, skipped.path, nil)); ok {
			t.Errorf("%s %s is recorded", skipped.method, skipped.path)
		}
	}
}

func TestParseActivityLogFilter(t *testing.T) {
	filter, err := parseActivityLogFilter("eventTimestamp ge '2026-10-01T00:00:00Z' and eventTimestamp le '2026-10-02T00:00:00Z' and resourceGroupName eq 'it''s'")
	if err != nil {
		t.Fatal(err)
	}
	if filter.resourceGroup != "it's" || filter.from.IsZero() || filter.to.IsZero() {
		t.Errorf("parsed %+v", filter)
	}
	if _, err := parseActivityLogFilter("eventTimestamp ge 2026-10-01T00:00:00Z and eventTimestamp le 2026-10-02T00:00:00Z and resourceGroup eq 'rg'"); err == nil {
		t.Error("a field the API does not filter on parsed")
	}
	bare, err := parseActivityLogFilter("eventTimestamp ge 2026-10-01T00:00:00.5 and eventTimestamp le 2026-10-02T00:00:00Z and resourceGroupName eq 'rg'")
	if err != nil || bare.resourceGroup != "rg" {
		t.Errorf("the Azure CLI's bare timestamps parsed as %+v, %v", bare, err)
	}
	for _, bad := range []string{"", "resourceGroupName eq 'rg'", "eventTimestamp ge 'yesterday'", "eventTimestamp ge '2026-10-01T00:00:00Z' and color eq 'red'"} {
		if _, err := parseActivityLogFilter(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
