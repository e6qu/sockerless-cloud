package main

import "net/http"

func init() {
	registerIAMRequestConditionPopulator("glue", iamPopulateGlueRequestConditionKeys)
}

type glueConnectionsList struct {
	Connections []string `json:"Connections"`
}

// iamPopulateGlueRequestConditionKeys adds the AWS Glue keys for the network a
// job or an interactive session runs in. A job names connections, not a
// network, so the subnet and security groups are the stored connections'
// physical requirements, and the VPC is the subnet's.
func iamPopulateGlueRequestConditionKeys(_ *http.Request, operation string, body []byte, ctx map[string][]string) {
	var request struct {
		Connections glueConnectionsList `json:"Connections"`
		JobUpdate   struct {
			Connections glueConnectionsList `json:"Connections"`
		} `json:"JobUpdate"`
	}
	switch operation {
	case "CreateJob", "UpdateJob", "CreateSession":
	default:
		return
	}
	if !iamDecodeJSONRequest(body, &request) {
		return
	}
	names := request.Connections.Connections
	if operation == "UpdateJob" {
		names = request.JobUpdate.Connections.Connections
	}
	var subnets, securityGroups []string
	for _, name := range names {
		connection, ok := glueConnections.Get(name)
		if !ok {
			continue
		}
		requirements := connection.PhysicalConnectionRequirements
		if subnet, ok := requirements["SubnetId"].(string); ok {
			subnets = append(subnets, subnet)
		}
		if groups, ok := requirements["SecurityGroupIdList"].([]any); ok {
			for _, group := range groups {
				if id, ok := group.(string); ok {
					securityGroups = append(securityGroups, id)
				}
			}
		}
	}
	iamSetConditionValues(ctx, "glue:SubnetIds", subnets...)
	iamSetConditionValues(ctx, "glue:SecurityGroupIds", securityGroups...)
	iamSetConditionValues(ctx, "glue:VpcIds", ec2SubnetVPCs(subnets)...)
}
