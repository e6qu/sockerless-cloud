package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestCloudMapRequestConditionKeysReadTheNamedNamespaceAndService(t *testing.T) {
	AwaitSimulatorBackground()
	cmNamespaces = sim.MakeStore[CMNamespace](nil, "cloudmap_namespaces")
	cmServices = sim.MakeStore[CMService](nil, "cloudmap_services")
	const nsARN = "arn:aws:servicediscovery:us-east-1:123456789012:namespace/ns-orders"
	const svcARN = "arn:aws:servicediscovery:us-east-1:123456789012:service/srv-orders"
	cmNamespaces.Put("ns-orders", CMNamespace{Id: "ns-orders", Name: "orders.local", Arn: nsARN})
	cmServices.Put("srv-orders", CMService{Id: "srv-orders", Name: "api", NamespaceId: "ns-orders", Arn: svcARN})
	creator := []string{awsAccountID()}

	cases := []struct {
		name, operation, body string
		want                  map[string][]string
	}{
		{"http service", "CreateService", `{"Name":"api","NamespaceId":"ns-orders"}`,
			map[string][]string{"servicediscovery:NamespaceArn": {nsARN}}},
		{"dns service", "CreateService", `{"Name":"api","DnsConfig":{"NamespaceId":"ns-orders","DnsRecords":[]}}`,
			map[string][]string{"servicediscovery:NamespaceArn": {nsARN}}},
		{"unknown namespace", "CreateService", `{"Name":"api","NamespaceId":"ns-missing"}`,
			map[string][]string{}},
		{"discover", "DiscoverInstances", `{"NamespaceName":"orders.local","ServiceName":"api"}`,
			map[string][]string{"servicediscovery:NamespaceName": {"orders.local"}, "servicediscovery:ServiceName": {"api"}}},
		{"discover revision", "DiscoverInstancesRevision", `{"NamespaceName":"orders.local","ServiceName":"api"}`,
			map[string][]string{"servicediscovery:NamespaceName": {"orders.local"}, "servicediscovery:ServiceName": {"api"}}},
		{"register", "RegisterInstance", `{"ServiceId":"srv-orders","InstanceId":"i-1","Attributes":{}}`,
			map[string][]string{"servicediscovery:ServiceArn": {svcARN}, "servicediscovery:ServiceCreatedByAccount": creator}},
		{"list", "ListInstances", `{"ServiceId":"srv-orders"}`,
			map[string][]string{"servicediscovery:ServiceArn": {svcARN}}},
		{"attributes", "UpdateServiceAttributes", `{"ServiceId":"srv-orders","Attributes":{"a":"b"}}`,
			map[string][]string{"servicediscovery:ServiceCreatedByAccount": creator}},
		{"unknown service", "DeregisterInstance", `{"ServiceId":"srv-missing","InstanceId":"i-1"}`,
			map[string][]string{}},
		{"absent members", "DiscoverInstances", `{}`, map[string][]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertConditionValues(t, jsonConditionContext("servicediscovery", c.operation, c.body), c.want)
		})
	}
}
