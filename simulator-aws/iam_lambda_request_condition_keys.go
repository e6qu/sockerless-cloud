package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

func init() {
	registerIAMRequestConditionPopulator("lambda", iamPopulateLambdaRequestConditionKeys)
}

type lambdaConditionRequest struct {
	CodeSigningConfigArn string   `json:"CodeSigningConfigArn"`
	Layers               []string `json:"Layers"`
	Principal            string   `json:"Principal"`
	VpcConfig            struct {
		SubnetIds        []string `json:"SubnetIds"`
		SecurityGroupIds []string `json:"SecurityGroupIds"`
	} `json:"VpcConfig"`
}

// iamPopulateLambdaRequestConditionKeys adds the AWS Lambda keys that describe
// the function configuration a request asks for and the principal a
// permission names.
func iamPopulateLambdaRequestConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	if operation == "RemovePermission" {
		iamSetConditionValues(ctx, "lambda:Principal",
			lambdaStatementPrincipals(sim.PathParam(r, "name"), sim.PathParam(r, "statement"))...)
		return
	}
	var request lambdaConditionRequest
	if !iamDecodeJSONRequest(body, &request) {
		return
	}
	switch operation {
	case "AddPermission":
		iamSetConditionValues(ctx, "lambda:Principal", request.Principal)
	case "PutFunctionCodeSigningConfig":
		iamSetConditionValues(ctx, "lambda:CodeSigningConfigArn", request.CodeSigningConfigArn)
	case "CreateFunction", "UpdateFunctionConfiguration":
		if operation == "CreateFunction" {
			iamSetConditionValues(ctx, "lambda:CodeSigningConfigArn", request.CodeSigningConfigArn)
		}
		iamSetConditionValues(ctx, "lambda:Layer", request.Layers...)
		iamSetConditionValues(ctx, "lambda:VpcIds", ec2SubnetVPCs(request.VpcConfig.SubnetIds)...)
		fallthrough
	case "CreateCapacityProvider":
		iamSetConditionValues(ctx, "lambda:SubnetIds", request.VpcConfig.SubnetIds...)
		iamSetConditionValues(ctx, "lambda:SecurityGroupIds", request.VpcConfig.SecurityGroupIds...)
	}
}

// lambdaStatementPrincipals reads the principal of the function-policy
// statement a RemovePermission names; the request carries only the
// statement's id.
func lambdaStatementPrincipals(function, sid string) []string {
	if function == "" || sid == "" {
		return nil
	}
	statements, _ := lambdaPolicies.Get(function)
	var principals []string
	for _, statement := range statements {
		if statement.Sid != sid {
			continue
		}
		for _, value := range statement.Principal {
			switch v := value.(type) {
			case string:
				principals = append(principals, v)
			case []any:
				for _, item := range v {
					if s, ok := item.(string); ok {
						principals = append(principals, s)
					}
				}
			}
		}
	}
	return principals
}

// ec2SubnetVPCs reads the VPC each stored subnet belongs to. The request names
// subnets only, and the VPC is a fact of the subnet.
func ec2SubnetVPCs(subnets []string) []string {
	var vpcs []string
	for _, id := range subnets {
		if subnet, ok := ec2Subnets.Get(id); ok {
			vpcs = append(vpcs, subnet.VpcId)
		}
	}
	return vpcs
}
