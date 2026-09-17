package main

import (
	"net/http"
	"strconv"
)

func init() {
	registerIAMRequestConditionPopulator("secretsmanager", iamPopulateSecretsManagerRequestConditionKeys)
}

func iamPopulateSecretsManagerRequestConditionKeys(_ *http.Request, operation string, body []byte, ctx map[string][]string) {
	request := iamParseBodyMembers(body)
	if request == nil {
		return
	}
	set := request.setters(ctx)
	switch operation {
	case "CreateSecret":
		set.str("secretsmanager:Name", "Name")
		iamPopulateSecretsManagerSecretSettings(request, ctx)
		iamPopulateSecretsManagerReplication(request, ctx)
	case "UpdateSecret":
		iamPopulateSecretsManagerSecretSettings(request, ctx)
	case "ReplicateSecretToRegions":
		iamPopulateSecretsManagerReplication(request, ctx)
	case "PutResourcePolicy":
		set.boolean("secretsmanager:BlockPublicPolicy", "BlockPublicPolicy")
	case "RotateSecret":
		set.str("secretsmanager:RotationLambdaARN", "RotationLambdaARN")
		set.str("secretsmanager:ExternalSecretRotationRoleArn", "ExternalSecretRotationRoleArn")
		_, modifies := request["RotationRules"]
		ctx["secretsmanager:ModifyRotationRules"] = []string{strconv.FormatBool(modifies)}
		// RotateSecret rotates immediately unless the request says otherwise.
		ctx["secretsmanager:RotateImmediately"] = []string{"true"}
		set.boolean("secretsmanager:RotateImmediately", "RotateImmediately")
	case "DeleteSecret":
		set.boolean("secretsmanager:ForceDeleteWithoutRecovery", "ForceDeleteWithoutRecovery")
		set.number("secretsmanager:RecoveryWindowInDays", "RecoveryWindowInDays")
	case "GetSecretValue":
		set.str("secretsmanager:VersionId", "VersionId")
		set.str("secretsmanager:VersionStage", "VersionStage")
	case "UpdateSecretVersionStage":
		set.str("secretsmanager:VersionStage", "VersionStage")
	}
}

func iamPopulateSecretsManagerSecretSettings(request iamBodyMembers, ctx map[string][]string) {
	set := request.setters(ctx)
	set.str("secretsmanager:Description", "Description")
	set.str("secretsmanager:Type", "Type")
	set.str("secretsmanager:KmsKeyId", "KmsKeyId")
	iamPopulateSecretsManagerKmsKeyArn(request, ctx)
}

func iamPopulateSecretsManagerReplication(request iamBodyMembers, ctx map[string][]string) {
	request.setters(ctx).boolean("secretsmanager:ForceOverwriteReplicaSecret", "ForceOverwriteReplicaSecret")
	var regions []string
	for _, replica := range request.objects("AddReplicaRegions") {
		if region, ok := replica.str("Region"); ok {
			regions = append(regions, region)
		}
	}
	if len(regions) > 0 {
		ctx["secretsmanager:AddReplicaRegions"] = regions
	}
}

// iamPopulateSecretsManagerKmsKeyArn adds the ARN of the AWS KMS key the
// request names by id, alias or ARN. A key the simulator does not hold settles
// no ARN.
func iamPopulateSecretsManagerKmsKeyArn(request iamBodyMembers, ctx map[string][]string) {
	ref, ok := request.str("KmsKeyId")
	if !ok {
		return
	}
	id, ok := resolveKMSKey(ref)
	if !ok {
		return
	}
	if key, found := kmsKeys.Get(id); found {
		ctx["secretsmanager:KmsKeyArn"] = []string{key.Arn}
	}
}
