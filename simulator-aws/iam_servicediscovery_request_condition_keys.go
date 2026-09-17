package main

import "net/http"

func init() {
	registerIAMRequestConditionPopulator("servicediscovery", iamPopulateCloudMapRequestConditionKeys)
}

func iamPopulateCloudMapRequestConditionKeys(_ *http.Request, operation string, body []byte, ctx map[string][]string) {
	request := iamParseBodyMembers(body)
	if request == nil {
		return
	}
	set := request.setters(ctx)
	switch operation {
	case "CreateService":
		// A DNS service may name its namespace inside DnsConfig instead.
		id, ok := request.str("NamespaceId")
		if !ok {
			id, ok = request.object("DnsConfig").str("NamespaceId")
		}
		if !ok {
			return
		}
		if namespace, found := cmNamespaces.Get(id); found {
			ctx["servicediscovery:NamespaceArn"] = []string{namespace.Arn}
		}
	case "DiscoverInstances", "DiscoverInstancesRevision":
		set.str("servicediscovery:NamespaceName", "NamespaceName")
		set.str("servicediscovery:ServiceName", "ServiceName")
	case "DeregisterInstance", "RegisterInstance", "UpdateInstanceCustomHealthStatus",
		"GetInstance", "GetInstancesHealthStatus", "ListInstances",
		"DeleteServiceAttributes", "UpdateServiceAttributes":
		id, ok := request.str("ServiceId")
		if !ok {
			return
		}
		service, found := cmServices.Get(id)
		if !found {
			return
		}
		attributes := operation == "DeleteServiceAttributes" || operation == "UpdateServiceAttributes"
		read := operation == "GetInstance" || operation == "GetInstancesHealthStatus" || operation == "ListInstances"
		if !attributes {
			ctx["servicediscovery:ServiceArn"] = []string{service.Arn}
		}
		if !read {
			// Every AWS Cloud Map service here was created through the
			// account this simulator serves.
			ctx["servicediscovery:ServiceCreatedByAccount"] = []string{awsAccountID()}
		}
	}
}
